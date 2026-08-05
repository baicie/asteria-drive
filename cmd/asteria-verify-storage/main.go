package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/baicie/asteria-drive/internal/buildinfo"
	"github.com/baicie/asteria-drive/internal/config"
	"github.com/baicie/asteria-drive/internal/recovery"
	"github.com/baicie/asteria-drive/internal/s3store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(buildinfo.String("asteria-verify-storage"))
		return
	}
	batchSize := flag.Int("batch-size", 500, "number of PostgreSQL rows read per batch")
	concurrency := flag.Int("concurrency", 16, "maximum concurrent S3 HeadObject calls")
	maxFindings := flag.Int("max-findings", 100, "maximum individual findings included in JSON")
	timeout := flag.Duration("timeout", 12*time.Hour, "overall verification timeout")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fail("configuration is invalid", err)
	}
	if cfg.MetadataDriver != "postgres" || cfg.StorageDriver != "s3" {
		fail("storage verification requires PostgreSQL and S3", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	catalog, err := recovery.NewPostgresCatalog(ctx, cfg.DatabaseURL)
	if err != nil {
		fail("catalog initialization failed", err)
	}
	defer catalog.Close()
	storage, err := s3store.New(ctx, s3store.Options{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket,
		AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey, UsePathStyle: cfg.S3PathStyle,
		AutoCreateBucket: false, EnableChecksumHeaders: cfg.S3ChecksumHeaders,
	})
	if err != nil {
		fail("storage initialization failed", err)
	}
	verifier, err := recovery.NewVerifier(recovery.Options{
		Catalog: catalog, Storage: storage, BatchSize: *batchSize,
		Concurrency: *concurrency, MaxFindings: *maxFindings,
	})
	if err != nil {
		fail("verification options are invalid", err)
	}
	report, err := verifier.Verify(ctx)
	if err != nil {
		fail("storage verification failed", err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fail("encode verification report", err)
	}
	if !report.Verified {
		os.Exit(2)
	}
}

func fail(message string, err error) {
	if err == nil {
		fmt.Fprintln(os.Stderr, message)
	} else {
		fmt.Fprintf(os.Stderr, "%s: %v\n", message, err)
	}
	os.Exit(1)
}

// asteria-loadtest creates deterministic namespace fixtures for capacity tests.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
	"github.com/baicie/asteria-drive/internal/loadtest"
)

func main() {
	databaseURL := flag.String("database-url", os.Getenv("ASTERIA_LOADTEST_DATABASE_URL"), "PostgreSQL connection URL")
	tenantID := flag.String("tenant-id", "", "tenant UUID to create or reuse")
	tenantName := flag.String("tenant-name", "Asteria load test", "display name used only when creating the tenant")
	rootID := flag.String("root-id", "", "expected root UUID; empty derives one for a new tenant")
	nodes := flag.Int64("nodes", 1000000, "number of directory descendants to generate")
	fanout := flag.Int64("fanout", 32, "directories per parent")
	batchSize := flag.Int("batch-size", 10000, "COPY rows per database batch")
	seed := flag.String("seed", "asteria-loadtest-v1", "deterministic ID and name seed")
	write := flag.Bool("write", false, "insert into PostgreSQL; otherwise validate and print the generated plan")
	analyze := flag.Bool("analyze", true, "run ANALYZE file_node after a successful write")
	timeout := flag.Duration("timeout", 45*time.Minute, "overall write deadline")
	output := flag.String("output", "", "JSON report destination; default stdout")
	flag.Parse()

	if !drive.ValidID(*tenantID) {
		fatalf("tenant-id must be a UUID")
	}
	if *rootID != "" && !drive.ValidID(*rootID) {
		fatalf("root-id must be a UUID")
	}
	if *batchSize < 1 || *batchSize > 100000 {
		fatalf("batch-size must be between 1 and 100000")
	}
	resolvedRoot := *rootID
	if resolvedRoot == "" {
		resolvedRoot = loadtest.DeterministicUUID(*tenantID+":"+*seed+":root", 0)
	}
	plan := loadtest.TreeOptions{TenantID: *tenantID, RootID: resolvedRoot, Count: *nodes, Fanout: *fanout, Seed: *seed}
	if err := plan.Validate(); err != nil {
		fatalf("invalid load plan: %v", err)
	}
	if !*write {
		writeJSON(map[string]any{
			"mode": "dry_run", "tenant_id": *tenantID, "root_id": resolvedRoot, "seed": *seed,
			"nodes": *nodes, "fanout": *fanout, "batch_size": *batchSize,
			"message": "Pass -write with an isolated performance database to load this plan.",
		}, *output)
		return
	}
	if strings.TrimSpace(*databaseURL) == "" {
		fatalf("database-url or ASTERIA_LOADTEST_DATABASE_URL is required with -write")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	report, err := loadtest.Load(ctx, loadtest.LoaderOptions{
		DatabaseURL: *databaseURL, TenantID: *tenantID, TenantName: *tenantName, RootID: *rootID,
		Count: *nodes, Fanout: *fanout, BatchSize: *batchSize, Seed: *seed, Analyze: *analyze,
	})
	if err != nil {
		fatalf("load namespace: %v", err)
	}
	writeJSON(report, *output)
}

func writeJSON(value any, output string) {
	var writer io.Writer = os.Stdout
	var file *os.File
	var err error
	if output != "" {
		file, err = os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fatalf("create report: %v", err)
		}
		defer file.Close()
		writer = file
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fatalf("write report: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

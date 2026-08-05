// asteria-slo runs a bounded, repeatable control-plane SLO sampling workload.
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

	"github.com/baicie/asteria-drive/internal/loadtest"
)

func main() {
	baseURL := flag.String("base-url", os.Getenv("ASTERIA_SLO_BASE_URL"), "Asteria API base URL")
	token := flag.String("token", os.Getenv("ASTERIA_SLO_TOKEN"), "bearer token; prefer ASTERIA_SLO_TOKEN")
	rootID := flag.String("root-id", os.Getenv("ASTERIA_SLO_ROOT_ID"), "tenant root directory ID")
	duration := flag.Duration("duration", 10*time.Minute, "measured duration")
	warmup := flag.Duration("warmup", 5*time.Minute, "unreported warmup duration")
	rate := flag.Int("rate", 50, "total target requests per second")
	concurrency := flag.Int("concurrency", 16, "maximum in-flight requests")
	timeout := flag.Duration("request-timeout", 5*time.Second, "per-request timeout")
	healthOnly := flag.Bool("health-only", false, "only sample GET /healthz; token and root-id are not required")
	includeDirectoryWrites := flag.Bool("include-directory-writes", false, "include POST /api/v1/directories against write-parent-id")
	writeParentID := flag.String("write-parent-id", os.Getenv("ASTERIA_SLO_WRITE_PARENT_ID"), "directory parent used only with -include-directory-writes")
	output := flag.String("output", "", "JSON report destination; default stdout")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := loadtest.RunSLO(ctx, loadtest.SLOOptions{
		BaseURL: strings.TrimRight(*baseURL, "/"), Token: *token, RootID: *rootID,
		Duration: *duration, Warmup: *warmup, Rate: *rate, Concurrency: *concurrency,
		Timeout: *timeout, HealthOnly: *healthOnly, IncludeDirectoryWrites: *includeDirectoryWrites,
		WriteParentID: *writeParentID,
	})
	if err != nil {
		fatalf("run SLO workload: %v", err)
	}
	var writer io.Writer = os.Stdout
	var file *os.File
	if *output != "" {
		file, err = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			fatalf("create report: %v", err)
		}
		defer file.Close()
		writer = file
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatalf("write report: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

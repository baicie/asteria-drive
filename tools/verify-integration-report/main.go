package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/baicie/asteria-drive/internal/cicheck"
)

func main() {
	manifestPath := flag.String("manifest", ".github/integration-tests.json", "path to the required integration test manifest")
	reportPath := flag.String("input", "integration-test.raw.json", "path to go test -json output")
	flag.Parse()

	manifestFile, err := os.Open(*manifestPath)
	if err != nil {
		fatalf("open integration manifest: %v", err)
	}
	manifest, err := cicheck.LoadIntegrationManifest(manifestFile)
	closeErr := manifestFile.Close()
	if err != nil {
		fatalf("load integration manifest: %v", err)
	}
	if closeErr != nil {
		fatalf("close integration manifest: %v", closeErr)
	}

	reportFile, err := os.Open(*reportPath)
	if err != nil {
		fatalf("open integration report: %v", err)
	}
	err = cicheck.VerifyIntegrationReport(reportFile, manifest)
	closeErr = reportFile.Close()
	if err != nil {
		fatalf("verify integration report: %v", err)
	}
	if closeErr != nil {
		fatalf("close integration report: %v", closeErr)
	}

	count := 0
	for _, requirement := range manifest.Packages {
		count += len(requirement.Tests)
	}
	fmt.Printf("verified %d required integration tests across %d packages\n", count, len(manifest.Packages))
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}

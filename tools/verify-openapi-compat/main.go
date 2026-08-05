package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/baicie/asteria-drive/internal/cicheck"
)

func main() {
	base := flag.String("base", "", "base OpenAPI document")
	current := flag.String("current", "", "current OpenAPI document")
	flag.Parse()
	if *base == "" || *current == "" {
		fmt.Fprintln(os.Stderr, "base and current are required")
		os.Exit(2)
	}
	if err := cicheck.VerifyOpenAPICompatibility(*base, *current); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

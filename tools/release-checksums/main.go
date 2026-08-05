package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/baicie/asteria-drive/internal/release"
)

func main() {
	directory := flag.String("dir", "", "release directory")
	output := flag.String("output", "checksums.txt", "checksum file name")
	flag.Parse()
	if *directory == "" {
		fmt.Fprintln(os.Stderr, "dir is required")
		os.Exit(2)
	}
	if err := release.WriteChecksums(*directory, *output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

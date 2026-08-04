package main

import (
	"fmt"
	"net/url"
	"os"

	"github.com/baicie/asteria-drive/internal/cicheck"
)

func main() {
	secrets := []string{
		os.Getenv("ASTERIA_TEST_DATABASE_URL"),
		os.Getenv("ASTERIA_TEST_S3_ACCESS_KEY"),
		os.Getenv("ASTERIA_TEST_S3_SECRET_KEY"),
	}
	if databaseURL, err := url.Parse(os.Getenv("ASTERIA_TEST_DATABASE_URL")); err == nil && databaseURL.User != nil {
		if password, ok := databaseURL.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	if err := cicheck.SanitizeLog(os.Stdin, os.Stdout, secrets); err != nil {
		fmt.Fprintf(os.Stderr, "sanitize CI log: %v\n", err)
		os.Exit(1)
	}
}

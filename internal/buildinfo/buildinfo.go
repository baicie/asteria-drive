package buildinfo

import "fmt"

// These values are replaced by release builds and remain useful for local builds.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func String(binary string) string {
	return fmt.Sprintf("%s %s (commit %s, built %s)", binary, Version, Commit, Date)
}

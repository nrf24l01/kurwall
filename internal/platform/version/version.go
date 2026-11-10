// Package version exposes build metadata injected via -ldflags.
package version

import "fmt"

var (
	// Version is the semantic version of the binary.
	Version = "dev"
	// Commit is the git revision the binary was built from.
	Commit = "unknown"
	// BuildDate is the UTC build timestamp.
	BuildDate = "unknown"
)

// String renders a compact build identifier.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuildDate)
}

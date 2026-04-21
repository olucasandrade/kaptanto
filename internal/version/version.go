package version

import "fmt"

// These variables are overwritten at link time via -ldflags.
// Default values identify local/untagged builds.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// String returns the full human-readable version line.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuildDate)
}

package version_test

import (
	"testing"

	"github.com/olucasandrade/kaptanto/internal/version"
)

func TestString_FormatsVersionCommitBuildDate(t *testing.T) {
	origVersion, origCommit, origBuildDate := version.Version, version.Commit, version.BuildDate
	t.Cleanup(func() {
		version.Version, version.Commit, version.BuildDate = origVersion, origCommit, origBuildDate
	})

	version.Version = "1.2.3"
	version.Commit = "abc1234"
	version.BuildDate = "2026-07-03T00:00:00Z"

	got := version.String()
	want := "1.2.3 (commit abc1234, built 2026-07-03T00:00:00Z)"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

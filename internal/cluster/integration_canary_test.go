package cluster

import (
	"os"
	"testing"
)

// TestIntegrationCanary_TEST_CLUSTER_DSN fails the build when running under
// GitHub Actions and TEST_CLUSTER_DSN is unset. A green integration job does
// not, by itself, prove that TestNodeHeartbeater (this package's gated
// suite) actually ran — it proves only that nothing failed, which is equally
// true whether the suite ran or silently t.Skip()'d. This canary turns that
// silent skip into a hard CI failure.
//
// It must never fire outside CI: the check is keyed strictly on
// GITHUB_ACTIONS=true, so local `go test ./...` runs without
// TEST_CLUSTER_DSN continue to skip the gated suite quietly, as before.
func TestIntegrationCanary_TEST_CLUSTER_DSN(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("TEST_CLUSTER_DSN") == "" {
		t.Fatal("TEST_CLUSTER_DSN must be set in CI: internal/cluster's gated integration tests would silently skip, defeating the purpose of running them in CI")
	}
}

package checkpoint_test

import (
	"os"
	"testing"
)

// TestIntegrationCanary_POSTGRES_TEST_DSN fails the build when running under
// GitHub Actions and POSTGRES_TEST_DSN is unset. A green integration job does
// not, by itself, prove that this package's gated Postgres checkpoint-store
// tests actually ran — it proves only that nothing failed, which is equally
// true whether the suite ran or silently t.Skip()'d. This canary turns that
// silent skip into a hard CI failure.
//
// It must never fire outside CI: the check is keyed strictly on the integration job
// (GITHUB_JOB=="integration"), so local `go test ./...` runs without
// POSTGRES_TEST_DSN continue to skip the gated suite quietly, as before.
func TestIntegrationCanary_POSTGRES_TEST_DSN(t *testing.T) {
	if os.Getenv("GITHUB_JOB") == "integration" && os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Fatal("POSTGRES_TEST_DSN must be set in CI: internal/checkpoint's gated Postgres tests would silently skip, defeating the purpose of running them in CI")
	}
}

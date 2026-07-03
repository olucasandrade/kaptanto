package mongodb_test

import (
	"os"
	"testing"
)

// TestIntegrationCanary_MONGO_TEST_URI fails the build when running under
// GitHub Actions and MONGO_TEST_URI is unset. A green integration job does
// not, by itself, prove that this package's gated Change Stream tests
// actually ran — it proves only that nothing failed, which is equally true
// whether the suite ran or silently t.Skip()'d. This canary turns that
// silent skip into a hard CI failure.
//
// It must never fire outside CI: the check is keyed strictly on the integration job
// (GITHUB_JOB=="integration"), so local `go test ./...` runs without
// MONGO_TEST_URI continue to skip the gated suite quietly, as before.
func TestIntegrationCanary_MONGO_TEST_URI(t *testing.T) {
	if os.Getenv("GITHUB_JOB") == "integration" && os.Getenv("MONGO_TEST_URI") == "" {
		t.Fatal("MONGO_TEST_URI must be set in CI: internal/source/mongodb's gated Change Stream tests would silently skip, defeating the purpose of running them in CI")
	}
}

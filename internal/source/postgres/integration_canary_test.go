package postgres

import (
	"os"
	"testing"
)

// TestIntegrationCanary_POSTGRES_TEST_DSN fails the build when running under
// GitHub Actions and POSTGRES_TEST_DSN is unset. A green integration job does
// not, by itself, prove that TestEnsurePublication_Integration_AllowAllTablesTrue
// actually ran — it proves only that nothing failed, which is equally true
// whether the test ran or silently t.Skip()'d (this exact failure mode
// previously hid an unconditional skip in this package for an unknown
// period of time). This canary turns a silent skip into a hard CI failure.
//
// It must never fire outside CI: the check is keyed strictly on
// GITHUB_ACTIONS=true, so local `go test ./...` runs without
// POSTGRES_TEST_DSN continue to skip the gated test quietly, as before.
func TestIntegrationCanary_POSTGRES_TEST_DSN(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Fatal("POSTGRES_TEST_DSN must be set in CI: internal/source/postgres's gated publication tests would silently skip, defeating the purpose of running them in CI")
	}
}

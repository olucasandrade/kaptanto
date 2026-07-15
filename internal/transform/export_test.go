package transform

import "time"

// SetJQRuntimeTimeoutForTest replaces the jq runtime timeout for the duration
// of a test. It is exported only to tests in the same package.
func SetJQRuntimeTimeoutForTest(d time.Duration) func() {
	old := jqRuntimeTimeout
	jqRuntimeTimeout = d
	return func() { jqRuntimeTimeout = old }
}

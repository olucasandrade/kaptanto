package redact

import "testing"

func TestURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "slack webhook path is dropped",
			in:   "https://hooks.slack.com/services/T0123/B0456/xXxSecretToken",
			want: "https://hooks.slack.com",
		},
		{
			name: "query and fragment dropped",
			in:   "https://example.com/hook?token=abc123#frag",
			want: "https://example.com",
		},
		{
			name: "userinfo dropped",
			in:   "https://user:pass@example.com:8443/path",
			want: "https://example.com:8443",
		},
		{
			name: "http scheme kept",
			in:   "http://127.0.0.1:9000/events",
			want: "http://127.0.0.1:9000",
		},
		{
			name: "non-http scheme keeps scheme and host only",
			in:   "ftp://files.example.com/etc/passwd",
			want: "ftp://files.example.com",
		},
		{
			name: "schemeless host-only input fails closed",
			in:   "example.com/secret-path",
			want: "<redacted-url>",
		},
		{
			name: "no host fails closed",
			in:   "file:///etc/passwd",
			want: "<redacted-url>",
		},
		{
			name: "unparseable fails closed",
			in:   "http://exa mple.com/path",
			want: "<redacted-url>",
		},
		{
			name: "empty input fails closed",
			in:   "",
			want: "<redacted-url>",
		},
		{
			name: "surrounding whitespace trimmed",
			in:   "  https://example.com/hook  ",
			want: "https://example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := URL(tc.in); got != tc.want {
				t.Fatalf("URL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestURL_NeverLeaksSecret(t *testing.T) {
	const secret = "xXxSecretToken"
	out := URL("https://hooks.slack.com/services/T0123/B0456/" + secret)
	if out == "" || len(out) == 0 {
		t.Fatal("expected non-empty redaction")
	}
	if contains := containsSubstring(out, secret); contains {
		t.Fatalf("redacted URL %q leaks secret %q", out, secret)
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

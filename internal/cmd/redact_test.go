package cmd

import (
	"strings"
	"testing"
)

func TestRedactDSN(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		secret   string // substring that must NOT appear in the output (empty = skip)
		wantHost string // substring that MUST still appear (empty = skip)
	}{
		{"postgres url", "postgres://user:s3cr3t@db.example:5432/app", "s3cr3t", "db.example:5432"},
		{"mongodb url", "mongodb://admin:hunter2@mongo:27017/app", "hunter2", "mongo:27017"},
		{"mongodb+srv url", "mongodb+srv://u:p%40ss@cluster.example/db", "p%40ss", "cluster.example"},
		{"amqp url", "amqp://guest:guestpw@rabbit:5672/", "guestpw", "rabbit:5672"},
		{"libpq keyword", "host=db user=app password=topsecret sslmode=require", "topsecret", "host=db"},
		{"libpq quoted", "host=db password='a b c' user=app", "a b c", "host=db"},
		{"query password", "postgres://user@host/db?password=secret", "secret", "host"},
		{"query password among params", "postgres://host/db?application_name=a&password=secret", "secret", "application_name"},
		{"query sslpassword", "postgresql://user@host/db?sslpassword=secret", "secret", "host"},
		{"userinfo and query password (query)", "postgres://user:upw@host/db?password=qpw", "qpw", "host"},
		{"userinfo and query password (userinfo)", "postgres://user:upw@host/db?password=qpw", "upw", "host"},
		{"no password url", "postgres://app@db.example/app", "", "db.example"},
		{"no credentials", "postgres://db.example:5432/app", "", "db.example"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDSN(tc.in)
			if tc.secret != "" && strings.Contains(got, tc.secret) {
				t.Fatalf("redactDSN(%q) = %q, leaks secret %q", tc.in, got, tc.secret)
			}
			if tc.wantHost != "" && !strings.Contains(got, tc.wantHost) {
				t.Fatalf("redactDSN(%q) = %q, missing host marker %q", tc.in, got, tc.wantHost)
			}
		})
	}
}

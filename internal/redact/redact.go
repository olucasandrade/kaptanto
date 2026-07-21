// Package redact provides helpers for stripping secrets from values before
// they appear in logs, returned errors, or DLQ reason strings (ACT-02:
// secrets are never logged).
package redact

import (
	"net/url"
	"strings"
)

// URL returns raw reduced to scheme://host, dropping userinfo, path, query,
// and fragment — the parts that typically carry credentials. For webhook URLs
// such as https://hooks.slack.com/services/T…/B…/xxx or
// https://inn.gs/e/<event-key>, the path (and sometimes the query) IS the
// credential, so none of it may survive into an error string.
//
// The function fails closed: if raw cannot be parsed, or yields no host,
// it returns a fixed placeholder instead of any part of raw.
func URL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "<redacted-url>"
	}
	return u.Scheme + "://" + u.Host
}

package cmd

import (
	"net/url"
	"regexp"
	"strings"
)

// dsnKeywordPassword matches a libpq keyword/value password field, e.g.
// `password=secret` or `password='se cret'`, capturing the keyword prefix.
var dsnKeywordPassword = regexp.MustCompile(`(?i)(password\s*=\s*)('[^']*'|"[^"]*"|\S+)`)

// urlUserinfoPassword matches the password in a URL userinfo section, e.g. the
// `secret` in `scheme://user:secret@host`. Used as a fallback when url.Parse
// fails so a credential never leaks through an unparseable URI.
var urlUserinfoPassword = regexp.MustCompile(`(://[^:@/?#]+:)[^@/?#]*(@)`)

// redactDSN returns a copy of a database DSN/URI safe for logging: any embedded
// password is replaced with "xxxxx". It handles URL-style DSNs (postgres://,
// mongodb://, mongodb+srv://, amqp://, ...) and libpq keyword=value strings.
//
// The function fails closed: if it cannot confidently parse the input it still
// strips anything that looks like a password rather than returning it verbatim.
func redactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}

	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			if u.User != nil {
				if _, hasPass := u.User.Password(); hasPass {
					u.User = url.UserPassword(u.User.Username(), "xxxxx")
				}
			}
			// Passwords also travel as query parameters (libpq ?password=,
			// ?sslpassword=, and driver variants), so scrub any param whose name
			// ends in "password".
			if q := u.Query(); len(q) > 0 {
				changed := false
				for key, vals := range q {
					if strings.HasSuffix(strings.ToLower(key), "password") {
						for i := range vals {
							vals[i] = "xxxxx"
						}
						q[key] = vals
						changed = true
					}
				}
				if changed {
					u.RawQuery = q.Encode()
				}
			}
			return u.String()
		}
		// Unparseable URL: strip the userinfo password directly so it never leaks.
		return urlUserinfoPassword.ReplaceAllString(dsn, "${1}xxxxx${2}")
	}

	if dsnKeywordPassword.MatchString(dsn) {
		return dsnKeywordPassword.ReplaceAllString(dsn, "${1}xxxxx")
	}

	return dsn
}

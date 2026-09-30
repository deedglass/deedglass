package config

import (
	"net"
	"net/url"
	"strings"
)

// Postgres URLs are accepted in URL form only (postgres:// or postgresql://). Messages about them
// never repeat the value, because it may hold a password.

func checkPostgresURL(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return "isn't a valid URL."
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "must be a postgres:// URL."
	}
	if u.Opaque != "" {
		return "must be a postgres:// URL with a host or a socket directory."
	}
	return ""
}

func hasPassword(v string) bool {
	u, err := url.Parse(v)
	if err != nil {
		return true // unknown: treat it as secret
	}
	if _, ok := u.User.Password(); ok {
		return true
	}
	return u.Query().Has("password")
}

// pgHost returns the host the URL connects to: the URL host, or the host query parameter.
func pgHost(u *url.URL) string {
	if h := u.Query().Get("host"); h != "" {
		return h
	}
	return u.Hostname()
}

func isLocalHost(h string) bool {
	if h == "" || strings.HasPrefix(h, "/") || h == "localhost" {
		return true // no host means a Unix socket
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// prodSSL returns a problem message when a prod URL to a remote host doesn't require TLS.
func prodSSL(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return ""
	}
	if isLocalHost(pgHost(u)) {
		return ""
	}
	switch mode := u.Query().Get("sslmode"); mode {
	case "require", "verify-full":
		return ""
	case "":
		return "has no sslmode, and prod needs TLS to a remote host."
	default:
		return "has sslmode=" + mode + ", and prod needs require or verify-full to a remote host."
	}
}

// maskPostgresURL hides the password (in the user info or a password parameter) and keeps the rest.
func maskPostgresURL(v string) string {
	u, err := url.Parse(v)
	if err != nil {
		return Redacted
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), "__REDACTED__")
		}
	}
	if q := u.Query(); q.Has("password") {
		q.Set("password", "__REDACTED__")
		u.RawQuery = q.Encode()
	}
	return strings.ReplaceAll(u.String(), "__REDACTED__", Redacted)
}

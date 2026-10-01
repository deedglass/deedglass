package identity

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Email limits (users.email has the same CHECK).
const (
	MaxEmail      = 254
	maxEmailLocal = 64
)

// EmailError is an email address that can't be used. Its message is the brand copy for
// validation_failed on "email".
type EmailError struct{ Value string }

func (e *EmailError) Error() string {
	v := e.Value
	if utf8.RuneCountInString(v) > 80 {
		v = string([]rune(v)[:80]) + "…"
	}
	return fmt.Sprintf("%q isn't a complete email address. Use the form name@company.com.", v)
}

// NormalizeEmail trims and lower-cases an address and checks its shape: one @, a local part of
// 1–64 characters, a domain with at least one dot and no empty labels, 3–254 characters in all,
// and no spaces, control characters, quotes or angle brackets. It doesn't check that the mailbox
// exists. Normalizing twice gives the same result.
func NormalizeEmail(s string) (string, error) {
	raw := s
	s = strings.ToLower(strings.TrimSpace(s))
	bad := func() (string, error) { return "", &EmailError{Value: strings.TrimSpace(raw)} }
	if len(s) < 3 || len(s) > MaxEmail || !utf8.ValidString(s) {
		return bad()
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || len(local) > maxEmailLocal || strings.Contains(domain, "@") {
		return bad()
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f || strings.ContainsRune(`"<>(),;:\[]`, r) {
			return bad()
		}
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return bad()
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return bad()
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return bad()
		}
	}
	return s, nil
}

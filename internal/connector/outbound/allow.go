//go:build outboundmail

package outbound

import (
	"regexp"
	"strings"
)

// The endpoint allow-list (S40 §3.3), compiled in. The request builder refuses every other method
// and path before a byte leaves. Unreachable by construction: drafts, modify, trash, untrash, any
// DELETE, settings, import, insert, batch*, watch (TestForbiddenEndpoints counts 0 requests).
var allowList = []struct {
	method string
	re     *regexp.Regexp
}{
	{"POST", regexp.MustCompile(`^/gmail/v1/users/me/messages/send$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/me/messages/[A-Za-z0-9_-]{1,128}$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/me/messages$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/me/history$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/me/threads/[A-Za-z0-9_-]{1,128}$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/me/profile$`)},
}

var forbiddenSegments = []string{"drafts", "modify", "trash", "untrash", "import", "insert", "watch", "settings", "attachments"}

// Allowed reports whether method and the decoded path may be requested.
func Allowed(method, path string) bool {
	for _, p := range strings.Split(path, "/") {
		for _, f := range forbiddenSegments {
			if p == f {
				return false
			}
		}
		if strings.HasPrefix(p, "batch") {
			return false
		}
	}
	// "send" only as the last segment of the one send path.
	if strings.Contains(path, "/send") && !(method == "POST" && path == "/gmail/v1/users/me/messages/send") {
		return false
	}
	for _, a := range allowList {
		if a.method == method && a.re.MatchString(path) {
			return true
		}
	}
	return false
}

// The mail.list_messages query grammar: exactly one of three forms.
var (
	reQInbox  = regexp.MustCompile(`^in:inbox newer_than:([1-9]|1[0-4])d$`)
	reQSent   = regexp.MustCompile(`^in:sent after:\d{4}/\d{2}/\d{2} before:\d{4}/\d{2}/\d{2}$`)
	reQMsgID  = regexp.MustCompile(`^rfc822msgid:<?[A-Za-z0-9._%+=-]{1,128}@[A-Za-z0-9.-]{1,253}>?$`)
	reQLabels = []*regexp.Regexp{reQInbox, reQSent, reQMsgID}
)

// ValidQuery reports whether q is one of the three query forms mail.list_messages accepts.
func ValidQuery(q string) bool {
	for _, re := range reQLabels {
		if re.MatchString(q) {
			return true
		}
	}
	return false
}

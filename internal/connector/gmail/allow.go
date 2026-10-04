package gmail

import (
	"regexp"
	"strings"
)

// The endpoint allow-list (S30 §3.2), compiled in. The request builder refuses every other
// method and path before a byte leaves, with internal_error and an alert. Nothing here can send,
// trash, delete or modify a message: drafts/send, messages/send and messages/{id}/modify are not
// on it, and TestSendUnreachable checks it.
var allowList = []struct {
	method string
	re     *regexp.Regexp
}{
	{"POST", regexp.MustCompile(`^/gmail/v1/users/[^/]+/drafts$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/[^/]+/drafts$`)},
	{"PUT", regexp.MustCompile(`^/gmail/v1/users/[^/]+/drafts/[A-Za-z0-9_-]{1,128}$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/[^/]+/drafts/[A-Za-z0-9_-]{1,128}$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/[^/]+/threads/[A-Za-z0-9_-]{1,128}$`)},
	{"GET", regexp.MustCompile(`^/gmail/v1/users/[^/]+/profile$`)},
}

// Allowed reports whether method and the decoded path may be requested. The mailbox segment may
// not be one of the reserved words, so "drafts" can't stand in for a mailbox.
func Allowed(method, path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) >= 5 {
		switch parts[4] {
		case "drafts", "messages", "threads", "profile", "send", "settings":
			return false
		}
	}
	for _, p := range parts {
		if p == "send" || p == "modify" || p == "trash" || p == "batchDelete" || p == "import" || p == "insert" {
			return false
		}
	}
	for _, a := range allowList {
		if a.method == method && a.re.MatchString(path) {
			return true
		}
	}
	return false
}

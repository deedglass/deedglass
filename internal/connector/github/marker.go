package github

import (
	"strings"
)

// The marker (S29 §3.2): bodies of create_issue, comment and create_pr end with
// "\n\n<!-- deedglass:act_… -->" when issue_marker is true. It is visible in the raw body.
const (
	markerOpen  = "<!-- deedglass:"
	markerClose = " -->"
)

// Marker is the HTML comment that carries an action id.
func Marker(actionID string) string { return markerOpen + actionID + markerClose }

// withMarker appends the marker to body.
func withMarker(body, actionID string) string {
	if body == "" {
		return Marker(actionID)
	}
	return body + "\n\n" + Marker(actionID)
}

// stripMarker removes a trailing marker: the body as the agent sent it, the action id it carries,
// and whether one was there. Trailing whitespace after the marker is tolerated.
func stripMarker(body string) (string, string, bool) {
	t := strings.TrimRight(body, " \t\r\n")
	if !strings.HasSuffix(t, markerClose) {
		return body, "", false
	}
	i := strings.LastIndex(t, markerOpen)
	if i < 0 {
		return body, "", false
	}
	id := t[i+len(markerOpen) : len(t)-len(markerClose)]
	if id == "" || strings.ContainsAny(id, " \t\r\n<>") {
		return body, "", false
	}
	rest := t[:i]
	switch {
	case strings.HasSuffix(rest, "\r\n\r\n"):
		rest = strings.TrimSuffix(rest, "\r\n\r\n")
	case strings.HasSuffix(rest, "\n\n"):
		rest = strings.TrimSuffix(rest, "\n\n")
	}
	return rest, id, true
}

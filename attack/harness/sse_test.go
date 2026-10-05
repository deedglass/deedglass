package harness

import (
	"testing"
	"time"

	slackc "github.com/deedglass/deedglass/internal/connector/slack"
)

// The S34 helpers' smoke tests: they run in make ci (no build tag), unlike the cases.

func TestParseSSE(t *testing.T) {
	b := []byte("retry: 3000\n\nid: a.1\nevent: hello\ndata: {\"x\":1}\n\n: comment\nid: a.2\nevent: action.created\ndata: line1\ndata: line2\n\nid: a.3\ndata: tail")
	ev := ParseSSE(b)
	if len(ev) != 3 || ev[0].ID != "a.1" || ev[0].Event != "hello" || ev[1].Data != "line1\nline2" || ev[2].ID != "a.3" || ev[2].Data != "tail" {
		t.Fatalf("%+v", ev)
	}
}

func TestSlackSignMatchesConnector(t *testing.T) {
	ts := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC).Unix()
	body := SlackForm(SlackClick("T0A", "A0A", "U0A", "deedglass_approve", "apr_x|sha256:y", "http://127.0.0.1:1/respond"))
	if got, want := SlackSign("0123456789abcdef0123456789abcdef", ts, body), slackc.Sign([]byte("0123456789abcdef0123456789abcdef"), "1791676800", body); got != want {
		t.Fatalf("%s != %s", got, want)
	}
}

func TestHasControl(t *testing.T) {
	for s, want := range map[string]bool{"plain\ttext\n": false, "esc \x1b[31m": true, "bell \a": true, "del \x7f": true, "é ✓": false} {
		if HasControl([]byte(s)) != want {
			t.Fatalf("%q", s)
		}
	}
}

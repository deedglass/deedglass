package toolname

import (
	"regexp"
	"testing"
)

// reAPI is the stricter of Anthropic's rule (^[a-zA-Z0-9_-]{1,128}$) and OpenAI's (the same
// characters, at most 64).
var reAPI = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func TestRoundTrip(t *testing.T) {
	for _, q := range []string{"postgres.query", "postgres.execute", "deedglass.check_approval", "fake.echo",
		"orders.update_status", "my-db.list_rows", "fhir.read"} {
		s := SafeName(q)
		if !reAPI.MatchString(s) {
			t.Errorf("SafeName(%q) = %q, not API-safe", q, s)
		}
		if got := Canonical(s); got != q {
			t.Errorf("Canonical(SafeName(%q)) = %q", q, got)
		}
		if got := Canonical(q); got != q {
			t.Errorf("Canonical(%q) = %q, want unchanged", q, got)
		}
		if Wire(q, Dotted) != q || Wire(q, Safe) != s {
			t.Errorf("Wire(%q) wrong", q)
		}
		if !IsDotted(q) || IsDotted(s) {
			t.Errorf("IsDotted(%q/%q) wrong", q, s)
		}
	}
}

func TestUnchanged(t *testing.T) {
	for _, n := range []string{"", "echo", "_x", "Postgres_query", "a_b", "1x_y", "x_", "scratch.files.read", "noconn"} {
		if got := Canonical(n); got != n {
			t.Errorf("Canonical(%q) = %q, want unchanged", n, got)
		}
	}
	if got := SafeName("echo"); got != "echo" {
		t.Errorf("SafeName(echo) = %q", got)
	}
	// An upstream tool name with its own dots keeps them after the connector.
	if got := SafeName("scratch.files.read"); got != "scratch_files.read" {
		t.Errorf("SafeName = %q", got)
	}
}

func TestParseStyle(t *testing.T) {
	for in, want := range map[string]Style{"": Safe, "safe": Safe, "dotted": Dotted} {
		if s, ok := ParseStyle(in); !ok || s != want {
			t.Errorf("ParseStyle(%q) = %q, %v", in, s, ok)
		}
	}
	if _, ok := ParseStyle("dots"); ok {
		t.Error("ParseStyle(dots) ok")
	}
}

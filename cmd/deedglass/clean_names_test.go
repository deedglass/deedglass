package main

import (
	"strings"
	"testing"
)

// TestClipCleans (S34, R07, SR-014): names that come from agents are escaped before a terminal
// sees them in the Ledger's tables: ESC, BEL, CR and the bidi overrides.
func TestClipCleans(t *testing.T) {
	for _, in := range []string{"fake.echo\x1b[31m", "fake.echo\u202eetirw", "agent\r\x07", "a\nb"} {
		out := clip(in, 40)
		if strings.ContainsAny(out, "\x1b\x07\r\n\u202e") {
			t.Fatalf("%q → %q", in, out)
		}
	}
	if clip("fake.echo", 40) != "fake.echo" {
		t.Fatal("a plain name changed")
	}
}

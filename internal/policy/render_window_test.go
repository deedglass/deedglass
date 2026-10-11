package policy

import "testing"

// TestWindowShortContextLine: a context line shorter than the window offset never panics.
func TestWindowShortContextLine(t *testing.T) {
	if got := window([]rune("short"), 400); got != "…" {
		t.Fatalf("%q", got)
	}
	if got := window([]rune("abc"), 0); got != "abc" {
		t.Fatalf("%q", got)
	}
}

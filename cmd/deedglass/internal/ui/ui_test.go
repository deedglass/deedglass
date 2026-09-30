package ui

import (
	"os"
	"testing"
)

func TestColorAllowed(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name  string
		tty   bool
		env   map[string]string
		flag  bool
		color bool
	}{
		{"tty", true, map[string]string{"TERM": "xterm-256color"}, false, true},
		{"not a tty", false, map[string]string{"TERM": "xterm"}, false, false},
		{"NO_COLOR", true, map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, false, false},
		{"TERM=dumb", true, map[string]string{"TERM": "dumb"}, false, false},
		{"--no-color", true, map[string]string{"TERM": "xterm"}, true, false},
	}
	for _, c := range cases {
		if got := ColorAllowed(c.tty, env(c.env), c.flag); got != c.color {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestStyle(t *testing.T) {
	on, off := Style{Color: true}, Style{}
	if off.Red("x") != "x" || off.OKLine("done") != "✓ done" || on.Dim("") != "" {
		t.Fatal("plain")
	}
	if on.Teal("x") != "\x1b[36mx\x1b[0m" || on.Red("x") != "\x1b[31mx\x1b[0m" || on.Violet("x") != "\x1b[35mx\x1b[0m" ||
		on.Amber("x") != "\x1b[33mx\x1b[0m" || on.Dim("x") != "\x1b[2mx\x1b[0m" || on.Bold("x") != "\x1b[1mx\x1b[0m" {
		t.Fatal("colour codes")
	}
	if on.FailLine("no") != "\x1b[31m✕\x1b[0m no" || on.UnknownLine("?") != "\x1b[33m○\x1b[0m ?" {
		t.Fatal("lines")
	}
	if Pad("✓", 3) != "✓  " || Pad("abcd", 2) != "abcd" || Width("—·") != 2 {
		t.Fatal("pad")
	}
	if IsTerminal(nil) {
		t.Fatal("nil file")
	}
	if n, err := os.Open(os.DevNull); err == nil {
		defer n.Close()
		if IsTerminal(n) {
			t.Fatal("the null device is not a terminal")
		}
	}
	f, _ := os.CreateTemp(t.TempDir(), "x")
	defer f.Close()
	if IsTerminal(f) {
		t.Fatal("a regular file is not a terminal")
	}
}

package demo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTapes (S37 §3.4): every demo tape sources the brand preset, the preset holds the brand's
// theme only, and a vertical tape types no visible command longer than 42 columns.
func TestTapes(t *testing.T) {
	files, _ := filepath.Glob("tapes/*.tape")
	if len(files) < 6 {
		t.Fatalf("%d tapes", len(files))
	}
	brand, _ := os.ReadFile("tapes/_brand.tape")
	for _, want := range []string{`Set FontFamily "Geist Mono"`, "Set FontSize 20", "Set Width 1920", "Set Height 1080", "Set Padding 48",
		`Set MarginFill "#07080C"`, "Set BorderRadius 20", "Set TypingSpeed 45ms", "Set CursorBlink false", "Set Framerate 30",
		`"background": "#0E1017"`, `"green": "#34E3C9"`, `"red": "#FF7A70"`, `"magenta": "#A99CFF"`, `"yellow": "#F7B955"`, `"blue": "#6F8DFF"`} {
		if !strings.Contains(string(brand), want) {
			t.Fatalf("_brand.tape misses %s", want)
		}
	}
	typed := regexp.MustCompile(`^Type "(.*)"$`)
	for _, f := range files {
		name := filepath.Base(f)
		if name == "_brand.tape" || name == "_setup.tape" {
			continue
		}
		b, _ := os.ReadFile(f)
		s := string(b)
		if !strings.Contains(s, "Source examples/demo/tapes/_brand.tape") && !strings.Contains(s, "Source examples/demo/tapes/_brand-vertical.tape") {
			t.Fatalf("%s doesn't source the brand preset", name)
		}
		if strings.Contains(s, "Set Theme") && name != "_brand.tape" {
			t.Fatalf("%s sets its own theme", name)
		}
		if !strings.Contains(s, "_brand-vertical.tape") || name == "_brand-vertical.tape" {
			continue
		}
		hidden := false
		for _, l := range strings.Split(s, "\n") {
			switch strings.TrimSpace(l) {
			case "Hide":
				hidden = true
			case "Show":
				hidden = false
			}
			if m := typed.FindStringSubmatch(strings.TrimSpace(l)); m != nil && !hidden && len([]rune(m[1])) > 42 {
				t.Errorf("%s: %q is %d columns; vertical tapes type at most 42", name, m[1], len([]rune(m[1])))
			}
		}
	}
}

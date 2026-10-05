//go:build demo

package demo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// normalizeLaunch masks what changes from run to run: ids, hashes, dates and durations. Deed
// numbers stay: make demo starts from a fresh stack, so every run seals the same deeds in order.
func normalizeLaunch(s string) string {
	for _, r := range []struct{ re, with string }{
		{`(act|deed|apr|agt|key|org|usr)_[0-9a-z]{26}`, "${1}_…"},
		{`sha256:[0-9a-f]{4,64}(…[0-9a-f]{4})?`, "sha256:…"},
		{`\d{4}-\d{2}-\d{2}( → \d{4}-\d{2}-\d{2})?`, "<date>"},
		{`\d+ m \d{2} s|\b\d+ s\b`, "<duration>"},
		{`\x1b\[[0-9;]*m`, ""},
	} {
		s = regexp.MustCompile(r.re).ReplaceAllString(s, r.with)
	}
	return s
}

// TestLaunchDemo is S37's gate for the launch demo (make demo-launch-check): make demo twice, each
// from a fresh stack, with identical normalized output that equals testdata/launch.golden.
// UPDATE=1 rewrites the golden (review the diff: the demo's story must not change by accident).
func TestLaunchDemo(t *testing.T) {
	if os.Getenv("DEEDGLASS_DEMO") != "1" {
		t.Skip("needs Docker and the demo stack's ports: make demo-launch-check")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var runs []string
	for i := 0; i < 2; i++ {
		cmd := exec.Command("make", "-s", "demo")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make demo run %d: %v\n%s", i+1, err, out)
		}
		t.Logf("run %d:\n%s", i+1, out)
		runs = append(runs, normalizeLaunch(string(out)))
	}
	if runs[0] != runs[1] {
		t.Fatalf("the two runs differ:\n--- run 1\n%s\n--- run 2\n%s", runs[0], runs[1])
	}
	golden := filepath.Join("testdata", "launch.golden")
	if os.Getenv("UPDATE") == "1" {
		if err := os.WriteFile(golden, []byte(runs[0]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != runs[0] {
		t.Fatalf("make demo doesn't match %s (UPDATE=1 rewrites it after a reviewed change):\n%s", golden, runs[0])
	}
	// The changed copy differs from the export by exactly one byte; the export is untouched.
	a, _ := os.ReadFile(filepath.Join(root, ".tmp/demo/deeds.jsonl"))
	b, _ := os.ReadFile(filepath.Join(root, ".tmp/demo/deeds-changed.jsonl"))
	if len(a) == 0 || len(a) != len(b) {
		t.Fatal("the export and its changed copy are missing or differ in length")
	}
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d bytes differ, want 1", n)
	}
	for _, want := range []string{"7 scenes", "✕ L2 · MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.",
		"○ skipped · needs Stripe test mode · run make demo-live", "chain intact · 1 seal key", "was changed after it was sealed.",
		"Demo complete · 6 scenes · 1 skipped"} {
		if !strings.Contains(runs[0], want) {
			t.Fatalf("missing %q", want)
		}
	}
}

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

// TestHeroScript is S21's hero, twice (make demo-hero-check): examples/demo/scripts/hero.sh runs
// against the running demo stack, then make demo-reset, then hero.sh again. Each run must print
// L2 · VERIFIED, then L2 · MISMATCH with Postgres's 301 rows, the diff, and an intact chain. The
// clip on the landing page is this script's output: the video is the test.
func TestHeroScript(t *testing.T) {
	if os.Getenv("DEEDGLASS_DEMO") != "1" {
		t.Skip("needs the demo stack: make demo-up, then make demo-hero-check")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "NO_COLOR=1", "HERO_PACE=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	reMismatch := regexp.MustCompile(`L2 · MISMATCH · Postgres changed 301 rows in public\.orders\. The agent reported 1\.`)
	for i := 0; i < 2; i++ {
		out := run("examples/demo/scripts/hero.sh")
		t.Logf("hero.sh run %d:\n%s", i+1, out)
		for _, want := range []string{
			"L2 · VERIFIED · Postgres committed 1 row in public.orders, as the statement said.",
			"✓ Trigger on · orders_ship_open_batch",
			"expected   UPDATE 1 row in public.orders where id = 42 · sets status",
			"observed   UPDATE 301 rows in public.orders · 300 outside id = 42",
			"first 20 of 301 rows · the rest in Console › Actions",
			"Witnessed by Postgres · commit ",
			"chain intact",
			"✓ Hero done · VERIFIED act_",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("run %d: missing %q", i+1, want)
			}
		}
		if !regexp.MustCompile(`✓ expected\s+42\s+paid → shipped`).MatchString(out) {
			t.Fatalf("run %d: order 42's row isn't first and expected", i+1)
		}
		if !reMismatch.MatchString(out) {
			t.Fatalf("run %d: no MISMATCH line with 301 rows", i+1)
		}
		if i == 0 {
			run("make", "-s", "demo-reset")
		}
	}
}

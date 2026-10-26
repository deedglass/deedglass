package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/cli/ui"
	"github.com/deedglass/deedglass/internal/protect"
)

// protectHome is a temp HOME with every agent's folder and a settings file the person already has.
func protectHome(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	home := t.TempDir()
	before := map[string][]byte{}
	for _, a := range protect.Agents {
		if err := os.MkdirAll(filepath.Join(home, a.Dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for file, body := range map[string]string{
		".claude/settings.json": "{\n  \"permissions\": { \"allow\": [\"Bash(npm test)\"] }\n}\n",
		".gemini/settings.json": "{\n  \"theme\": \"GitHub\"\n}\n",
	} {
		p := filepath.Join(home, filepath.FromSlash(file))
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		before[p] = []byte(body)
	}
	return home, before
}

func snapshot(t *testing.T, home string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".deedglass" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		} else {
			out[p] = "<dir>"
		}
		return nil
	})
	return out
}

// TestProtectEndToEnd: --dry-run writes nothing, --yes adds the guard to all five agents, a second
// run changes nothing, and --undo leaves HOME byte-identical to before.
func TestProtectEndToEnd(t *testing.T) {
	home, _ := protectHome(t)
	env := map[string]string{"HOME": home}
	start := snapshot(t, home)
	hc := "--hook-command=/opt/dg/deedglass"

	r := cli(t, env, false, "protect", "--dry-run", hc)
	if r.code != 0 || !strings.Contains(r.stdout, "5 of 5 agents found · dry run") || !strings.Contains(r.stdout, "Dry run: nothing was written. 5 files would change.") {
		t.Fatalf("dry run: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, l := range strings.Split(r.stdout, "\n") {
		if ui.Width(l) > 80 {
			t.Errorf("line over 80 columns: %q", l)
		}
	}
	if got := snapshot(t, home); !equalMaps(got, start) {
		t.Fatal("--dry-run wrote something")
	}

	// No --yes and no "y": nothing is written.
	r = cliIn(t, []byte("n\n"), env, false, "protect", hc)
	if r.code != 0 || !strings.Contains(r.stderr, "[y/N]") || !strings.Contains(r.stderr, "Nothing was written.") {
		t.Fatalf("declined: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got := snapshot(t, home); !equalMaps(got, start) {
		t.Fatal("a declined run wrote something")
	}

	r = cli(t, env, false, "protect", "--yes", "--json", hc)
	var out struct {
		Applied bool             `json:"applied"`
		Changes int              `json:"changes"`
		Backup  string           `json:"backup"`
		Command string           `json:"command"`
		Badge   string           `json:"badge"`
		Agents  []protect.Change `json:"agents"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &out) != nil || !out.Applied || out.Changes != 5 || out.Backup == "" || out.Command != "/opt/dg/deedglass guard shell" || out.Badge != protectBadge {
		t.Fatalf("--yes --json: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, a := range protect.Agents {
		b, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(a.File)))
		if err != nil || !strings.Contains(string(b), "/opt/dg/deedglass guard shell") {
			t.Errorf("%s: no guard in %s (%v)", a.Client, a.File, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); !strings.Contains(string(b), "Bash(npm test)") {
		t.Error("Claude Code's own settings were lost")
	}
	protected := snapshot(t, home)

	r = cli(t, env, false, "protect", "--yes", hc)
	if r.code != 0 || !strings.Contains(r.stdout, "Nothing to change.") || strings.Count(r.stdout, "already has the guard") != 5 {
		t.Fatalf("second run: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got := snapshot(t, home); !equalMaps(got, protected) {
		t.Fatal("a second run changed a file")
	}

	r = cli(t, env, false, "protect", "--undo", "--dry-run")
	if r.code != 0 || !strings.Contains(r.stdout, "Dry run: 5 files would be put back.") {
		t.Fatalf("undo dry run: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, env, false, "protect", "--undo")
	if r.code != 0 || !strings.Contains(r.stdout, "Put back 5 files.") {
		t.Fatalf("undo: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if got := snapshot(t, home); !equalMaps(got, start) {
		t.Errorf("after --undo HOME differs:\n got %v\nwant %v", got, start)
	}
	r = cli(t, env, false, "protect", "--undo")
	if r.code != 0 || !strings.Contains(r.stdout, "Nothing to undo") {
		t.Fatalf("second undo: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// TestProtectEachClient: --client picks one agent; each one's file round-trips on its own.
func TestProtectEachClient(t *testing.T) {
	for _, a := range protect.Agents {
		t.Run(a.Client, func(t *testing.T) {
			home, _ := protectHome(t)
			env := map[string]string{"HOME": home}
			start := snapshot(t, home)
			r := cli(t, env, false, "protect", "--yes", "--client", a.Client, "--hook-command", "deedglass")
			if r.code != 0 || !strings.Contains(r.stdout, "Added the guard to 1 file.") {
				t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stdout, "  badge  "+protectBadge+"\n  share  ") || strings.Contains(r.stdout, "\x1b[") {
				t.Errorf("badge lines missing, or colour off a terminal:\n%s", r.stdout)
			}
			if got := snapshot(t, home); equalMaps(got, start) {
				t.Fatal("nothing changed")
			}
			if r := cli(t, env, false, "protect", "--undo"); r.code != 0 {
				t.Fatalf("undo: %d\n%s%s", r.code, r.stdout, r.stderr)
			}
			if got := snapshot(t, home); !equalMaps(got, start) {
				t.Errorf("after undo HOME differs")
			}
		})
	}
}

// TestProtectBadgeNoColor: on a terminal with NO_COLOR, the badge lines carry no escape codes; a
// run that changes nothing offers no badge.
func TestProtectBadgeNoColor(t *testing.T) {
	home, _ := protectHome(t)
	env := map[string]string{"HOME": home, "NO_COLOR": "1"}
	r := cli(t, env, true, "protect", "--yes", "--hook-command", "deedglass")
	if r.code != 0 || !strings.Contains(r.stdout, protectBadge) || strings.Contains(r.stdout, "\x1b[") {
		t.Fatalf("%d\n%q", r.code, r.stdout)
	}
	r = cli(t, env, true, "protect", "--yes", "--hook-command", "deedglass")
	if strings.Contains(r.stdout, "badge") {
		t.Errorf("a run with nothing to change offered the badge:\n%s", r.stdout)
	}
	r = cli(t, map[string]string{"HOME": home}, false, "protect", "--dry-run", "--json", "--hook-command", "deedglass")
	var out struct{ Badge *string }
	if json.Unmarshal([]byte(r.stdout), &out) != nil || out.Badge == nil || *out.Badge != "" {
		t.Errorf("--json without a write: badge %v\n%s", out.Badge, r.stdout)
	}
}

// TestProtectNoAgents: an empty HOME says so and writes nothing.
func TestProtectNoAgents(t *testing.T) {
	home := t.TempDir()
	r := cli(t, map[string]string{"HOME": home}, false, "protect", "--yes", "--hook-command", "deedglass")
	if r.code != 0 || !strings.Contains(r.stdout, "No coding agent found") || !strings.Contains(r.stdout, "0 of 5 agents found") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Errorf("wrote %v", ents)
	}
}

// TestProtectUsage: unknown clients and arguments are usage errors.
func TestProtectUsage(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{{"protect", "--client", "vscode"}, {"protect", "now"}} {
		if r := cli(t, map[string]string{"HOME": home}, false, args...); r.code != 2 {
			t.Errorf("%v: exit %d\n%s", args, r.code, r.stderr)
		}
	}
}

// TestProtectSkipsComments: a settings file with comments is left alone and the run says why.
func TestProtectSkipsComments(t *testing.T) {
	home, _ := protectHome(t)
	p := filepath.Join(home, ".cursor", "hooks.json")
	body := []byte("{\n  // mine\n  \"version\": 1\n}\n")
	_ = os.WriteFile(p, body, 0o644)
	r := cli(t, map[string]string{"HOME": home}, false, "protect", "--yes", "--hook-command", "deedglass")
	if r.code != 0 || !strings.Contains(r.stdout, "left alone: it isn't plain JSON") {
		t.Fatalf("%d\n%s", r.code, r.stdout)
	}
	if b, _ := os.ReadFile(p); !bytes.Equal(b, body) {
		t.Error("the file with comments changed")
	}
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

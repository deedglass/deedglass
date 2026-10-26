package protect

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/*.after.json")

const cmd = "/usr/local/bin/deedglass"

// fixtures: each agent's config as a person might already have it. "" means no file (the folder
// exists, the agent is installed). Expected outcomes are the goldens in testdata/<client>.after.json.
var fixtures = map[string]string{
	"claude-code": `{
    "permissions": { "allow": ["Bash(npm test)"], "defaultMode": "acceptEdits" },
    "env": { "FOO": "1" },
    "hooks": {
        "PreToolUse": [
            { "matcher": "Edit", "hooks": [{ "type": "command", "command": "./lint.sh" }] }
        ],
        "Stop": [ { "hooks": [{ "type": "command", "command": "say done" }] } ]
    }
}
`,
	"cursor": `{
  "version": 1,
  "hooks": {
    "afterFileEdit": [{ "command": "./format.sh" }]
  }
}
`,
	"codex":   "",
	"gemini":  "{\n\t\"theme\": \"GitHub\",\n\t\"hooks\": {\n\t\t\"BeforeTool\": [\n\t\t\t{ \"matcher\": \"^write_file$\", \"hooks\": [{ \"type\": \"command\", \"command\": \"./check.sh\", \"timeout\": 5000 }] }\n\t\t]\n\t}\n}\n",
	"copilot": "",
}

func setup(t *testing.T, client string) (Options, Agent) {
	t.Helper()
	home := t.TempDir()
	var a Agent
	for _, x := range Agents {
		if x.Client == client {
			a = x
		}
	}
	if err := os.MkdirAll(filepath.Join(home, a.Dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if body := fixtures[client]; body != "" {
		if err := os.WriteFile(filepath.Join(home, a.File), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Options{Home: home, Command: cmd, Now: func() time.Time { return time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC) }}, a
}

func only(t *testing.T, cs []Change, client string) Change {
	t.Helper()
	var got *Change
	for i := range cs {
		if cs[i].Client == client {
			got = &cs[i]
		} else if cs[i].Status != Absent {
			t.Errorf("%s: %s, want absent (its folder doesn't exist)", cs[i].Client, cs[i].Status)
		}
	}
	if got == nil {
		t.Fatalf("no change for %s", client)
	}
	return *got
}

// TestEachAgent: for every agent's config format, against a temp HOME: plan adds the hook, apply
// writes exactly the golden (the person's keys and hooks kept, in order, in their indent), a second
// run changes nothing, and undo puts the file back byte for byte (or removes what protect created).
func TestEachAgent(t *testing.T) {
	for _, a := range Agents {
		t.Run(a.Client, func(t *testing.T) {
			o, _ := setup(t, a.Client)
			file := filepath.Join(o.Home, a.File)
			before, beforeErr := os.ReadFile(file)
			cs, err := Plan(o)
			if err != nil {
				t.Fatal(err)
			}
			c := only(t, cs, a.Client)
			if c.Status != Add || c.File != file {
				t.Fatalf("plan: %s %s (%s), want add %s", c.Status, c.File, c.Reason, file)
			}
			if b, _ := os.ReadFile(file); !bytes.Equal(b, before) {
				t.Fatal("plan wrote the file")
			}
			run, err := Apply(o, cs)
			if err != nil || run == "" {
				t.Fatalf("apply: %q %v", run, err)
			}
			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", a.Client+".after.json")
			if *update {
				_ = os.MkdirAll("testdata", 0o755)
				if err := os.WriteFile(golden, after, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, want) {
				t.Errorf("after apply:\n%s\nwant (%s):\n%s", after, golden, want)
			}
			if !strings.Contains(string(after), cmd+" guard shell") {
				t.Errorf("no hook command in\n%s", after)
			}
			if st, _ := os.Stat(file); beforeErr == nil && st.Mode().Perm() != 0o600 {
				t.Errorf("mode %v, want the file's own 0600", st.Mode().Perm())
			}

			// Idempotent: a second run finds the guard and writes nothing, not even a backup.
			cs2, _ := Plan(o)
			if c := only(t, cs2, a.Client); c.Status != Present {
				t.Errorf("second plan: %s (%s), want present", c.Status, c.Reason)
			}
			if run2, err := Apply(o, cs2); run2 != "" || err != nil {
				t.Errorf("second apply: %q %v", run2, err)
			}
			if b, _ := os.ReadFile(file); !bytes.Equal(b, after) {
				t.Error("second run changed the file")
			}

			// Undo: back byte for byte, or gone with the folders protect made.
			dry, err := Undo(o, true)
			if err != nil || len(dry) != 1 || dry[0].Status != Restored {
				t.Fatalf("undo --dry-run: %+v %v", dry, err)
			}
			if b, _ := os.ReadFile(file); !bytes.Equal(b, after) {
				t.Error("a dry undo changed the file")
			}
			us, err := Undo(o, false)
			if err != nil || len(us) != 1 || us[0].Status != Restored {
				t.Fatalf("undo: %+v %v", us, err)
			}
			b, err := os.ReadFile(file)
			if beforeErr != nil {
				if err == nil {
					t.Errorf("undo left the file protect created:\n%s", b)
				}
				if _, err := os.Stat(filepath.Dir(file)); a.Client == "copilot" && err == nil {
					t.Error("undo left the hooks folder protect created")
				}
			} else if !bytes.Equal(b, before) {
				t.Errorf("after undo:\n%s\nwant:\n%s", b, before)
			}
			if again, _ := Undo(o, false); len(again) != 0 {
				t.Errorf("a second undo did something: %+v", again)
			}
		})
	}
}

// TestAllAgentsRoundTrip: every agent at once, then undo: every file as it was.
func TestAllAgentsRoundTrip(t *testing.T) {
	home := t.TempDir()
	o := Options{Home: home, Command: "npx -y deedglass@0.12.0"}
	before := map[string][]byte{}
	for _, a := range Agents {
		_ = os.MkdirAll(filepath.Join(home, a.Dir), 0o755)
		if body := fixtures[a.Client]; body != "" {
			_ = os.WriteFile(filepath.Join(home, a.File), []byte(body), 0o644)
			before[a.File] = []byte(body)
		}
	}
	cs, _ := Plan(o)
	n := 0
	for _, c := range cs {
		if c.Status != Add {
			t.Errorf("%s: %s", c.Client, c.Status)
		}
		n++
	}
	if n != 5 {
		t.Fatalf("%d changes, want 5", n)
	}
	if _, err := Apply(o, cs); err != nil {
		t.Fatal(err)
	}
	us, err := Undo(o, false)
	if err != nil || len(us) != 5 {
		t.Fatalf("%+v %v", us, err)
	}
	for _, a := range Agents {
		b, err := os.ReadFile(filepath.Join(home, a.File))
		if want, ok := before[a.File]; ok {
			if !bytes.Equal(b, want) {
				t.Errorf("%s: not restored", a.Client)
			}
		} else if err == nil {
			t.Errorf("%s: protect's file left behind", a.Client)
		}
	}
}

// TestKeepsExistingGuard: a guard the person already set up (any path, the plugin's or their own)
// counts; protect adds nothing.
func TestKeepsExistingGuard(t *testing.T) {
	o, a := setup(t, "claude-code")
	body := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"deedglass guard shell --deny"}]}]}}`
	_ = os.WriteFile(filepath.Join(o.Home, a.File), []byte(body), 0o644)
	cs, _ := Plan(o)
	if c := only(t, cs, "claude-code"); c.Status != Present {
		t.Errorf("%s, want present", c.Status)
	}
}

// TestSkipsWhatItCantRead: comments, a list at the top, a hooks value of the wrong type: left alone.
func TestSkipsWhatItCantRead(t *testing.T) {
	for name, body := range map[string]string{
		"comments":  "{\n  // my settings\n  \"theme\": \"x\"\n}\n",
		"array":     "[]",
		"hooks":     `{"hooks": "none"}`,
		"event":     `{"hooks": {"PreToolUse": {"matcher": "Bash"}}}`,
		"trailing":  `{} {}`,
		"truncated": `{"hooks": {`,
	} {
		o, a := setup(t, "claude-code")
		file := filepath.Join(o.Home, a.File)
		_ = os.WriteFile(file, []byte(body), 0o644)
		cs, _ := Plan(o)
		c := only(t, cs, "claude-code")
		if c.Status != Skip || c.Reason == "" {
			t.Errorf("%s: %s %q, want skip with a reason", name, c.Status, c.Reason)
		}
		if run, _ := Apply(o, cs); run != "" {
			t.Errorf("%s: applied", name)
		}
		if b, _ := os.ReadFile(file); string(b) != body {
			t.Errorf("%s: file changed", name)
		}
	}
}

// TestEmptyFile: an empty settings file is an empty object.
func TestEmptyFile(t *testing.T) {
	o, a := setup(t, "cursor")
	file := filepath.Join(o.Home, a.File)
	_ = os.WriteFile(file, nil, 0o644)
	cs, _ := Plan(o)
	if c := only(t, cs, "cursor"); c.Status != Add {
		t.Fatalf("%s %s", c.Status, c.Reason)
	}
	_, _ = Apply(o, cs)
	_, _ = Undo(o, false)
	if b, err := os.ReadFile(file); err != nil || len(b) != 0 {
		t.Errorf("%q %v, want the empty file back", b, err)
	}
}

// TestUndoAfterEdit: when the person changed the file after protect wrote it, undo keeps their
// change and takes out only the guard.
func TestUndoAfterEdit(t *testing.T) {
	o, a := setup(t, "claude-code")
	file := filepath.Join(o.Home, a.File)
	cs, _ := Plan(o)
	if _, err := Apply(o, cs); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	edited := strings.Replace(string(b), `"FOO": "1"`, `"FOO": "1", "BAR": "2"`, 1)
	_ = os.WriteFile(file, []byte(edited), 0o600)
	us, err := Undo(o, false)
	if err != nil || len(us) != 1 || us[0].Status != Removed {
		t.Fatalf("%+v %v", us, err)
	}
	got, _ := os.ReadFile(file)
	s := string(got)
	if strings.Contains(s, "guard shell") || !strings.Contains(s, `"BAR": "2"`) || !strings.Contains(s, "./lint.sh") || !strings.Contains(s, "say done") {
		t.Errorf("after undo:\n%s", s)
	}
}

// TestUndoNewEventRemoved: a file with no hooks gets them, and loses them again on a surgical undo.
func TestUndoNewEventRemoved(t *testing.T) {
	o, a := setup(t, "gemini")
	file := filepath.Join(o.Home, a.File)
	_ = os.WriteFile(file, []byte("{\n  \"theme\": \"x\"\n}\n"), 0o644)
	cs, _ := Plan(o)
	_, _ = Apply(o, cs)
	b, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(b), `"x"`, `"y"`, 1)), 0o644)
	us, _ := Undo(o, false)
	if len(us) != 1 || us[0].Status != Removed {
		t.Fatalf("%+v", us)
	}
	if got, _ := os.ReadFile(file); string(got) != "{\n  \"theme\": \"y\"\n}\n" {
		t.Errorf("%q", got)
	}
}

// TestProjectScope: --project adds the project's folders (Claude Code's personal settings.local.json);
// a project without an agent's folder gets no row.
func TestProjectScope(t *testing.T) {
	o, _ := setup(t, "claude-code")
	o.Dir, o.Project = t.TempDir(), true
	_ = os.MkdirAll(filepath.Join(o.Dir, ".claude"), 0o755)
	cs, _ := Plan(o)
	var proj []Change
	for _, c := range cs {
		if c.Scope == "project" {
			proj = append(proj, c)
		}
	}
	if len(proj) != 1 || proj[0].File != filepath.Join(o.Dir, ".claude", "settings.local.json") || proj[0].Status != Add {
		t.Fatalf("%+v", proj)
	}
}

// TestClientsFilter: --client limits the plan to those agents.
func TestClientsFilter(t *testing.T) {
	o, _ := setup(t, "cursor")
	o.Clients = []string{"cursor"}
	cs, _ := Plan(o)
	if len(cs) != 1 || cs[0].Client != "cursor" {
		t.Fatalf("%+v", cs)
	}
}

// TestSymlinkKept: a settings file that is a symlink (a dotfiles checkout) stays a symlink.
func TestSymlinkKept(t *testing.T) {
	o, a := setup(t, "cursor")
	file := filepath.Join(o.Home, a.File)
	real := filepath.Join(t.TempDir(), "hooks.json")
	b, _ := os.ReadFile(file)
	_ = os.WriteFile(real, b, 0o644)
	_ = os.Remove(file)
	if err := os.Symlink(real, file); err != nil {
		t.Skip(err)
	}
	cs, _ := Plan(o)
	_, _ = Apply(o, cs)
	if st, _ := os.Lstat(file); st.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced")
	}
	if got, _ := os.ReadFile(real); !strings.Contains(string(got), "guard shell") {
		t.Error("the link's target didn't get the hook")
	}
	_, _ = Undo(o, false)
	if got, _ := os.ReadFile(real); !bytes.Equal(got, b) {
		t.Error("undo didn't restore the target")
	}
}

// TestHookCommand: the hook keeps working after this process ends (F1).
func TestHookCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	_ = os.MkdirAll(bin, 0o755)
	exe := filepath.Join(dir, "Cellar", "deedglass", "0.12.0", "bin", "deedglass")
	_ = os.MkdirAll(filepath.Dir(exe), 0o755)
	_ = os.WriteFile(exe, []byte("x"), 0o755)
	link := filepath.Join(bin, "deedglass")
	_ = os.Symlink(exe, link)
	other := filepath.Join(dir, "other-deedglass")
	_ = os.WriteFile(other, []byte("y"), 0o755)
	cases := []struct{ name, exe, onPath, version, want string }{
		{"on PATH, same binary: the stable link", exe, link, "0.12.0", link},
		{"on PATH, another binary: this one", exe, other, "0.12.0", exe},
		{"not on PATH", exe, "", "0.12.0", exe},
		{"npx run", "/Users/a/.npm/_npx/1f2e/node_modules/@deedglass/cli-darwin-arm64/deedglass", "", "0.12.0", "npx -y deedglass@0.12.0"},
		{"npx run, dev build", "/Users/a/.npm/_npx/1f2e/node_modules/@deedglass/cli-darwin-arm64/deedglass", "", "0.12.0-dev", "deedglass"},
		{"go run", "/tmp/go-build123/b001/exe/deedglass", "", "", "deedglass"},
		{"spaces and quotes", "/Users/O'Brien/My Tools/deedglass", "", "0.12.0", `'/Users/O'\''Brien/My Tools/deedglass'`},
	}
	for _, c := range cases {
		if got := HookCommand(c.exe, c.onPath, c.version); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// TestIdempotentWithAnyBinaryName: a hook command whose binary isn't called deedglass is still
// recognised on the next run, and undo still finds it.
func TestIdempotentWithAnyBinaryName(t *testing.T) {
	o, a := setup(t, "claude-code")
	o.Command = "/opt/tools/dg"
	cs, _ := Plan(o)
	if _, err := Apply(o, cs); err != nil {
		t.Fatal(err)
	}
	cs, _ = Plan(o)
	if c := only(t, cs, "claude-code"); c.Status != Present {
		t.Fatalf("%s, want present", c.Status)
	}
	file := filepath.Join(o.Home, a.File)
	b, _ := os.ReadFile(file)
	_ = os.WriteFile(file, []byte(strings.Replace(string(b), `"FOO": "1"`, `"FOO": "2"`, 1)), 0o600)
	if us, _ := Undo(o, false); len(us) != 1 || us[0].Status != Removed {
		t.Fatalf("%+v", us)
	}
	if got, _ := os.ReadFile(file); strings.Contains(string(got), "guard shell") {
		t.Errorf("guard left:\n%s", got)
	}
}

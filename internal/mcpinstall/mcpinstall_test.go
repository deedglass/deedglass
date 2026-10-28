package mcpinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func opts(t *testing.T) Options {
	t.Helper()
	return Options{Home: t.TempDir(), GOOS: "linux", Command: "/usr/local/bin/deedglass", Args: []string{"mcp", "serve", "--client", "cursor"},
		Now: func() time.Time { return time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC) }}
}

func fileOf(t *testing.T, o Options, id string) string {
	t.Helper()
	tg, _ := Lookup(id)
	return tg.file(o.layout())
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAbsentWhenNotInstalled(t *testing.T) {
	o := opts(t)
	for _, tg := range Targets {
		c, err := Plan(o, tg.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != Absent || c.Installed || c.CanUndo || c.File == "" || !strings.Contains(c.Entry, `"deedglass"`) {
			t.Errorf("%s: %+v", tg.ID, c)
		}
		if r, err := Apply(o, c); err != nil || r.Status != Absent {
			t.Errorf("%s: apply wrote something: %+v %v", tg.ID, r, err)
		}
	}
}

// Every target: add to a file with other keys (kept in order and indent), present on a second run,
// then undo restores the file byte for byte.
func TestAddPresentUndo(t *testing.T) {
	fixtures := map[string]string{
		ClaudeDesktop: "{\n    \"preferences\": { \"theme\": \"dark\" },\n    \"mcpServers\": {\n        \"filesystem\": { \"command\": \"npx\", \"args\": [\"-y\", \"fs\"] }\n    },\n    \"zzz\": 1\n}\n",
		Cursor:        "",
		VSCode:        "{\n\t\"inputs\": [],\n\t\"servers\": {}\n}\n",
		Gemini:        "{\n  \"theme\": \"GitHub\",\n  \"hooks\": { \"BeforeTool\": [] }\n}\n",
	}
	for id, before := range fixtures {
		t.Run(id, func(t *testing.T) {
			o := opts(t)
			tg, _ := Lookup(id)
			if err := os.MkdirAll(tg.dir(o.layout()), 0o755); err != nil {
				t.Fatal(err)
			}
			file := fileOf(t, o, id)
			if before != "" {
				write(t, file, before)
			}
			c, err := Plan(o, id)
			if err != nil {
				t.Fatal(err)
			}
			if c.Status != Add || !c.Installed || c.CanUndo {
				t.Fatalf("plan: %+v", c)
			}
			r, err := Apply(o, c)
			if err != nil {
				t.Fatal(err)
			}
			after := read(t, file)
			var parsed map[string]any
			if err := json.Unmarshal([]byte(after), &parsed); err != nil {
				t.Fatalf("not JSON after: %v\n%s", err, after)
			}
			servers, _ := parsed[tg.Key].(map[string]any)
			entry, _ := servers[Name].(map[string]any)
			if entry == nil || entry["command"] != o.Command {
				t.Fatalf("entry missing: %s", after)
			}
			if tg.typed && entry["type"] != "stdio" || !tg.typed && entry["type"] != nil {
				t.Fatalf("type: %s", after)
			}
			// Other keys kept, in order, with the file's own indent.
			if before != "" {
				for _, k := range []string{"preferences", "filesystem", "zzz", "inputs", "theme", "hooks"} {
					if strings.Contains(before, `"`+k+`"`) && !strings.Contains(after, `"`+k+`"`) {
						t.Errorf("%s lost: %s", k, after)
					}
				}
				bi, ai := strings.Index(before, "\n")+1, strings.Index(after, "\n")+1
				if before[bi:bi+1] != after[ai:ai+1] {
					t.Errorf("indent changed:\n%s", after)
				}
				if strings.Index(after, `"preferences"`) > strings.Index(after, `"mcpServers"`) && id == ClaudeDesktop {
					t.Errorf("order changed:\n%s", after)
				}
				if r.Backup == "" || read(t, r.Backup) != before || !strings.HasSuffix(r.Backup, ".bak") || filepath.Dir(r.Backup) != filepath.Dir(file) {
					t.Errorf("backup: %q", r.Backup)
				}
			} else if r.Backup != "" {
				t.Errorf("a new file has no backup, got %q", r.Backup)
			}
			// Idempotent.
			c2, _ := Plan(o, id)
			if c2.Status != Present || !c2.CanUndo || c2.Backup != r.Backup {
				t.Fatalf("second plan: %+v", c2)
			}
			if _, err := Apply(o, c2); err != nil {
				t.Fatal(err)
			}
			if read(t, file) != after {
				t.Fatal("a second apply changed the file")
			}
			// Undo.
			u, err := Undo(o, id)
			if err != nil {
				t.Fatal(err)
			}
			if u.Status != Restored {
				t.Fatalf("undo: %+v", u)
			}
			if before == "" {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatalf("the created file should be gone: %v", err)
				}
			} else if read(t, file) != before {
				t.Fatalf("not restored:\n%s", read(t, file))
			}
			c3, _ := Plan(o, id)
			if c3.Status != Add || c3.CanUndo {
				t.Fatalf("after undo: %+v", c3)
			}
			if u, _ := Undo(o, id); u.Status != Left {
				t.Fatalf("second undo: %+v", u)
			}
		})
	}
}

func TestUpdateReplacesOnlyOurEntry(t *testing.T) {
	o := opts(t)
	file := fileOf(t, o, Cursor)
	write(t, file, `{"mcpServers":{"other":{"command":"x"},"deedglass":{"command":"/old/deedglass","args":["mcp","serve"]}}}`)
	c, err := Plan(o, Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != Update {
		t.Fatalf("%+v", c)
	}
	if _, err := Apply(o, c); err != nil {
		t.Fatal(err)
	}
	after := read(t, file)
	if !strings.Contains(after, `"other"`) || strings.Contains(after, "/old/deedglass") || !strings.Contains(after, o.Command) {
		t.Fatalf("%s", after)
	}
}

// The person edits the file after Connect wrote it: Undo removes only the Deedglass entry.
func TestUndoAfterTheirEdit(t *testing.T) {
	o := opts(t)
	file := fileOf(t, o, Gemini)
	write(t, file, "{\n  \"theme\": \"GitHub\"\n}\n")
	c, _ := Plan(o, Gemini)
	if _, err := Apply(o, c); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(read(t, file), `"theme": "GitHub"`, `"theme": "Light", "added": true`, 1)
	write(t, file, edited)
	u, err := Undo(o, Gemini)
	if err != nil {
		t.Fatal(err)
	}
	after := read(t, file)
	if u.Status != Removed || strings.Contains(after, Name) || !strings.Contains(after, `"added": true`) || strings.Contains(after, `"mcpServers"`) {
		t.Fatalf("%+v\n%s", u, after)
	}
}

func TestSkipsWhatItCantRead(t *testing.T) {
	o := opts(t)
	cases := map[string]string{"comment": "{ // hi\n}", "array": "[]", "servers-not-object": `{"mcpServers": 3}`}
	for name, body := range cases {
		file := fileOf(t, o, Cursor)
		write(t, file, body)
		c, err := Plan(o, Cursor)
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != Skip || c.Reason == "" {
			t.Errorf("%s: %+v", name, c)
		}
		if _, err := Apply(o, c); err != nil || read(t, file) != body {
			t.Errorf("%s: apply changed a skipped file", name)
		}
	}
}

func TestNoKeyInEntryAndPaths(t *testing.T) {
	o := Options{Home: "/home/x", GOOS: "darwin", Command: "npx", Args: []string{"-y", "deedglass", "mcp", "serve", "--client", "claude-desktop"}}
	c, err := Plan(o, ClaudeDesktop)
	if err != nil {
		t.Fatal(err)
	}
	if c.File != "/home/x/Library/Application Support/Claude/claude_desktop_config.json" {
		t.Fatalf("darwin path: %s", c.File)
	}
	if !strings.Contains(c.Entry, `"npx"`) || strings.Contains(c.Entry, "key") {
		t.Fatalf("entry: %s", c.Entry)
	}
	w := Options{Home: `C:\Users\x`, GOOS: "windows", AppData: `C:\Users\x\AppData\Roaming`, Command: "deedglass"}
	if c, _ := Plan(w, VSCode); !strings.HasSuffix(c.File, filepath.Join("Roaming", "Code", "User", "mcp.json")) {
		t.Fatalf("windows path: %s", c.File)
	}
	if _, err := Plan(o, "nope"); err == nil {
		t.Fatal("an unknown tool must fail")
	}
	if _, err := Plan(Options{Home: "/h"}, Cursor); err == nil {
		t.Fatal("no command must fail")
	}
}

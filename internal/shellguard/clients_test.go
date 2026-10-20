package shellguard

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var updateClients = flag.Bool("update-clients", false, "rewrite testdata/clients/*.config and *.out.json")

// golden compares got with testdata/clients/name, or rewrites it under -update-clients.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "clients", name)
	if *updateClients {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/shellguard -run TestClient -update-clients writes it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s drifted:\n got %s\nwant %s\n(review, then go test ./internal/shellguard -run TestClient -update-clients)", path, got, want)
	}
}

func supported() []ClientHook {
	var out []ClientHook
	for _, c := range Clients {
		if c.Unsupported == "" {
			out = append(out, c)
		}
	}
	return out
}

// TestClientTable: every row is complete and consistent, and the five clients TY-08 names are each
// either supported or have their limitation written down.
func TestClientTable(t *testing.T) {
	if Clients[0].Name != "claude-code" {
		t.Fatal("claude-code must be the first row: it is the default --client")
	}
	seen := map[string]bool{}
	for _, c := range Clients {
		if seen[c.Name] {
			t.Errorf("%s: duplicate row", c.Name)
		}
		seen[c.Name] = true
		if c.Title == "" || len(c.Docs) == 0 || !regexp.MustCompile(`^2026-\d\d-\d\d$`).MatchString(c.Checked) {
			t.Errorf("%s: needs a title, the docs it was checked against and a checked date", c.Name)
		}
		for _, d := range c.Docs {
			if !strings.HasPrefix(d, "https://") {
				t.Errorf("%s: docs %q", c.Name, d)
			}
		}
		if c.Unsupported != "" {
			if c.Config != "" || c.Event != "" {
				t.Errorf("%s: an unsupported client has no snippet and no event", c.Name)
			}
			if !strings.Contains(c.Unsupported, "deedglass mcp") {
				t.Errorf("%s: an unsupported client must point to the MCP path", c.Name)
			}
			continue
		}
		if c.Event == "" || c.File == "" || c.Config == "" {
			t.Errorf("%s: needs an event, a file and a config", c.Name)
		}
		if !c.Ask && len(c.Limits) == 0 {
			t.Errorf("%s: a client that can't ask must say so in Limits", c.Name)
		}
		if (c.Format == FormatCursor) != (c.Shell == "") {
			t.Errorf("%s: only Cursor's shell-only event has no tool name", c.Name)
		}
	}
	for _, n := range []string{"claude-code", "codex", "gemini", "copilot", "cursor", "vscode"} {
		if !seen[n] {
			t.Errorf("no row for %s", n)
		}
	}
	if _, ok := LookupClient("emacs"); ok {
		t.Error("LookupClient found a client that isn't in the table")
	}
}

// TestClientConfigGoldens: each snippet is pinned byte for byte, parses as JSON, registers the
// client's event, and runs this command with its own --client.
func TestClientConfigGoldens(t *testing.T) {
	for _, c := range supported() {
		t.Run(c.Name, func(t *testing.T) {
			golden(t, c.Name+".config.json", []byte(c.Config))
			var v struct {
				Hooks map[string]json.RawMessage `json:"hooks"`
			}
			if err := json.Unmarshal([]byte(c.Config), &v); err != nil {
				t.Fatalf("config isn't JSON: %v", err)
			}
			if len(v.Hooks) != 1 || v.Hooks[c.Event] == nil {
				t.Errorf("config registers %v, want only %s", v.Hooks, c.Event)
			}
			want := "deedglass guard shell --client " + c.Name
			if c.Name == "claude-code" {
				want = `"deedglass guard shell"`
			}
			if !strings.Contains(c.Config, want) {
				t.Errorf("config doesn't run %s", want)
			}
			if strings.Contains(c.Config, "allow") {
				t.Error("config mentions allow")
			}
		})
	}
}

// TestClientIncident80868: TY-08's done line. The #80868 compound command, in each client's own
// input shape (testdata/clients/<name>.in.json), gets that client's ask, or its deny where the
// client can't ask (written down in Limits). Outputs pinned in <name>.out.json.
func TestClientIncident80868(t *testing.T) {
	for _, c := range supported() {
		t.Run(c.Name, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join("testdata", "clients", c.Name+".in.json"))
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			fs, err := c.Hook(bytes.NewReader(in), &out, false, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !has(fs, "prisma-shadow-database-url") {
				t.Fatalf("got %v, want prisma-shadow-database-url", ids(fs))
			}
			golden(t, c.Name+".out.json", out.Bytes())
			want := `"ask"`
			if !c.Ask {
				want = `"deny"`
			}
			if !strings.Contains(out.String(), want) || strings.Contains(out.String(), `"allow"`) {
				t.Errorf("answer %s, want %s and never allow", out.String(), want)
			}
		})
	}
}

// inputFor builds a minimal input in c's shape for cmd.
func inputFor(c ClientHook, cmd string) []byte {
	var v any
	switch c.Format {
	case FormatCopilot:
		a, _ := json.Marshal(map[string]string{"command": cmd})
		v = map[string]any{"cwd": "/tmp", "toolName": c.Shell, "toolArgs": string(a)}
	case FormatCursor:
		v = map[string]any{"hook_event_name": c.Event, "command": cmd, "cwd": "/tmp"}
	default:
		v = map[string]any{"hook_event_name": c.Event, "tool_name": c.Shell, "cwd": "/tmp", "tool_input": map[string]string{"command": cmd}}
	}
	b, _ := json.Marshal(v)
	return b
}

// noDecision is what each client reads as "the hook said nothing".
func noDecision(c ClientHook) string {
	if c.Format == FormatGemini {
		return "{}\n"
	}
	return ""
}

// TestClientIncidentsAndOrdinary: every incident gets a decision in every client, every ordinary
// command gets none, and no answer is ever allow.
func TestClientIncidentsAndOrdinary(t *testing.T) {
	inc, ord := readEntries(t, "incidents.txt"), readEntries(t, "ordinary.txt")
	for _, c := range supported() {
		for _, e := range inc {
			var out bytes.Buffer
			if _, err := c.Hook(bytes.NewReader(inputFor(c, e.cmd)), &out, false, Options{}); err != nil {
				t.Fatal(err)
			}
			if out.String() == noDecision(c) || strings.Contains(out.String(), "allow") {
				t.Errorf("%s: incidents.txt:%d: answer %q", c.Name, e.line, out.String())
			}
		}
		for _, e := range ord {
			var out bytes.Buffer
			if _, err := c.Hook(bytes.NewReader(inputFor(c, e.cmd)), &out, false, Options{}); err != nil {
				t.Fatal(err)
			}
			if out.String() != noDecision(c) {
				t.Errorf("%s: ordinary.txt:%d: answer %q", c.Name, e.line, out.String())
			}
		}
	}
}

// TestClientIgnores: other tools, other events and other clients' shapes get no decision.
func TestClientIgnores(t *testing.T) {
	cases := map[string][]string{
		"claude-code": {`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"command":"dropdb app"}}`, `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"dropdb app"}}`},
		"codex":       {`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"dropdb app"}}`, `{"hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_input":{"command":"dropdb app"}}`},
		"gemini":      {`{"hook_event_name":"AfterTool","tool_name":"run_shell_command","tool_input":{"command":"dropdb app"}}`, `{"hook_event_name":"BeforeTool","tool_name":"write_file","tool_input":{"command":"dropdb app"}}`},
		"copilot":     {`{"toolName":"edit","toolArgs":"{\"command\":\"dropdb app\"}"}`, `{"toolName":"bash","toolArgs":"not json"}`, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"dropdb app"}}`},
		"cursor":      {`{"hook_event_name":"beforeMCPExecution","command":"dropdb app"}`, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"dropdb app"}}`},
	}
	for name, ins := range cases {
		c, _ := LookupClient(name)
		for _, in := range ins {
			var out bytes.Buffer
			fs, err := c.Hook(strings.NewReader(in), &out, false, Options{})
			if err != nil || len(fs) != 0 || out.String() != noDecision(c) {
				t.Errorf("%s: %s: got %v %q %v", name, in, ids(fs), out.String(), err)
			}
		}
	}
}

// TestCopilotToolArgsObject: an object toolArgs is read like GitHub's JSON-string example.
func TestCopilotToolArgsObject(t *testing.T) {
	c, _ := LookupClient("copilot")
	var out bytes.Buffer
	fs, err := c.Hook(strings.NewReader(`{"toolName":"bash","toolArgs":{"command":"dropdb app"}}`), &out, false, Options{})
	if err != nil || len(fs) == 0 || !strings.Contains(out.String(), `"permissionDecision":"ask"`) {
		t.Errorf("got %v %q %v", ids(fs), out.String(), err)
	}
}

// TestClientDeny: --deny gives deny in every client.
func TestClientDeny(t *testing.T) {
	for _, c := range supported() {
		var out bytes.Buffer
		if _, err := c.Hook(bytes.NewReader(inputFor(c, "dropdb app")), &out, true, Options{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"deny"`) || !strings.Contains(out.String(), "ask the user to run it themselves") {
			t.Errorf("%s: %s", c.Name, out.String())
		}
	}
}

// TestClientBadInput: unreadable input is an error for every client (the CLI decides the exit).
func TestClientBadInput(t *testing.T) {
	for _, c := range supported() {
		var out bytes.Buffer
		if _, err := c.Hook(strings.NewReader("not json"), &out, false, Options{}); err != ErrHookInput || out.Len() != 0 {
			t.Errorf("%s: %v %q", c.Name, err, out.String())
		}
	}
}

// TestClientDocs: docs/INSTALL_CLIENTS.md shows every snippet exactly as the table prints it, and
// names every client's limits, so the page can't drift from --print-config.
func TestClientDocs(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "INSTALL_CLIENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, c := range Clients {
		if !strings.Contains(doc, "### "+strings.Split(c.Title, " (")[0]) {
			t.Errorf("docs/INSTALL_CLIENTS.md: no section for %s", c.Title)
		}
		if c.Config != "" && !strings.Contains(doc, "```json\n"+c.Config+"```") {
			t.Errorf("docs/INSTALL_CLIENTS.md: %s's snippet differs from the table (deedglass guard shell --print-config --client %s)", c.Name, c.Name)
		}
	}
}

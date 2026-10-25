package front

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/toolname"
)

// reAPITool is the stricter of Anthropic's tool-name rule ({1,128}) and OpenAI's (at most 64).
var reAPITool = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func listNames(t *testing.T, f *Front, r Request) []string {
	t.Helper()
	var ts []struct{ Name string }
	_ = json.Unmarshal(result(t, f.Handle(context.Background(), r))["tools"], &ts)
	out := make([]string, len(ts))
	for i, x := range ts {
		out[i] = x.Name
	}
	return out
}

// TestToolNamesSafe: tools/list shows API-safe names by default, dotted on request, and
// tools/call takes either spelling to the same qualified tool and digest.
func TestToolNamesSafe(t *testing.T) {
	ns := &NativeSource{Connectors: []connector.Connector{fake.New()}}
	s := &scripted{submit: func(gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{"ok":true}`), nil }}
	f := &Front{Tools: ns, Gate: s}

	safe := listNames(t, f, req("tools/list", "", true))
	if len(safe) < 2 || safe[0] != "fake_echo" || safe[len(safe)-1] != "deedglass_check_approval" {
		t.Fatalf("safe listing %v", safe)
	}
	for _, n := range safe {
		if !reAPITool.MatchString(n) {
			t.Fatalf("%q isn't accepted by the model APIs", n)
		}
	}

	f.ToolNames = toolname.Dotted
	dotted := listNames(t, f, req("tools/list", "", false))
	if dotted[0] != "fake.echo" || dotted[len(dotted)-1] != "deedglass.check_approval" {
		t.Fatalf("dotted listing %v", dotted)
	}
	// Deedglass-Tool-Names on the request wins over the front's setting.
	r := req("tools/list", "", true)
	r.ToolNames = toolname.Safe
	if got := listNames(t, f, r); got[0] != "fake_echo" {
		t.Fatalf("request override %v", got)
	}
	f.ToolNames = ""

	var digests []string
	for _, name := range []string{"fake_echo", "fake.echo"} {
		resp := f.Handle(context.Background(), req("tools/call", `"name":"`+name+`","arguments":{"text":"hi"}`, true))
		if resp.Env["error"] != nil {
			t.Fatalf("%s: %s", name, resp.Env.Marshal())
		}
		if s.last.Tool != "fake.echo" {
			t.Fatalf("%s reached the Gate as %q", name, s.last.Tool)
		}
		digests = append(digests, s.last.ToolDigest)
	}
	if digests[0] == "" || digests[0] != digests[1] {
		t.Fatalf("digests differ by spelling: %v", digests)
	}
	// The digest is the qualified definition's, whatever the listing shows.
	all, _ := ns.All(context.Background(), req("", "", false).Principal)
	if all[0].Digest != digests[0] {
		t.Fatalf("digest %s, listing %s", digests[0], all[0].Digest)
	}
	for _, name := range []string{"fake_nope", "deedglass_other", "deedglass.other"} {
		resp := f.Handle(context.Background(), req("tools/call", `"name":"`+name+`","arguments":{}`, false))
		if resp.Env["error"] == nil {
			t.Fatalf("%s: %s", name, resp.Env.Marshal())
		}
	}
}

func TestWireNames(t *testing.T) {
	ts := []Tool{{Name: "a.b"}, {Name: "a_b"}, {Name: "pg.query"}, {Name: "echo"}, {Name: "files.read"}}
	got := WireNames(ts, toolname.Safe)
	want := []string{"a.b", "a_b", "pg_query", "echo", "files_read"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WireNames = %v, want %v", got, want)
		}
	}
	if d := WireNames(ts, toolname.Dotted); d[2] != "pg.query" {
		t.Fatalf("dotted %v", d)
	}
}

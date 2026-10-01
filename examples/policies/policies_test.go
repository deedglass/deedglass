package policies_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/examples/policies"
	"github.com/deedglass/deedglass/internal/policy"
)

var update = flag.Bool("update", false, "rewrite hashes.json")

// The same check as `make policy-test` (deedglass policy test --dir examples/policies
// --require-coverage --voice), so `go test ./...` covers the starters too: every policy compiles
// with zero warnings under the voice check, has a cases file, passes every case, and every rule
// decides at least one case.
func TestStarters(t *testing.T) {
	names := policies.Starters()
	want := []string{"default-deny", "github-read-mostly", "gmail-drafts-only", "postgres-safe", "slack-post-with-approval", "stripe-refunds"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("starters %v, want %v", names, want)
	}
	total := 0
	hashes := map[string]string{}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			src, err := policies.Source(name)
			if err != nil {
				t.Fatal(err)
			}
			c, diags := policy.Compile(src, policy.Options{File: name + ".yaml", Voice: true})
			if c == nil || len(diags) > 0 {
				t.Fatalf("%s: %+v", name, diags)
			}
			if c.Name != name {
				t.Fatalf("the file %s.yaml names the policy %q", name, c.Name)
			}
			hashes[name] = c.VersionHash
			raw, err := policies.Cases(name)
			if err != nil {
				t.Fatalf("%s has no cases file: %v", name, err)
			}
			cf, probs := policy.ParseCasesYAML(raw, name+".cases.yaml")
			if cf == nil {
				t.Fatalf("%+v", probs)
			}
			r := policy.RunCases(context.Background(), c, cf, policy.TestOptions{})
			for _, cr := range r.Cases {
				if !cr.Pass {
					t.Errorf("%s · %s (line %d): %v", name, cr.Name, cr.Line, cr.Problems)
				}
			}
			if len(r.Coverage.Uncovered) > 0 {
				t.Errorf("%s: rules no case decides: %v", name, r.Coverage.Uncovered)
			}
			if r.Coverage.Total == 0 && r.Coverage.Default == 0 {
				t.Errorf("%s: no case reaches the default", name)
			}
			for _, b := range c.Budgets {
				found := false
				for _, bc := range r.Coverage.Budgets {
					found = found || (bc.ID == b.ID && bc.Cases > 0)
				}
				if !found {
					t.Errorf("%s: no case trips budget %s", name, b.ID)
				}
			}
			total += r.Passed + r.Failed
			// The header comment says how to test it.
			if !bytes.Contains(src, []byte("deedglass policy test examples/policies/"+name+".yaml")) {
				t.Errorf("%s: the header comment must give the test command", name)
			}
		})
	}
	if total < 84 {
		t.Fatalf("%d cases across the starters; S12 asks for at least 84", total)
	}
	// Pinned hashes: any change to a starter is a visible diff in hashes.json.
	b, _ := json.MarshalIndent(hashes, "", "  ")
	b = append(b, '\n')
	if *update {
		if err := os.WriteFile("hashes.json", b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(b, policies.Hashes()) {
		t.Fatalf("a starter's version_hash changed; review the change, then run go test ./examples/policies -update\nwant:\n%s\ngot:\n%s", policies.Hashes(), b)
	}
}

// AlwaysDenies (S11) agrees with what the starters say about a few tools.
func TestStartersAlwaysDeny(t *testing.T) {
	agent := policy.AgentFacts{ID: policy.LocalAgentID, Name: "support-bot", Status: policy.StatusActive, Labels: map[string]string{}}
	tool := func(name, side string) policy.ToolFacts {
		ct, _, _ := strings.Cut(name, ".")
		return policy.ToolFacts{Name: name, Connector: ct, ConnectorType: ct, SideEffect: side}
	}
	for _, tc := range []struct {
		policy string
		tool   policy.ToolFacts
		want   bool
	}{
		{"gmail-drafts-only", tool("gmail.send_email", "unknown"), true},
		{"gmail-drafts-only", tool("gmail.create_draft", "unknown"), false},
		{"postgres-safe", tool("postgres.query", "unknown"), false},
		{"default-deny", tool("postgres.query", "unknown"), true},
		{"default-deny", tool("stripe.create_refund", "write"), true},
		{"github-read-mostly", tool("github.merge_pull_request", "unknown"), true},
		{"stripe-refunds", tool("stripe.delete_customer", "destructive"), true},
		{"stripe-refunds", tool("stripe.create_refund", "write"), false},
		{"slack-post-with-approval", tool("slack.post_message", "write"), false},
	} {
		src, err := policies.Source(tc.policy)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := policy.Compile(src, policy.Options{})
		if got := policy.AlwaysDenies(c, tc.tool, agent); got != tc.want {
			t.Errorf("%s · %s: AlwaysDenies %v, want %v", tc.policy, tc.tool.Name, got, tc.want)
		}
		if why, _, _ := policy.AlwaysDeniesWhy(c, tc.tool, agent); why != tc.want {
			t.Errorf("%s · %s: AlwaysDeniesWhy disagrees", tc.policy, tc.tool.Name)
		}
	}
}

// TestDemoVisibility is S14 §6's visibility table on examples/demo/policies/acme-demo.yaml, as
// POST /v1/tools/resolve sees wrapped tools (connector type mcp, side effect unknown).
func TestDemoVisibility(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "demo", "policies", "acme-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c, diags := policy.Compile(src, policy.Options{})
	if c == nil {
		t.Fatal(diags)
	}
	agent := policy.AgentFacts{ID: policy.LocalAgentID, Name: "claude-desktop", Status: policy.StatusActive, Labels: map[string]string{}}
	for _, tc := range []struct {
		tool   string
		hidden bool
		rule   string
	}{
		{"scratch.read_text_file", false, "allow-scratch-reads"},
		{"scratch.list_directory", false, "allow-scratch-reads"},
		{"scratch.write_file", false, "scratch-writes-only-in-allowed"},
		{"scratch.edit_file", false, "scratch-writes-only-in-allowed"},
		{"scratch.move_file", true, ""},
		{"scratch.create_directory", true, ""},
		{"scratch.list_allowed_directories", false, "allow-scratch-reads"},
		{"fake.echo", false, "allow-fake"},
	} {
		ft := policy.ToolFacts{Name: tc.tool, Connector: "scratch", ConnectorType: "mcp", SideEffect: "unknown"}
		denied, rule, reason := policy.AlwaysDeniesWhy(c, ft, agent)
		if denied != tc.hidden || rule != tc.rule || denied != policy.AlwaysDenies(c, ft, agent) || (denied && reason != policy.ReasonDefaultDeny) {
			t.Errorf("%s: (%v, %q, %q)", tc.tool, denied, rule, reason)
		}
	}
}

// The embedded files are exactly the files on disk.
func TestEmbedMatchesDisk(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var disk []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".yaml") {
			disk = append(disk, e.Name())
		}
	}
	sort.Strings(disk)
	for _, n := range disk {
		a, _ := os.ReadFile(filepath.Join(".", n))
		b, err := policies.FS.(interface{ ReadFile(string) ([]byte, error) }).ReadFile(n)
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs from the embedded copy", n)
		}
	}
}

package archtest

import (
	"slices"
	"strings"
	"testing"
)

// TY-04 (DL-109, ops/strategy/TEN_YEAR.md §4 rule 1): the core knows no protocol. The Gate, the
// Witness and deeds (every package under internal/gate, internal/witness and internal/deed, test
// files aside) depend, directly or through anything they import, on none of:
//   - internal/mcp and internal/mcpb (an ingress protocol);
//   - internal/connector/<vendor>: every package under internal/connector except the shared,
//     vendor-free leaves in coreConnectorLeaves (the interfaces in internal/connector itself);
//   - a coding-agent hook package: internal/shellguard (the Claude Code PreToolUse contract), and
//     any package with a path element hook, hooks or agenthook* (TY-08's future adapters).
//
// An adapter may import the core (internal/connector/postgres imports internal/witness); never the
// other way round. TestCoreIsolationCatchesPlanted runs the same rule over a fixture module with
// planted imports, so a rule that matches nothing can't pass vacuously.

var corePackages = []string{"internal/gate", "internal/witness", "internal/deed"}

// coreConnectorLeaves are the internal/connector packages the core may reach: the JSON Schema
// subset the Witness validates output with, and the one egress door (no vendor in either).
var coreConnectorLeaves = []string{"internal/connector/schema", "internal/connector/egress"}

// under reports whether path is rel or a package below it, in this module.
func under(path, rel string) bool {
	p := module + "/" + rel
	return path == p || strings.HasPrefix(path, p+"/")
}

// forbiddenForCore says why the core may not depend on path, or "" when it may.
func forbiddenForCore(path string) string {
	switch {
	case under(path, "internal/mcp"), under(path, "internal/mcpb"):
		return "an MCP package"
	case under(path, "internal/shellguard"):
		return "a coding-agent hook package"
	case strings.HasPrefix(path, module+"/internal/connector/"):
		for _, leaf := range coreConnectorLeaves {
			if under(path, leaf) {
				return ""
			}
		}
		return "a connector (vendor) package"
	}
	if !strings.HasPrefix(path, module+"/") {
		return ""
	}
	for _, el := range strings.Split(strings.TrimPrefix(path, module+"/"), "/") {
		if el == "hook" || el == "hooks" || strings.HasPrefix(el, "agenthook") {
			return "a coding-agent hook package"
		}
	}
	return ""
}

// coreViolations lists every core package's forbidden dependency, sorted.
func coreViolations(all map[string]pkg) []string {
	var out []string
	for path, p := range all {
		core := false
		for _, c := range corePackages {
			core = core || under(path, c)
		}
		if !core {
			continue
		}
		for _, d := range p.Deps {
			if why := forbiddenForCore(d); why != "" {
				out = append(out, strings.TrimPrefix(path, module+"/")+" → "+strings.TrimPrefix(d, module+"/")+" ("+why+")")
			}
		}
	}
	slices.Sort(out)
	return out
}

func TestCoreIsolation(t *testing.T) {
	all := listDeps(t)
	for _, c := range corePackages {
		if _, ok := all[module+"/"+c]; !ok {
			t.Fatalf("%s not found; is the core list current?", c)
		}
	}
	for _, v := range coreViolations(all) {
		t.Errorf("%s: the core never depends on a protocol or vendor adapter (TY-04); put it behind an interface the adapter implements", v)
	}
}

// TestCoreIsolationCatchesPlanted runs go list over testdata/planted, a module with this module's
// path and stub packages, where each core package carries one planted import: internal/witness
// imports internal/connector/postgres, internal/gate imports internal/mcp/front, internal/deed
// reaches internal/connector/stripe through a subpackage, internal/deed/archive imports
// internal/shellguard and internal/gate/lifecycle imports internal/integrations/codex/hooks. The
// allowed leaves (internal/connector, its schema package) and an adapter importing the core
// (internal/connector/postgres → internal/witness) are not reported.
func TestCoreIsolationCatchesPlanted(t *testing.T) {
	got := coreViolations(listDepsIn(t, "testdata/planted"))
	want := []string{
		"internal/deed → internal/connector/stripe (a connector (vendor) package)",
		"internal/deed/archive → internal/shellguard (a coding-agent hook package)",
		"internal/deed/sub → internal/connector/stripe (a connector (vendor) package)",
		"internal/gate → internal/mcp/front (an MCP package)",
		"internal/gate/lifecycle → internal/integrations/codex/hooks (a coding-agent hook package)",
		"internal/witness → internal/connector/postgres (a connector (vendor) package)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("planted violations\n got: %q\nwant: %q", got, want)
	}
}

func TestForbiddenForCore(t *testing.T) {
	for path, want := range map[string]bool{
		module + "/internal/mcp":                  true,
		module + "/internal/mcp/front":            true,
		module + "/internal/mcpb":                 true,
		module + "/internal/mcpbridge":            false, // a prefix of the name isn't the package
		module + "/internal/shellguard":           true,
		module + "/internal/connector":            false,
		module + "/internal/connector/schema":     false,
		module + "/internal/connector/egress":     false,
		module + "/internal/connector/postgres":   true,
		module + "/internal/connector/fhir/pgmap": true,
		module + "/internal/connector/saas":       true,
		module + "/internal/agenthooks/codex":     true,
		module + "/internal/hooks":                true,
		module + "/internal/notify/webhook":       false, // a webhook isn't a coding-agent hook
		module + "/internal/sqlguard":             false,
		"github.com/someone/hooks":                false,
	} {
		if got := forbiddenForCore(path) != ""; got != want {
			t.Errorf("forbiddenForCore(%s) = %v, want %v", path, got, want)
		}
	}
}

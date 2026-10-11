package celexport

import (
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/policy"
)

func TestMatchCEL(t *testing.T) {
	src := `deedglass: policy/v1
name: export-test
defaults: { decision: deny, reason: No. }
rules:
  - id: rule-a
    match: { tool: [postgres.query, "stripe.*"], connector_type: postgres, side_effect: write, agent: [claude-code, "cur*"], labels: { env: prod } }
    decision: allow
  - id: rule-b
    decision: deny
    reason: Always.
  - id: rule-c
    match: { tool: postgres.execute }
    when: sql.kind == "update"
    decision: require_approval
    reason: Ask a "person".
`
	c, diags := policy.Compile([]byte(src), policy.Options{})
	for _, d := range diags {
		if d.Severity == policy.SevError {
			t.Fatalf("compile: %s", d.Message)
		}
	}
	p := Export(c)
	if len(p.Rules) != 3 || p.Default.Decision != "deny" || p.Object != "play_policy" {
		t.Fatalf("export = %+v", p)
	}
	want := `(glob(tool.name, "postgres.query") || glob(tool.name, "stripe.*")) && glob(tool.connector_type, "postgres") && tool.side_effect in ["write"] && (glob(agent.name, "claude-code") || glob(agent.name, "cur*")) && ("env" in agent.labels && agent.labels["env"] == "prod")`
	if p.Rules[0].MatchCEL != want {
		t.Errorf("match_cel =\n%s\nwant\n%s", p.Rules[0].MatchCEL, want)
	}
	if p.Rules[0].WhenCEL != "true" || p.Rules[1].MatchCEL != "true" {
		t.Errorf("empty when/match = %q / %q", p.Rules[0].WhenCEL, p.Rules[1].MatchCEL)
	}
	if p.Rules[2].WhenCEL != `sql.kind == "update"` || !strings.Contains(p.Rules[2].Reason, `"person"`) {
		t.Errorf("rule c = %+v", p.Rules[2])
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{`a`: `"a"`, `a"b`: `"a\"b"`, `a\b`: `"a\\b"`, "a\nb": `"a\nb"`} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

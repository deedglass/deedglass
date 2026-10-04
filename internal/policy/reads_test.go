package policy

import "testing"

func TestReadsAnomaly(t *testing.T) {
	head := "deedglass: policy/v1\nname: anomaly-test\ndefaults: { decision: deny, reason: No rule allows this action. }\n"
	cases := []struct {
		name, src string
		want      bool
	}{
		{"none", head + "rules:\n  - id: rule-a\n    decision: allow\n", false},
		{"when reads it", head + "rules:\n  - id: rule-a\n    when: anomaly.active && anomaly.severity in [\"warn\", \"high\"]\n    decision: require_approval\n    reason: A person approves writes while this agent is unusual.\n", true},
		{"kinds", head + "rules:\n  - id: rule-a\n    when: '\"volume_spike\" in anomaly.kinds'\n    decision: deny\n    reason: Spiking.\n", true},
		{"string literal only", head + "rules:\n  - id: rule-a\n    when: tool.name == \"anomaly\"\n    decision: allow\n", false},
		{"measure", head + "budgets:\n  - id: budget-a\n    window: day\n    max_spend: \"10.00 usd\"\n    measure: 'anomaly.active ? 2 : 1'\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, diags := Compile([]byte(c.src), Options{})
			if p == nil {
				t.Fatalf("compile: %v", diags)
			}
			if got := ReadsAnomaly(p); got != c.want {
				t.Fatalf("ReadsAnomaly = %v, want %v", got, c.want)
			}
		})
	}
	if ReadsAnomaly(nil) {
		t.Fatal("nil policy reads nothing")
	}
}

func TestAnomalyFactsInCEL(t *testing.T) {
	src := "deedglass: policy/v1\nname: anomaly-test\ndefaults: { decision: allow }\nrules:\n  - id: approve-while-anomalous\n    match: { side_effect: [write, destructive] }\n    when: anomaly.active && anomaly.severity in [\"warn\", \"high\"]\n    decision: require_approval\n    reason: This agent is behaving unusually right now. A person approves writes until the anomaly is resolved.\n"
	p, diags := Compile([]byte(src), Options{})
	if p == nil {
		t.Fatalf("compile: %v", diags)
	}
	in := Input{V: InputVersion, Tool: ToolInput{ToolFacts: ToolFacts{Name: "postgres.execute", SideEffect: "write"}},
		Agent: AgentInput{AgentFacts: AgentFacts{Name: "a", Status: StatusActive}}, Time: DefaultCaseTime}
	if d := Evaluate(bg, p, in, EvalOptions{}); d.Outcome != Allow {
		t.Fatalf("inactive: %s %s", d.Outcome, d.RuleID)
	}
	in.Anomaly = AnomalyFacts{Active: true, Kinds: []string{"volume_spike"}, Severity: "warn"}
	if d := Evaluate(bg, p, in, EvalOptions{}); d.Outcome != RequireApproval || d.RuleID != "approve-while-anomalous" {
		t.Fatalf("active: %s %s", d.Outcome, d.RuleID)
	}
	in.Anomaly = AnomalyFacts{Active: true, Kinds: []string{"off_hours"}, Severity: "info"}
	if d := Evaluate(bg, p, in, EvalOptions{}); d.Outcome != Allow {
		t.Fatalf("info: %s %s", d.Outcome, d.RuleID)
	}
}

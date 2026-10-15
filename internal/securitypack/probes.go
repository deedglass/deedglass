package securitypack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
)

// The probes: fixed inputs the enforced version is evaluated on with the real evaluator, so
// default_deny reports the rules' behaviour, not their text. The agent, org and time are fixed
// too; the SQL tool is a Postgres write and the refund is a $5,000.00 USD refund on a charge
// Stripe confirmed, undisputed, with that much left to refund.
type probeInput struct {
	name string
	in   policy.Input
}

// ProbeNames are the probes in the order they are reported.
var ProbeNames = []string{"drop_table", "truncate", "delete_all", "large_refund"}

// probeDeadline is generous: a deny caused by the deadline would read as a refusal.
const probeDeadline = 5 * time.Second

var probeTime = time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

func probeBase(tool, ctype string) policy.Input {
	conn, _, _ := strings.Cut(tool, ".")
	return policy.Input{V: policy.InputVersion,
		Org:   policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_00000000000000000000000000", Timezone: "UTC"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: "agt_00000000000000000000000000", Name: "review-probe", Labels: map[string]string{}, Status: "active"}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool, Connector: conn, ConnectorType: ctype, SideEffect: "write"}, DefaultSchema: "public"},
		Time:  probeTime, Request: policy.RequestFacts{Mode: "gateway"}}
}

func sqlProbe(name, tool, sql string) probeInput {
	in := probeBase(tool, "postgres")
	b, _ := json.Marshal(map[string]string{"sql": sql})
	in.Args = b
	return probeInput{name: name, in: in}
}

// probes builds the four inputs. pgTool and stripeTool are the org's own tool names when it has
// such connectors, so connector-scoped rules see their connector; otherwise the type's name.
func probes(pgTool, stripeTool string) []probeInput {
	refund := probeBase(stripeTool, "stripe")
	refund.Args = json.RawMessage(`{"charge":"ch_review_probe","amount":500000,"reason":"requested_by_customer"}`)
	refund.Request.Stripe = policy.StripeFacts{Enriched: true, Charge: "ch_review_probe", Currency: "usd", ChargeAmount: 500000,
		Refundable: 500000, ChargeAgeDays: 3, AmountDisplay: "$5,000.00"}
	return []probeInput{
		sqlProbe("drop_table", pgTool, "DROP TABLE public.orders"),
		sqlProbe("truncate", pgTool, "TRUNCATE public.orders"),
		sqlProbe("delete_all", pgTool, "DELETE FROM public.orders"),
		{name: "large_refund", in: refund},
	}
}

// probeLabel is a probe as the answer names it.
func probeLabel(name string) string {
	switch name {
	case "drop_table":
		return "DROP TABLE"
	case "truncate":
		return "TRUNCATE"
	case "delete_all":
		return "DELETE with no WHERE"
	case "large_refund":
		return "a $5,000 refund"
	}
	return name
}

// ruleFacts is what the core derives from the enforced version's source.
type ruleFacts struct {
	rules          int
	budgets        int
	approvalRules  int
	twoPersonRules int
	probes         []Probe
}

// deriveRules compiles the enforced version and runs the probes. A source that doesn't compile
// (it compiled when it was pushed, so this means the stored source changed) is an error: the
// questions that need the rules become Can't tell.
func deriveRules(ctx context.Context, v *ActiveVersion, pgTool, stripeTool string) (ruleFacts, error) {
	var rf ruleFacts
	c, diags := policy.Compile([]byte(v.YAML), policy.Options{File: v.Name + ".yaml"})
	if c == nil {
		return rf, fmt.Errorf("the enforced version %s doesn't compile (%d problems)", v.VersionID, len(diags))
	}
	if v.VersionHash != "" && c.VersionHash != v.VersionHash { //deedglass:allow-compare a policy version's public hash
		return rf, fmt.Errorf("the enforced version %s doesn't match its stored hash", v.VersionID)
	}
	rf.rules, rf.budgets = len(c.Rules), len(c.Budgets)
	for _, r := range c.Rules {
		if r.Decision == policy.RequireApproval {
			rf.approvalRules++
			if r.Approval != nil && r.Approval.Required >= 2 {
				rf.twoPersonRules++
			}
		}
	}
	for _, p := range probes(pgTool, stripeTool) {
		d := policy.Evaluate(ctx, c, p.in, policy.EvalOptions{Deadline: probeDeadline})
		if len(d.Errors) > 0 || d.Code == policy.CodePolicyUnavailable {
			// A deny from an evaluation error would overstate the rules: never count it.
			return rf, fmt.Errorf("the probe %s couldn't be evaluated", p.name)
		}
		rf.probes = append(rf.probes, Probe{Name: p.name, Outcome: string(d.Outcome), RuleID: d.RuleID})
	}
	return rf, nil
}

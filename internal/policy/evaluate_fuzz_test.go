package policy

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/examples/policies"
)

// starterLike are policies of the shapes S12's starters use: postgres-safe, a refunds policy with
// a measured org budget, and default-deny.
var starterLike = []string{
	evalHead + "budgets:\n  - id: refunds-per-day\n    per: org\n    window: day\n    max_spend: \"500.00 usd\"\n    measure: 'has(args.amount) && type(args.amount) == int ? args.amount : 0'\n" +
		"rules:\n  - id: needs-amount\n    match: { tool: stripe.create_refund }\n    when: '!has(args.amount) || type(args.amount) != int || args.amount <= 0'\n    decision: deny\n    reason: Refunds need an amount.\n" +
		"  - id: small\n    match: { tool: stripe.create_refund }\n    when: 'args.amount <= minor(\"50.00 usd\")'\n    decision: allow\n    verify: L1\n" +
		"  - id: large\n    match: { tool: stripe.create_refund }\n    decision: require_approval\n    reason: Large refunds wait.\n",
	evalHead + "budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\n",
}

func FuzzEvaluate(f *testing.F) {
	src, err := policies.Source("postgres-safe")
	if err != nil {
		f.Fatal(err)
	}
	policies := []*Compiled{mustCompile(f, string(src))}
	for _, s := range starterLike {
		policies = append(policies, mustCompile(f, s))
	}
	// Seeds: every SQL string of the sqlguard corpus, plus a few argument shapes.
	var corpus struct {
		Cases []struct {
			SQL *string `yaml:"sql"`
		} `yaml:"cases"`
	}
	if b, err := os.ReadFile("../sqlguard/testdata/corpus.yaml"); err == nil {
		_ = yaml.Unmarshal(b, &corpus)
	}
	for _, c := range corpus.Cases {
		if c.SQL != nil {
			b, _ := json.Marshal(map[string]string{"sql": *c.SQL})
			f.Add("postgres.execute", string(b), uint8(0), int64(0))
		}
	}
	for _, a := range []string{`{"amount": 2000}`, `{"amount": -1}`, `{"amount": 1.5}`, `{}`, `{"amount": "x"}`, `[1]`, `{"items":[1,2,3]}`} {
		f.Add("stripe.create_refund", a, uint8(1), int64(3))
	}
	f.Fuzz(func(t *testing.T, tool, args string, status uint8, used int64) {
		x := in(tool, args)
		x.Agent.Status = []string{"active", "suspended", "stopped", "", "weird"}[int(status)%5]
		x.Org.Stopped = status%7 == 6
		if used < 0 {
			used = -used
		}
		x.Usage = UsageSnapshot{CallsMinute: used % 100, CallsToday: used, Budgets: map[string]int64{"agent-rate": used % 100, "refunds-per-day": used}}
		x.Agent.Budgets = &AgentBudgets{CallsPerMinute: i64(1 + used%120)}
		for _, c := range policies {
			start := time.Now()
			d := Evaluate(context.Background(), c, x, EvalOptions{Explain: status%2 == 0, Deadline: testDeadline})
			if time.Since(start) > 2*time.Second {
				t.Fatalf("evaluation took %v", time.Since(start))
			}
			switch d.Outcome {
			case Allow, Deny, RequireApproval:
			default:
				t.Fatalf("outcome %q", d.Outcome)
			}
			if d.Outcome != Allow && d.Message == "" {
				t.Fatalf("no message: %+v", d)
			}
			if d.Outcome == Deny && (len(d.Reservations) > 0 || len(d.CostBudgets) > 0) {
				t.Fatalf("a deny with reservations: %+v", d)
			}
			if d.Outcome == Allow && d.Code != CodeNone {
				t.Fatalf("an allow with a code: %+v", d)
			}
		}
	})
}

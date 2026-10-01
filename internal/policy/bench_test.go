package policy

import (
	"fmt"
	"strings"
	"testing"
)

// benchPolicy builds a policy with n rules of the realistic kind: a match, a `when` on args or
// sql, and every decision.
func benchPolicy(n int) []byte {
	var b strings.Builder
	b.WriteString("deedglass: policy/v1\nname: bench\ndefaults:\n  decision: deny\n  reason: No rule allows this action.\n")
	b.WriteString("budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\n  - id: refunds-per-day\n    match: { tool: stripe.create_refund }\n    per: org\n    window: day\n    max_spend: \"500.00 usd\"\n    measure: args.amount\n")
	b.WriteString("rules:\n")
	whens := []string{
		`sql.kind in ["drop", "truncate"] || sql.multi || sql.ambiguous`,
		`has(args.amount) && args.amount <= minor("50.00 usd")`,
		`sql.tables.all(t, glob(t, "public.*")) && !sql.writes`,
		`agent.labels["env"] == "prod" && time.hour >= 9 && time.hour < 18`,
		`args.channel.matches("^#(support|ops)-[a-z]+$")`,
	}
	decs := []string{"deny", "allow", "require_approval"}
	for i := 0; i < n; i++ {
		d := decs[i%3]
		fmt.Fprintf(&b, "  - id: rule-%d\n    match: { tool: \"svc%d.*\", side_effect: [write, destructive] }\n    when: '%s'\n    decision: %s\n", i, i, whens[i%len(whens)], d)
		if d != "allow" {
			fmt.Fprintf(&b, "    reason: Rule %d decides this action.\n", i)
		}
	}
	return []byte(b.String())
}

func benchCompile(b *testing.B, n int) {
	src := benchPolicy(n)
	if c, d := Compile(src, Options{}); c == nil {
		b.Fatal(d)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if c, _ := Compile(src, Options{}); c == nil {
			b.Fatal("not accepted")
		}
	}
}

func BenchmarkCompile50(b *testing.B)  { benchCompile(b, 50) }
func BenchmarkCompile500(b *testing.B) { benchCompile(b, 500) }

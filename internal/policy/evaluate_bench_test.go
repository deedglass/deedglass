package policy

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// evalBenchPolicy builds n rules that all statically match a Postgres action and whose conditions
// read args and sql, so every rule's CEL runs before the default decides: the worst case for one
// action, and the case the §3.7 budget measures.
func evalBenchPolicy(n int) string {
	var b strings.Builder
	b.WriteString("deedglass: policy/v1\nname: bench-eval\ndefaults:\n  decision: deny\n  reason: No rule allows this action.\n")
	b.WriteString("budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\n  - id: agent-daily\n    window: day\n    max_calls: 5000\n")
	b.WriteString("rules:\n")
	whens := []string{
		`sql.kind in ["drop", "truncate"] && sql.multi`,
		`has(args.amount) && args.amount > minor("50.00 usd")`,
		`sql.writes && sql.tables.exists(t, glob(t, "audit.*"))`,
		`agent.labels["env"] == "staging" && time.hour < 6`,
		`size(sql.dangerous_functions) > 0 && !request.intent_signed`,
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  - id: rule-%d\n    match: { connector_type: postgres }\n    when: '%s'\n    decision: deny\n    reason: Rule %d decides this action.\n", i, whens[i%len(whens)], i)
	}
	return b.String()
}

func benchEvaluate(b *testing.B, c *Compiled, x Input, o EvalOptions) {
	ctx := context.Background()
	if d := Evaluate(ctx, c, x, o); d.Outcome == "" {
		b.Fatal("no decision")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Evaluate(ctx, c, x, o)
	}
}

var benchSQL = sqlIn("postgres.query", "SELECT o.id, o.status FROM public.orders o JOIN public.customers c ON c.id = o.customer_id WHERE o.id = 42")

func BenchmarkEvaluatePostgresSafe(b *testing.B) {
	benchEvaluate(b, postgresSafe(b), benchSQL, EvalOptions{})
}

func BenchmarkEvaluate50(b *testing.B) {
	benchEvaluate(b, mustCompile(b, evalBenchPolicy(50)), benchSQL, EvalOptions{})
}

func BenchmarkEvaluate50Explain(b *testing.B) {
	benchEvaluate(b, mustCompile(b, evalBenchPolicy(50)), benchSQL, EvalOptions{Explain: true})
}

func BenchmarkEvaluate500(b *testing.B) {
	benchEvaluate(b, mustCompile(b, evalBenchPolicy(500)), benchSQL, EvalOptions{})
}

package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types/ref"
)

var evalTime = time.Date(2026, 10, 4, 4, 30, 0, 0, time.UTC) // 10:00 in Asia/Kolkata

// testDeadline replaces the 50 ms backstop in tests that check decisions, not deadlines: under the
// race detector and a loaded machine one evaluation can take longer, and that would be a flake.
const testDeadline = 10 * time.Second

func mustCompile(t testing.TB, src string) *Compiled {
	t.Helper()
	c, ds := Compile([]byte(src), Options{File: "test.yaml"})
	if c == nil {
		t.Fatalf("compile: %v\n%s", ds, src)
	}
	return c
}

func postgresSafe(t testing.TB) *Compiled {
	t.Helper()
	src, err := os.ReadFile("testdata/eval/postgres-safe.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return mustCompile(t, string(src))
}

// in builds an input for a tool with args.
func in(tool string, args string) Input {
	ct := ""
	if strings.HasPrefix(tool, "postgres.") {
		ct = "postgres"
	} else if strings.HasPrefix(tool, "stripe.") {
		ct = "stripe"
	}
	conn, _, _ := strings.Cut(tool, ".")
	return Input{V: 1,
		Org:   OrgInput{OrgFacts: OrgFacts{ID: "org_01jb8k0000000000000000000a", Timezone: "Asia/Kolkata"}},
		Agent: AgentInput{AgentFacts: AgentFacts{ID: "agt_01jb8k0000000000000000000a", Name: "support-bot", Labels: map[string]string{"env": "prod"}, Status: "active"}},
		Tool:  ToolInput{ToolFacts: ToolFacts{Name: tool, Connector: conn, ConnectorType: ct, SideEffect: "write"}},
		Args:  json.RawMessage(args), Time: evalTime,
		Request: RequestFacts{Mode: "gateway"}}
}

func sqlIn(tool, sql string) Input {
	b, _ := json.Marshal(map[string]string{"sql": sql})
	return in(tool, string(b))
}

func i64(n int64) *int64 { return &n }

const evalHead = "deedglass: policy/v1\nname: acme-test\ndefaults:\n  decision: deny\n  reason: No rule allows this action.\n"

func TestEvaluateMatrix(t *testing.T) {
	allowAll := evalHead + "rules:\n  - id: allow-all\n    decision: allow\n"
	ctx := context.Background()
	type check func(t *testing.T, d Decision)
	is := func(o Outcome, code Code, source, rule string) check {
		return func(t *testing.T, d Decision) {
			t.Helper()
			if d.Outcome != o || d.Code != code || d.Source != source || d.RuleID != rule {
				t.Fatalf("got %s %s %s %q (%s), want %s %s %s %q", d.Outcome, d.Code, d.Source, d.RuleID, d.Message, o, code, source, rule)
			}
		}
	}
	cases := []struct {
		name   string
		policy string
		input  func() Input
		checks []check
	}{
		{"org stopped beats allow-all", allowAll, func() Input { x := in("stripe.refund", "{}"); x.Org.Stopped = true; return x },
			[]check{is(Deny, CodeOrgStopped, SourceStop, ""), func(t *testing.T, d Decision) {
				if d.Message != msgOrgStopped {
					t.Fatal(d.Message)
				}
			}}},
		{"agent stopped", allowAll, func() Input { x := in("stripe.refund", "{}"); x.Agent.Status = "stopped"; return x },
			[]check{is(Deny, CodeAgentStopped, SourceStop, ""), func(t *testing.T, d Decision) {
				if d.Message != `Agent "support-bot" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.` {
					t.Fatal(d.Message)
				}
			}}},
		{"agent suspended", allowAll, func() Input { x := in("stripe.refund", "{}"); x.Agent.Status = "suspended"; return x },
			[]check{is(Deny, CodeAgentSuspended, SourceStop, "")}},
		{"budget exceeded beats a later allow", evalHead + "budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input {
				x := in("stripe.refund", "{}")
				x.Usage.Budgets = map[string]int64{"agent-rate": 60}
				return x
			},
			[]check{is(Deny, CodeBudgetExceeded, SourceBudget, "agent-rate"), func(t *testing.T, d Decision) {
				if d.Budget == nil || d.Budget.Limit != 60 || d.Budget.Used != 60 || d.Budget.RetryAfterS != 60 || d.Budget.Unit != "calls" {
					t.Fatalf("%+v", d.Budget)
				}
				if d.Message != `Budget "agent-rate" reached: 60 of 60 calls this minute. Try again in 60 s.` {
					t.Fatal(d.Message)
				}
				if len(d.Reservations) != 0 {
					t.Fatal("a deny keeps no reservations")
				}
			}}},
		{"budget within its limit reserves", evalHead + "budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input {
				x := in("stripe.refund", "{}")
				x.Usage.Budgets = map[string]int64{"agent-rate": 59}
				return x
			},
			[]check{is(Allow, CodeNone, SourceRule, "allow-all"), func(t *testing.T, d Decision) {
				var budget []Reservation
				for _, r := range d.Reservations {
					if r.Budget == "agent-rate" {
						budget = append(budget, r)
					}
				}
				if len(budget) != 1 || budget[0].Limit != 60 || budget[0].Amount != 1 || budget[0].Counter != "budget:agent-rate" ||
					budget[0].Scope != "agt_01jb8k0000000000000000000a" || !budget[0].WindowStart.Equal(evalTime) {
					t.Fatalf("%+v", d.Reservations)
				}
				if len(d.Reservations) != 4 { // calls minute, hour, day, and the budget
					t.Fatalf("%d reservations", len(d.Reservations))
				}
			}}},
		{"agent calls_per_minute 60 used 60 beats a policy budget of 100",
			evalHead + "budgets:\n  - id: wide\n    window: minute\n    max_calls: 100\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input {
				x := in("stripe.refund", "{}")
				x.Agent.Budgets = &AgentBudgets{CallsPerMinute: i64(60)}
				x.Usage.CallsMinute = 60
				return x
			},
			[]check{is(Deny, CodeBudgetExceeded, SourceBudget, AgentCallsPerMinute), func(t *testing.T, d Decision) {
				if d.Message != `Budget "calls_per_minute" for agent "support-bot" reached: 60 of 60 calls this minute. Try again in 60 s.` {
					t.Fatal(d.Message)
				}
			}}},
		{"a stricter policy budget (30/min) beats the agent's 60/min",
			evalHead + "budgets:\n  - id: strict\n    window: minute\n    max_calls: 30\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input {
				x := in("stripe.refund", "{}")
				x.Agent.Budgets = &AgentBudgets{CallsPerMinute: i64(60)}
				x.Usage.CallsMinute = 30
				x.Usage.Budgets = map[string]int64{"strict": 30}
				return x
			},
			[]check{is(Deny, CodeBudgetExceeded, SourceBudget, "strict")}},
		{"agent calls_per_day and spend_per_day",
			allowAll,
			func() Input {
				x := in("stripe.refund", "{}")
				x.Agent.Budgets = &AgentBudgets{CallsPerDay: i64(5000), SpendPerDay: &Money{Amount: 5000, Currency: "usd"}}
				x.Usage.CallsToday = 10
				x.Usage.SpendToday = 5000
				x.Usage.Currency = "usd"
				return x
			},
			[]check{is(Deny, CodeBudgetExceeded, SourceBudget, AgentSpendPerDay), func(t *testing.T, d Decision) {
				if d.Message != `Budget "spend_per_day" for agent "support-bot" reached: $50.00 of $50.00 today. It resets at midnight Asia/Kolkata (in 14 h).` {
					t.Fatal(d.Message)
				}
			}}},
		{"cost-based org budget at its limit denies before execution",
			evalHead + "budgets:\n  - id: refunds-per-day\n    per: org\n    window: day\n    max_spend: \"500.00 usd\"\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input {
				x := in("stripe.refund", "{}")
				x.Usage.Budgets = map[string]int64{"refunds-per-day": 50000}
				return x
			},
			[]check{is(Deny, CodeBudgetExceeded, SourceBudget, "refunds-per-day"), func(t *testing.T, d Decision) {
				if d.Message != `Budget "refunds-per-day" reached: $500.00 of $500.00 across all agents today. It resets at midnight Asia/Kolkata (in 14 h).` {
					t.Fatal(d.Message)
				}
			}}},
		{"cost-based budget under its limit becomes a cost target",
			evalHead + "budgets:\n  - id: refunds-per-day\n    per: org\n    window: day\n    max_spend: \"500.00 usd\"\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(Allow, CodeNone, SourceRule, "allow-all"), func(t *testing.T, d Decision) {
				if len(d.CostBudgets) != 2 || d.CostBudgets[1].Budget != "refunds-per-day" || d.CostBudgets[1].Scope != ScopeOrg ||
					d.CostBudgets[1].Currency != "usd" || !d.CostBudgets[1].WindowStart.Equal(time.Date(2026, 10, 3, 18, 30, 0, 0, time.UTC)) {
					t.Fatalf("%+v", d.CostBudgets)
				}
			}}},
		{"measure counts the action's amount",
			evalHead + "budgets:\n  - id: refunds\n    window: day\n    max_spend: \"50.00 usd\"\n    measure: 'has(args.amount) ? args.amount : 0'\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input {
				x := in("stripe.refund", `{"amount": 2000}`)
				x.Usage.Budgets = map[string]int64{"refunds": 3001}
				return x
			},
			[]check{is(Deny, CodeBudgetExceeded, SourceBudget, "refunds")}},
		{"measure error denies policy_denied",
			evalHead + "budgets:\n  - id: refunds\n    window: day\n    max_spend: \"50.00 usd\"\n    measure: 'args.amount'\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", `{}`) },
			[]check{is(Deny, CodePolicyDenied, SourceBudget, "budget:refunds"), func(t *testing.T, d Decision) {
				if len(d.Errors) != 1 || !strings.HasPrefix(d.Message, `Blocked by policy "budget:refunds": Deedglass could not measure this action`) {
					t.Fatal(d.Message, d.Errors)
				}
			}}},
		{"measure that isn't a non-negative int denies",
			evalHead + "budgets:\n  - id: refunds\n    window: day\n    max_spend: \"50.00 usd\"\n    measure: 'args.amount'\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", `{"amount": -5}`) },
			[]check{is(Deny, CodePolicyDenied, SourceBudget, "budget:refunds")}},
		{"measure returning a double denies",
			evalHead + "budgets:\n  - id: refunds\n    window: day\n    max_spend: \"50.00 usd\"\n    measure: 'args.amount'\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", `{"amount": 5.5}`) },
			[]check{is(Deny, CodePolicyDenied, SourceBudget, "budget:refunds")}},
		{"first match wins",
			evalHead + "rules:\n  - id: first\n    decision: require_approval\n    reason: Wait for a human.\n  - id: second\n    decision: allow\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(RequireApproval, CodeApprovalRequired, SourceRule, "first")}},
		{"no rule matches: the default decides",
			evalHead + "rules:\n  - id: only-github\n    match: { tool: github.* }\n    decision: allow\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(Deny, CodePolicyDenied, SourceDefault, ""), func(t *testing.T, d Decision) {
				if d.Message != `Blocked by policy "acme-test": No rule allows this action.` {
					t.Fatal(d.Message)
				}
			}}},
		{"when error on an allow rule: skipped, the next rule decides",
			evalHead + "rules:\n  - id: big\n    decision: allow\n    when: 'args.amount > 5'\n  - id: fallback\n    decision: require_approval\n    reason: Wait for a human.\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(RequireApproval, CodeApprovalRequired, SourceRule, "fallback"), func(t *testing.T, d Decision) {
				if len(d.Errors) != 1 || !strings.HasPrefix(d.Errors[0], "big: ") {
					t.Fatal(d.Errors)
				}
			}}},
		{"when error on a deny rule: deny with the suffix",
			evalHead + "rules:\n  - id: cap-refunds\n    decision: deny\n    reason: Refunds over $50.00 wait for a human.\n    when: 'args.amount > 5000'\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(Deny, CodePolicyDenied, SourceRule, "cap-refunds"), func(t *testing.T, d Decision) {
				if d.Message != `Blocked by policy "cap-refunds": Refunds over $50.00 wait for a human. (Deedglass could not evaluate this rule: no such key: amount.)` {
					t.Fatal(d.Message)
				}
			}}},
		{"when error on an approval rule: it still decides",
			evalHead + "rules:\n  - id: big\n    decision: require_approval\n    reason: Big ones wait.\n    when: 'args.amount > 5000'\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(RequireApproval, CodeApprovalRequired, SourceRule, "big")}},
		{"a dyn when that isn't a bool is an error",
			evalHead + "rules:\n  - id: flag\n    decision: allow\n    when: 'args.flag'\n",
			func() Input { return in("stripe.refund", `{"flag": "yes"}`) },
			[]check{is(Deny, CodePolicyDenied, SourceDefault, "")}},
		{"shadow mode: same decision, no reservations",
			allowAll, func() Input { return in("stripe.refund", "{}") },
			[]check{is(Allow, CodeNone, SourceRule, "allow-all")}},
		{"require_approval merges approval defaults and keeps verify",
			evalHead + "  verify: L1\n  approval: { approvers: [role:owner], expires_in: 2h }\nrules:\n  - id: wait\n    decision: require_approval\n    reason: Wait.\n    approval: { required: 2, approvers: [role:owner, role:admin] }\n    verify: L2\n    notify: [slack]\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{is(RequireApproval, CodeApprovalRequired, SourceRule, "wait"), func(t *testing.T, d Decision) {
				if d.Approval == nil || d.Approval.Required != 2 || d.Approval.ExpiresIn != 7200 || d.Verify != L2 || len(d.Notify) != 1 {
					t.Fatalf("%+v %s %v", d.Approval, d.Verify, d.Notify)
				}
				if d.Message != `Waiting for approval under policy "wait": Wait.` {
					t.Fatal(d.Message)
				}
			}}},
		{"allow takes verify from the defaults",
			evalHead + "  verify: L1\nrules:\n  - id: allow-all\n    decision: allow\n",
			func() Input { return in("stripe.refund", "{}") },
			[]check{func(t *testing.T, d Decision) {
				if d.Verify != L1 {
					t.Fatal(d.Verify)
				}
			}}},
		{"invalid input denies with invalid-input",
			allowAll, func() Input { return in("stripe.refund", `[1]`) },
			[]check{is(Deny, CodePolicyDenied, SourceBuiltin, RuleInvalidInput)}},
		{"a bad time zone is invalid input",
			allowAll, func() Input { x := in("stripe.refund", "{}"); x.Org.Timezone = "Mars/Olympus"; return x },
			[]check{is(Deny, CodePolicyDenied, SourceBuiltin, RuleInvalidInput)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := mustCompile(t, c.policy)
			mode := ModeEnforce
			if strings.HasPrefix(c.name, "shadow") {
				mode = ModeShadow
			}
			d := Evaluate(ctx, p, c.input(), EvalOptions{Explain: true, Mode: mode, Deadline: testDeadline})
			for _, ch := range c.checks {
				ch(t, d)
			}
			if mode == ModeShadow && len(d.Reservations) != 0 {
				t.Fatal("shadow returned reservations")
			}
			if d.Outcome != Allow && d.Message == "" {
				t.Fatal("a non-allow decision needs a message")
			}
			// The same input without explain decides the same way.
			d2 := Evaluate(ctx, p, c.input(), EvalOptions{Mode: mode, Deadline: testDeadline})
			if d2.Outcome != d.Outcome || d2.RuleID != d.RuleID || d2.Message != d.Message || d2.Trace != nil {
				t.Fatalf("explain changed the decision: %+v vs %+v", d2, d)
			}
		})
	}
}

func bigList(n int) string {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("1")
	}
	b.WriteString("]}")
	return b.String()
}

func TestEvaluateUnavailable(t *testing.T) {
	ctx := context.Background()
	d := Evaluate(ctx, nil, in("stripe.refund", "{}"), EvalOptions{})
	if d.Outcome != Deny || d.Code != CodePolicyUnavailable || d.Message != MessageUnavailable {
		t.Fatalf("nil policy: %+v", d)
	}
	// A custom function that panics: cel-go recovers it as an "internal error".
	env, err := cel.NewEnv(cel.Function("boom", cel.Overload("boom", nil, cel.BoolType,
		cel.FunctionBinding(func(...ref.Val) ref.Val { panic("boom") }))))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile("boom()")
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	c := &Compiled{Name: "x", Defaults: Defaults{Decision: Allow}, Rules: []Rule{{ID: "r", Decision: Allow, When: prg}}}
	if d := Evaluate(ctx, c, in("stripe.refund", "{}"), EvalOptions{}); d.Code != CodePolicyUnavailable {
		t.Fatalf("panic in a function: %+v", d)
	}
	// A Go panic inside the evaluator (a rule with a broken matcher can't happen through
	// Compile; simulate one with a nil-program budget measure) is recovered too.
	c2 := &Compiled{Name: "x", Defaults: Defaults{Decision: Allow}, Budgets: []Budget{{ID: "b", Window: "day", MaxSpend: &Money{Amount: 1, Currency: "usd"}, Measure: panicProgram{}}}}
	if d := Evaluate(ctx, c2, in("stripe.refund", "{}"), EvalOptions{}); d.Code != CodePolicyUnavailable {
		t.Fatalf("go panic: %+v", d)
	}
	// Context deadline: an already-cancelled context denies policy_unavailable.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	p := mustCompile(t, evalHead+"rules:\n  - id: allow-big\n    decision: allow\n    when: 'args.items.all(x, x > 0)'\n")
	if d := Evaluate(cctx, p, in("stripe.refund", bigList(100)), EvalOptions{}); d.Code != CodePolicyUnavailable {
		t.Fatalf("cancelled: %+v", d)
	}
	// A deadline that passes during evaluation of a long comprehension.
	if d := Evaluate(ctx, p, in("stripe.refund", bigList(30000)), EvalOptions{Deadline: time.Nanosecond}); d.Outcome != Deny {
		t.Fatalf("deadline: %+v", d)
	}
}

// panicProgram is a cel.Program whose evaluation panics outside cel-go's own recovery.
type panicProgram struct{ cel.Program }

func (panicProgram) ContextEval(context.Context, any) (ref.Val, *cel.EvalDetails, error) { panic("x") }

func TestEvaluatePair(t *testing.T) {
	enforce := mustCompile(t, evalHead+"rules:\n  - id: allow-all\n    decision: allow\n")
	shadow := mustCompile(t, evalHead+"rules:\n  - id: deny-all\n    decision: deny\n    reason: Nothing runs.\n")
	d, s := EvaluatePair(context.Background(), enforce, shadow, in("stripe.refund", "{}"), EvalOptions{})
	if d.Outcome != Allow || s == nil || s.Outcome != Deny || len(d.Reservations) == 0 || len(s.Reservations) != 0 {
		t.Fatalf("%+v %+v", d, s)
	}
	d, s = EvaluatePair(context.Background(), enforce, nil, in("stripe.refund", "{}"), EvalOptions{})
	if s != nil || d.Outcome != Allow {
		t.Fatal("no shadow")
	}
	d, s = EvaluatePair(context.Background(), enforce, shadow, in("stripe.refund", "[]"), EvalOptions{})
	if d.RuleID != RuleInvalidInput || s == nil || s.RuleID != RuleInvalidInput {
		t.Fatal("invalid input in a pair")
	}
}

func TestEvaluateDeterminism(t *testing.T) {
	c := postgresSafe(t)
	inputs := []Input{
		sqlIn("postgres.execute", "/* tidy up */ DrOp TABLE public.orders"),
		sqlIn("postgres.query", "SELECT id FROM orders LIMIT 5"),
		sqlIn("postgres.execute", "UPDATE public.orders SET status = 'shipped' WHERE id = 42"),
	}
	want := make([]string, len(inputs))
	for i, x := range inputs {
		d := Evaluate(context.Background(), c, x, EvalOptions{Explain: true, Deadline: testDeadline})
		b, _ := json.Marshal(d)
		want[i] = string(b)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 1000)
	for g := 0; g < 1000; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			i := g % len(inputs)
			d := Evaluate(context.Background(), c, inputs[i], EvalOptions{Explain: true, Deadline: testDeadline})
			b, _ := json.Marshal(d)
			if string(b) != want[i] {
				errs <- string(b)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("a different decision: %s", e)
	}
}

func TestPostgresSafeDemo(t *testing.T) {
	c := postgresSafe(t)
	ctx := context.Background()
	cases := []struct {
		tool, sql string
		usage     UsageSnapshot
		outcome   Outcome
		rule      string
		message   string
	}{
		{"postgres.execute", "/* tidy up */ DrOp TABLE public.orders", UsageSnapshot{}, Deny, "block-destructive-sql",
			`Blocked by policy "block-destructive-sql": DROP and TRUNCATE are never allowed for agents.`},
		{"postgres.execute", "COMMIT; DROP TABLE orders", UsageSnapshot{}, Deny, "one-statement-only", ""},
		{"postgres.query", "SELECT id FROM orders LIMIT 5", UsageSnapshot{}, Allow, "allow-reads", ""},
		{"postgres.execute", "UPDATE public.orders SET status = 'shipped' WHERE id = 42", UsageSnapshot{}, RequireApproval, "writes-need-approval",
			`Waiting for approval under policy "writes-need-approval": Writes to this database wait for a human. An admin can approve in Console › Approvals.`},
		{"postgres.query", "SELECT 1", UsageSnapshot{CallsMinute: 60, Budgets: map[string]int64{"agent-rate": 60}}, Deny, "agent-rate", ""},
		{"postgres.execute", "DELETE FROM orders", UsageSnapshot{}, Deny, "block-unbounded-writes", ""},
		{"postgres.query", "WITH d AS (DELETE FROM orders RETURNING *) SELECT count(*) FROM d", UsageSnapshot{}, Deny, "block-unbounded-writes", ""},
		{"postgres.execute", "INSERT INTO public.support_notes (order_id, body) VALUES (42, 'Called')", UsageSnapshot{}, Allow, "allow-listed-writes", ""},
		{"postgres.execute", "DRОP TABLE orders", UsageSnapshot{}, Deny, "block-unclassified-sql", ""},
		{"postgres.execute", "SELECT pg_logical_emit_message(true, 'deedglass', 'act_x')", UsageSnapshot{}, Deny, "block-server-side-code", ""},
		{"postgres.execute", "SELECT 1", UsageSnapshot{}, Deny, "", ""},
		{"postgres.call", "SELECT 1", UsageSnapshot{}, RequireApproval, "functions-need-approval", ""},
	}
	for _, k := range cases {
		x := sqlIn(k.tool, k.sql)
		x.Usage = k.usage
		d := Evaluate(ctx, c, x, EvalOptions{Explain: true, Deadline: testDeadline})
		if d.Outcome != k.outcome || d.RuleID != k.rule || (k.message != "" && d.Message != k.message) {
			t.Errorf("%s %q: %s %s %q", k.tool, k.sql, d.Outcome, d.RuleID, d.Message)
		}
	}
	d := Evaluate(ctx, c, sqlIn("postgres.execute", "/* tidy up */ DrOp TABLE public.orders"), EvalOptions{Explain: true, Deadline: testDeadline})
	var rules []string
	for _, s := range d.Trace {
		rules = append(rules, fmt.Sprintf("%s:%s:%s:%s", s.Step, s.ID, s.Result, s.Detail))
	}
	want := []string{"stop::pass:agent active · org running", "budget:agent-rate:pass:0 of 60 calls this minute",
		"budget:agent-daily:pass:0 of 5,000 calls today", "rule:block-unclassified-sql:no_match:when → false",
		"rule:one-statement-only:no_match:when → false", "rule:block-destructive-sql:match:when → true", "rule::not_evaluated:9 more rules not evaluated"}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("trace:\n%s", strings.Join(rules, "\n"))
	}
	if d.RuleLine != 39 || d.SQLGuard != "1" || d.Facts.SQL.Kind != "drop" {
		t.Fatalf("line %d sqlguard %q kind %q", d.RuleLine, d.SQLGuard, d.Facts.SQL.Kind)
	}
}

func TestAlwaysDenies(t *testing.T) {
	ps := postgresSafe(t)
	dd := mustCompile(t, evalHead+"budgets:\n  - id: agent-rate\n    window: minute\n    max_calls: 60\n")
	allowDefault := mustCompile(t, "deedglass: policy/v1\nname: open\ndefaults:\n  decision: allow\nrules:\n  - id: no-drops\n    match: { tool: postgres.drop_* }\n    decision: deny\n    reason: Never.\n")
	gh := mustCompile(t, evalHead+"rules:\n  - id: reads\n    match: { tool: github.get_* }\n    decision: allow\n  - id: merges\n    match: { tool: github.merge_* }\n    decision: deny\n    reason: Humans merge.\n  - id: branch-writes\n    match: { tool: github.create_or_update_file }\n    decision: require_approval\n    reason: Wait.\n    when: 'has(args.branch)'\n")
	agent := AgentFacts{ID: "agt_01jb8k0000000000000000000a", Name: "support-bot"}
	cases := []struct {
		name string
		c    *Compiled
		tool ToolFacts
		want bool
	}{
		{"postgres.query has a when rule first", ps, ToolFacts{Name: "postgres.query", ConnectorType: "postgres"}, false},
		{"an unknown connector type falls to default deny", ps, ToolFacts{Name: "slack.post", ConnectorType: "slack"}, true},
		{"postgres.describe: block-unclassified has a when", ps, ToolFacts{Name: "postgres.describe", ConnectorType: "postgres"}, false},
		{"postgres tool without the postgres type: describe allowed", ps, ToolFacts{Name: "postgres.describe"}, false},
		{"postgres tool without the type: unknown tool default deny", ps, ToolFacts{Name: "postgres.vacuum"}, true},
		{"default-deny with no rules", dd, ToolFacts{Name: "anything"}, true},
		{"default allow", allowDefault, ToolFacts{Name: "stripe.refund"}, false},
		{"deny without when", allowDefault, ToolFacts{Name: "postgres.drop_table"}, true},
		{"an earlier allow", gh, ToolFacts{Name: "github.get_issue"}, false},
		{"deny without when, github", gh, ToolFacts{Name: "github.merge_pull_request"}, true},
		{"approval with when", gh, ToolFacts{Name: "github.create_or_update_file"}, false},
		{"nil policy", nil, ToolFacts{Name: "x"}, true},
	}
	for _, k := range cases {
		if got := AlwaysDenies(k.c, k.tool, agent); got != k.want {
			t.Errorf("%s: %v, want %v", k.name, got, k.want)
		}
	}
	if _, r, decided := alwaysDenies(gh, ToolFacts{Name: "github.merge_x"}, agent); r == nil || r.ID != "merges" || !decided {
		t.Fatal("the deciding rule")
	}
}

func TestPrepare(t *testing.T) {
	cases := []struct {
		args string
		ok   bool
		key  string
		want any
	}{
		{`{"a": 1}`, true, "a", int64(1)},
		{`{"a": 1.5}`, true, "a", 1.5},
		{`{"a": 1e3}`, true, "a", 1000.0},
		{`{"a": 9223372036854775808}`, true, "a", 9.223372036854776e18},
		{`{"a": "x"}`, true, "a", "x"},
		{`{"a": true}`, true, "a", true},
		{`{"a": null}`, true, "a", nil},
		{`{"a": [1, "b"]}`, true, "a", []any{int64(1), "b"}},
		{`{"a": {"b": 2}}`, true, "a", map[string]any{"b": int64(2)}},
		{``, true, "", nil},
		{`null`, true, "", nil},
		{`[]`, false, "", nil},
		{`{"a": 1} x`, false, "", nil},
		{`{"a": 1, "a": 2}`, false, "", nil},
		{`{"a": }`, false, "", nil},
		{`{"a": 1e999}`, false, "", nil},
		{strings.Repeat(`{"a":`, 32) + "1" + strings.Repeat("}", 32), true, "", nil},
		{strings.Repeat(`{"a":`, 33) + "1" + strings.Repeat("}", 33), false, "", nil},
		{`{"a": "` + strings.Repeat("x", MaxArgsBytes) + `"}`, false, "", nil},
	}
	for _, c := range cases {
		x := in("stripe.refund", c.args)
		p, err := Prepare(x)
		if (err == nil) != c.ok {
			t.Errorf("%.40s: err %v", c.args, err)
			continue
		}
		if err == nil && c.key != "" && !reflect.DeepEqual(p.act.args[c.key], c.want) {
			t.Errorf("%s: %#v, want %#v", c.args, p.act.args[c.key], c.want)
		}
	}
	x := in("stripe.refund", "{}")
	x.V = 2
	if _, err := Prepare(x); err == nil {
		t.Error("version 2")
	}
	x = in("stripe.refund", "{}")
	x.Time = time.Time{}
	if _, err := Prepare(x); err == nil {
		t.Error("zero time")
	}
	x = in("stripe.refund", "{}")
	x.Agent.Status = "gone"
	if _, err := Prepare(x); err == nil {
		t.Error("status")
	}
}

func TestTimeParts(t *testing.T) {
	x := in("stripe.refund", "{}")
	x.Time = time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC) // 01:30 on Monday 5 October in Kolkata
	p, err := Prepare(x)
	if err != nil {
		t.Fatal(err)
	}
	tp := p.Facts().Time
	if tp.Hour != 1 || tp.Minute != 30 || tp.Weekday != "mon" || tp.Date != "2026-10-05" {
		t.Fatalf("%+v", tp)
	}
	x.Org.Timezone = ""
	p, _ = Prepare(x)
	if p.Facts().Time.Hour != 20 || p.Facts().Time.Weekday != "sun" {
		t.Fatalf("UTC: %+v", p.Facts().Time)
	}
	// The SQL facts are derived only for a postgres connector with a string args.sql.
	p, _ = Prepare(sqlIn("postgres.query", "SELECT 1"))
	if !p.Facts().SQL.Present || p.Facts().SQL.Kind != "select" {
		t.Fatal("postgres sql")
	}
	p, _ = Prepare(sqlIn("stripe.refund", "DROP TABLE x"))
	if p.Facts().SQL.Present || p.Facts().SQL.Kind != "none" {
		t.Fatal("non-postgres sql")
	}
	p, _ = Prepare(in("postgres.query", `{"sql": 5}`))
	if p.Facts().SQL.Present {
		t.Fatal("non-string sql")
	}
}

func TestInputDigest(t *testing.T) {
	a, err := sqlIn("postgres.query", "SELECT 1").Digest()
	if err != nil || !strings.HasPrefix(a, "sha256:") || len(a) != 71 {
		t.Fatal(a, err)
	}
	b, _ := sqlIn("postgres.query", "SELECT 2").Digest()
	if a == b {
		t.Fatal("different inputs, same digest")
	}
	x := sqlIn("postgres.query", "SELECT 1")
	raw, _ := json.Marshal(x)
	var y Input
	if err := json.Unmarshal(raw, &y); err != nil {
		t.Fatal(err)
	}
	c, _ := y.Digest()
	if a != c {
		t.Fatal("a JSON round trip changed the digest")
	}
}

func TestMessages(t *testing.T) {
	cases := map[time.Duration]string{0: "1 s", 14 * time.Second: "14 s", 59*time.Second + time.Millisecond: "60 s", 61 * time.Second: "2 m",
		12 * time.Minute: "12 m", 6*time.Hour + 12*time.Minute: "6 h 12 m", 2 * time.Hour: "2 h", 3*time.Hour + 59*time.Minute + 30*time.Second: "4 h"}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
	for _, c := range []struct {
		amount int64
		cur    string
		want   string
	}{{50000, "usd", "$500.00"}, {120000000, "inr", "₹1,200,000.00"}, {500, "jpy", "¥500"}, {1250, "chf", "12.50 chf"}} {
		if got := formatMoney(c.amount, c.cur); got != c.want {
			t.Errorf("formatMoney(%d, %s) = %q, want %q", c.amount, c.cur, got, c.want)
		}
	}
	org, stopped, suspended := StopMessages("support-bot")
	if org != msgOrgStopped || !strings.Contains(stopped, `"support-bot" is stopped`) || !strings.Contains(suspended, "suspended") {
		t.Fatal("StopMessages")
	}
	h := &BudgetHit{ID: "agent-rate", Per: PerAgent, Window: WindowHour, Limit: 5, Used: 5, Unit: "calls", ResetsAt: evalTime.Add(30 * time.Minute)}
	if got := budgetMessage(h, "x", "", evalTime); got != `Budget "agent-rate" reached: 5 of 5 calls this hour. Try again in 30 m.` {
		t.Fatal(got)
	}
	h = &BudgetHit{ID: "org-rate", Per: PerOrg, Window: WindowDay, Limit: 5000, Used: 5000, Unit: "calls", ResetsAt: evalTime.Add(time.Hour)}
	if got := budgetMessage(h, "x", "", evalTime); got != `Budget "org-rate" reached: 5,000 of 5,000 calls across all agents today. It resets at midnight UTC (in 1 h).` {
		t.Fatal(got)
	}
	for _, m := range []string{msgUnavailable, msgOrgStopped, msgCostLimit, msgInvalidInput, msgMeasureError} {
		if strings.ContainsAny(m, "!—–") {
			t.Errorf("%q breaks the voice rules", m)
		}
	}
}

// withCostLimit rebuilds every rule and measure program of c with a low runtime cost limit, so the
// cost-limit path runs on small inputs. Compile's own programs use MaxCost (100,000).
func withCostLimit(t *testing.T, c *Compiled, limit uint64) *Compiled {
	t.Helper()
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	build := func(src string) cel.Program {
		ast, iss := env.Compile(src)
		if iss.Err() != nil {
			t.Fatal(iss.Err())
		}
		prg, err := env.Program(ast, cel.EvalOptions(cel.OptOptimize), cel.CostLimit(limit), cel.InterruptCheckFrequency(InterruptEvery))
		if err != nil {
			t.Fatal(err)
		}
		return prg
	}
	cp := *c
	cp.Rules = append([]Rule(nil), c.Rules...)
	cp.Budgets = append([]Budget(nil), c.Budgets...)
	for i := range cp.Rules {
		if cp.Rules[i].When != nil {
			cp.Rules[i].When = build(cp.Rules[i].WhenSource)
		}
	}
	for i := range cp.Budgets {
		if cp.Budgets[i].Measure != nil {
			cp.Budgets[i].Measure = build(cp.Budgets[i].MeasureSource)
		}
	}
	return &cp
}

// TestCostLimitFailsClosed is DL-024's carry-over: over the runtime CEL cost limit is a deny with
// a clear reason, whatever the rule's decision, and never an allow.
func TestCostLimitFailsClosed(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, policy, rule string
		source             string
	}{
		{"allow rule", evalHead + "rules:\n  - id: all-positive\n    decision: allow\n    when: 'args.items.all(x, x > 0)'\n  - id: allow-all\n    decision: allow\n", "all-positive", SourceRule},
		{"deny rule", evalHead + "rules:\n  - id: no-negatives\n    decision: deny\n    reason: No negative items.\n    when: 'args.items.exists(x, x < 0)'\n  - id: allow-all\n    decision: allow\n", "no-negatives", SourceRule},
		{"approval rule", evalHead + "rules:\n  - id: review\n    decision: require_approval\n    reason: Review it.\n    when: 'args.items.exists(x, x < 0)'\n  - id: allow-all\n    decision: allow\n", "review", SourceRule},
		{"measure", evalHead + "budgets:\n  - id: items\n    window: day\n    max_spend: \"50.00 usd\"\n    measure: 'size(args.items.filter(x, x > 0))'\nrules:\n  - id: allow-all\n    decision: allow\n", "budget:items", SourceBudget},
	}
	for _, k := range cases {
		t.Run(k.name, func(t *testing.T) {
			c := withCostLimit(t, mustCompile(t, k.policy), 50)
			d := Evaluate(ctx, c, in("stripe.refund", bigList(200)), EvalOptions{Explain: true})
			if d.Outcome != Deny || d.Code != CodePolicyDenied || d.RuleID != k.rule || d.Source != k.source {
				t.Fatalf("got %s %s %s %s: %s", d.Outcome, d.Code, d.Source, d.RuleID, d.Message)
			}
			want := fmt.Sprintf(`Blocked by policy %q: Deedglass stopped evaluating this rule at its cost limit of 100,000, so the action did not run.`, k.rule)
			if !strings.HasPrefix(d.Message, want) || len(d.Errors) != 1 || len(d.Reservations) != 0 || len(d.CostBudgets) != 0 {
				t.Fatalf("%q %v", d.Message, d.Errors)
			}
			// Under the limit, the same policy decides normally.
			d = Evaluate(ctx, c, in("stripe.refund", bigList(2)), EvalOptions{})
			if k.name != "measure" && d.Outcome == Deny && d.Code == CodePolicyDenied && strings.Contains(d.Message, "cost limit") {
				t.Fatalf("a small input hit the limit: %s", d.Message)
			}
		})
	}
}

// TestHugeArgsNeverAllow: with the real limits, an agent that sends a huge list to a rule that
// iterates it gets a deny (the cost limit or the evaluation deadline), never an allow, and the
// evaluation stays bounded. cel-go's runtime cost tracking is quadratic in comprehension length,
// so for very long lists the 50 ms deadline is what stops it (docs/LIMITATIONS.md).
func TestHugeArgsNeverAllow(t *testing.T) {
	c := mustCompile(t, evalHead+"rules:\n  - id: all-positive\n    decision: allow\n    when: 'args.items.all(x, x > 0)'\n  - id: allow-all\n    decision: allow\n")
	for _, n := range []int{20_000, 60_000} {
		start := time.Now()
		d := Evaluate(context.Background(), c, in("stripe.refund", bigList(n)), EvalOptions{})
		took := time.Since(start)
		if d.Outcome != Deny {
			t.Fatalf("%d items: %s %s", n, d.Outcome, d.RuleID)
		}
		if took > 2*time.Second {
			t.Fatalf("%d items took %v", n, took)
		}
		if d.Code == CodePolicyUnavailable && (len(d.Errors) != 1 || !strings.Contains(d.Errors[0], "all-positive: stopped at the evaluation time limit")) {
			t.Fatalf("%v", d.Errors)
		}
	}
}

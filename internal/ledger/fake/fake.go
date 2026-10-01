// Package fake makes realistic actions for demos and tests (`deedglass dev fake-action`, S06): a
// deterministic plan from a seed, and a runner that walks every action through the real write path
// (ledger.CreateAction, Transition, SealAction, SealAmendment), so every screen, demo and clip
// shows real sealed deeds. Dev only: the CLI refuses to run it outside DEEDGLASS_ENV=dev.
//
// What is simulated: the policy decision, the tool's latency and result, and the Witness's
// verdict. Nothing calls a real tool. The executing_at timestamp is set back by the simulated
// latency so deeds show a realistic latency_ms; every other time is the database clock.
package fake

//deedglass:allow-math-rand the demo plan must be reproducible from a seed (PCG); it never makes a secret, an id or a token

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// Kind is one outcome the generator can produce.
type Kind string

const (
	Allow        Kind = "allow"        // allowed, executed, VERIFIED (L0 40%, L1 25%, L2 35%)
	Deny         Kind = "deny"         // denied by a rule
	Mismatch     Kind = "mismatch"     // L2 MISMATCH on postgres.execute
	Unverifiable Kind = "unverifiable" // the witness timed out
	Failed       Kind = "failed"       // the tool failed
	Pending      Kind = "pending"      // waiting for approval (left unsealed; S22 decides)
	Amend        Kind = "amend"        // sealed UNVERIFIABLE, later amended to VERIFIED
)

// Kinds lists every kind with its default weight (percent).
var Kinds = []struct {
	Kind   Kind
	Weight int
}{{Allow, 60}, {Deny, 12}, {Mismatch, 6}, {Unverifiable, 8}, {Failed, 6}, {Pending, 5}, {Amend, 3}}

// ParseKinds reads a comma list of kinds ("allow,deny"); "" means all of them.
func ParseKinds(s string) ([]Kind, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []Kind
	for _, x := range strings.Split(s, ",") {
		k := Kind(strings.TrimSpace(x))
		if !slices.ContainsFunc(Kinds, func(w struct {
			Kind   Kind
			Weight int
		}) bool {
			return w.Kind == k
		}) {
			return nil, fmt.Errorf("%q isn't a kind; use allow, deny, mismatch, unverifiable, failed, pending or amend", x)
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out, nil
}

// AgentNames are the demo agents, in order; --agents n uses the first n.
var AgentNames = []string{"support-bot", "billing-bot", "ops-bot", "research-bot", "sales-bot", "data-bot", "infra-bot", "docs-bot"}

// Tools are the canonical connector tool names S17, S29 and S30 ship, so screenshots never show a
// tool that won't exist.
var Tools = []string{"postgres.query", "postgres.execute", "stripe.create_refund", "github.create_issue", "github.merge_pr",
	"slack.post_message", "gmail.create_draft", "notion.create_page", "orders.get"}

// Plan is one action to make. Everything is decided here, from the seed; ids and times are real.
type Plan struct {
	Kind       Kind
	Agent      int // index into the run's agents
	Tool       string
	Args       json.RawMessage
	OnBehalfOf string
	Session    string
	RuleID     string // the deciding rule (allow rules too)
	LatencyMS  int64
	ErrorCode  string // failed: upstream_error | timeout
	Level      string // L0 | L1 | L2 (witnessed kinds)
	Verdict    string // VERIFIED | MISMATCH | UNVERIFIABLE
	Method     string
	Detail     json.RawMessage // the witness's stored diff, when there is one
	Result     json.RawMessage // the tool result, when it ran
}

// Generate returns n plans for seed. The same seed, n, kinds and agents always give the same
// plans (math/rand/v2 PCG).
func Generate(seed uint64, n int, kinds []Kind, agents int) []Plan {
	r := rand.New(rand.NewPCG(seed, seed^0x6465656467617373)) // "deedglass"
	if agents < 1 {
		agents = 1
	}
	var pool []struct {
		Kind   Kind
		Weight int
	}
	for _, k := range Kinds {
		if len(kinds) == 0 || slices.Contains(kinds, k.Kind) {
			pool = append(pool, k)
		}
	}
	total := 0
	for _, k := range pool {
		total += k.Weight
	}
	out := make([]Plan, 0, n)
	for range n {
		x := r.IntN(total)
		kind := pool[len(pool)-1].Kind
		for _, k := range pool {
			if x < k.Weight {
				kind = k.Kind
				break
			}
			x -= k.Weight
		}
		out = append(out, plan(r, kind, agents))
	}
	return out
}

func pick[T any](r *rand.Rand, xs ...T) T { return xs[r.IntN(len(xs))] }

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // plain values only
	}
	return b
}

func plan(r *rand.Rand, kind Kind, agents int) Plan {
	p := Plan{Kind: kind, Agent: r.IntN(agents),
		OnBehalfOf: fmt.Sprintf("u_%04d", 1000+r.IntN(9000)), Session: fmt.Sprintf("chat_%02d", 10+r.IntN(90))}
	switch kind {
	case Allow:
		switch x := r.IntN(100); {
		case x < 40:
			p.Level, p.Tool, p.Method = "L0", pick(r, "slack.post_message", "gmail.create_draft", "orders.get", "notion.create_page"), "response.schema"
		case x < 65:
			p.Level, p.Tool = "L1", pick(r, "github.create_issue", "stripe.create_refund", "github.merge_pr", "notion.create_page")
			p.Method = "readback." + p.Tool
		default:
			p.Level, p.Tool, p.Method = "L2", pick(r, "postgres.query", "postgres.execute"), "postgres.pgoutput"
		}
		p.Verdict, p.RuleID = "VERIFIED", allowRule(p.Tool)
	case Deny:
		switch pick(r, "block-destructive-sql", "block-destructive-sql", "refund-limit", "budget") {
		case "block-destructive-sql":
			p.Tool, p.RuleID = "postgres.execute", "block-destructive-sql"
			p.Args = raw(map[string]any{"sql": pick(r, "DROP TABLE public.orders", "DELETE FROM public.customers", "TRUNCATE public.invoices")})
		case "refund-limit":
			p.Tool, p.RuleID = "stripe.create_refund", "refund-limit"
			p.Args = raw(map[string]any{"charge": fmt.Sprintf("ch_%06d", r.IntN(1_000_000)), "amount": 5000 + 100*r.IntN(500), "currency": "usd"})
		default:
			p.Tool, p.RuleID = pick(r, Tools...), "budget"
		}
	case Mismatch:
		p.Tool, p.Level, p.Verdict, p.Method, p.RuleID = "postgres.execute", "L2", "MISMATCH", "postgres.pgoutput", "allow-order-updates"
		p.Args = raw(map[string]any{"sql": "UPDATE public.orders SET status = $1 WHERE id = $2", "params": []any{"shipped", 4000 + r.IntN(1000)}})
		p.Detail = fakeDetail(p.Level, p.Verdict, p.Method, "row_count_mismatch", "violation", "row.count",
			"Postgres changed 301 rows in public.orders. The agent reported 1.")
		p.Result = raw(map[string]any{"rows_affected": 1})
	case Unverifiable, Amend:
		p.Tool = pick(r, "postgres.execute", "github.merge_pr", "stripe.create_refund")
		p.Level, p.Verdict, p.RuleID = "L1", "UNVERIFIABLE", allowRule(p.Tool)
		p.Method = "readback." + p.Tool
		if p.Tool == "postgres.execute" {
			p.Level, p.Method = "L2", "postgres.pgoutput"
		}
		code, msg := "readback_timeout", "The read-back took longer than 10 s."
		if p.Level == "L2" {
			code, msg = "l2_timeout", "No evidence arrived before the time limit."
		}
		p.Detail = fakeDetail(p.Level, p.Verdict, p.Method, code, "gap", "readback", msg)
	case Failed:
		p.Tool, p.RuleID, p.ErrorCode = pick(r, Tools...), "", pick(r, "upstream_error", "upstream_error", "timeout")
		p.RuleID = allowRule(p.Tool)
	case Pending:
		p.Tool, p.RuleID = "stripe.create_refund", "refund-over-100"
		p.Args = raw(map[string]any{"charge": fmt.Sprintf("ch_%06d", r.IntN(1_000_000)), "amount": 10000 + 100*r.IntN(400), "currency": "usd"})
	}
	if p.Args == nil {
		p.Args = args(r, p.Tool)
	}
	if kind != Deny && kind != Pending {
		p.LatencyMS = latency(r, p.Tool)
		if p.Result == nil && kind != Failed {
			p.Result = raw(map[string]any{"ok": true})
		}
	}
	return p
}

// fakeDetail is a witness_detail v1 (S19) for the demo data: the claim, one reason and its
// sentence. The L2 codes are the ones S21 registers; the demo shows their shape early.
func fakeDetail(level, verdict, method, code, class, check, msg string) json.RawMessage {
	lv := map[string]any{"L0": map[string]any{"verdict": "VERIFIED", "method": "reported"}, "L1": nil, "L2": nil}
	lv[level] = map[string]any{"verdict": verdict, "method": method}
	return raw(map[string]any{"object": "witness_detail", "version": 1,
		"claim":   map[string]any{"level": level, "verdict": verdict, "method": method, "required_level": "L0"},
		"summary": msg, "levels": lv,
		"reasons": []any{
			map[string]any{"code": "tool_reported_success", "class": "pass", "level": "L0", "check": "status", "message": "The tool reported success."},
			map[string]any{"code": code, "class": class, "level": level, "check": check, "message": msg},
		},
		"witnessed_at": "2026-10-01T09:00:00.000Z"})
}

func allowRule(tool string) string {
	switch {
	case strings.HasPrefix(tool, "postgres."):
		return "allow-order-updates"
	case strings.HasPrefix(tool, "stripe."):
		return "refunds-under-100"
	}
	return "allow-" + strings.SplitN(tool, ".", 2)[0]
}

func latency(r *rand.Rand, tool string) int64 {
	switch {
	case tool == "postgres.query":
		return int64(3 + r.IntN(15))
	case tool == "postgres.execute":
		return int64(20 + r.IntN(60))
	case strings.HasPrefix(tool, "stripe."):
		return int64(180 + r.IntN(320))
	case strings.HasPrefix(tool, "github."):
		return int64(150 + r.IntN(250))
	}
	return int64(60 + r.IntN(200))
}

func args(r *rand.Rand, tool string) json.RawMessage {
	switch tool {
	case "postgres.query":
		return raw(map[string]any{"sql": "SELECT id, status FROM public.orders WHERE customer_id = $1", "params": []any{r.IntN(10000)}})
	case "postgres.execute":
		return raw(map[string]any{"sql": "UPDATE public.orders SET status = $1 WHERE id = $2", "params": []any{"shipped", 4000 + r.IntN(1000)}})
	case "stripe.create_refund":
		return raw(map[string]any{"charge": fmt.Sprintf("ch_%06d", r.IntN(1_000_000)), "amount": 500 + 100*r.IntN(90), "currency": "usd"})
	case "github.create_issue":
		return raw(map[string]any{"repo": "acme/web", "title": pick(r, "Checkout times out on Safari", "Refund email has the wrong total", "Flaky test in orders_spec")})
	case "github.merge_pr":
		return raw(map[string]any{"repo": "acme/web", "number": 100 + r.IntN(900)})
	case "slack.post_message":
		return raw(map[string]any{"channel": pick(r, "#support", "#ops", "#billing"), "text": "Order status updated."})
	case "gmail.create_draft":
		return raw(map[string]any{"to": "customer@example.test", "subject": "About your order"})
	case "notion.create_page":
		return raw(map[string]any{"parent": "Runbooks", "title": "Incident notes"})
	}
	return raw(map[string]any{"id": 4000 + r.IntN(1000)})
}

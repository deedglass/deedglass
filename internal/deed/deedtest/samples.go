package deedtest

import (
	"fmt"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

func ptr[T any](v T) *T { return &v }

// digestOf is the payload digest of a JSON literal (DigestJSON, plain JCS).
func digestOf(js string) string {
	d, err := deed.DigestJSON([]byte(js))
	if err != nil {
		panic(err)
	}
	return d
}

// Snapshot returns a realistic action snapshot: an agent's UPDATE through the Postgres connector,
// allowed by policy, executed in 41 ms and witnessed at L2. n varies the ids.
func Snapshot(org string, n int) deed.ActionSnapshot {
	start := T0.Add(time.Duration(n-1) * time.Second).Add(-200 * time.Millisecond)
	return deed.ActionSnapshot{
		Org:           org,
		Agent:         deed.AgentRef{ID: ID("agt", 1), Name: "billing-agent"},
		OnBehalfOf:    ptr("usr_ref:priya"),
		Session:       ptr("ses-7f3a"),
		ActionID:      ID("act", n),
		Tool:          "postgres.execute",
		Connector:     ptr(ID("con", 1)),
		Mode:          "gateway",
		RequestDigest: digestOf(fmt.Sprintf(`{"sql":"UPDATE public.orders SET status = $1 WHERE id = $2","params":["refunded",%d]}`, 1000+n)),
		Policy: deed.PolicySection{ID: ptr(ID("pol", 1)), VersionID: ptr(ID("pv", 1)), VersionHash: ptr(digestOf(`{"rules":["allow-billing-updates"]}`)),
			Mode: "enforce", Decision: "allow", RuleID: ptr("allow-billing-updates")},
		Execution: &deed.ExecutionSnapshot{Status: "succeeded", StartedAt: start, FinishedAt: start.Add(41 * time.Millisecond),
			ResultDigest: ptr(digestOf(`{"rows_affected":1}`))},
		Witness: &deed.WitnessSection{Level: "L2", Verdict: "VERIFIED", Method: "postgres.pgoutput", DetailDigest: ptr(digestOf(`{"table":"public.orders","rows":1}`))},
		Cost:    &deed.Cost{Amount: 120, Currency: "usd"},
	}
}

func mustDraft(d deed.Draft, err error) deed.Body {
	if err != nil {
		panic(err)
	}
	return d.Body
}

// ActionVerified is an action deed draft, executed and witnessed L2 VERIFIED.
func ActionVerified(org string, n int) deed.Body { return mustDraft(deed.ForAction(Snapshot(org, n))) }

// ActionDenied is a policy deny: never executed, no witness, no cost.
func ActionDenied(org string, n int) deed.Body {
	s := Snapshot(org, n)
	s.Tool = "postgres.execute"
	s.RequestDigest = digestOf(`{"sql":"DROP TABLE public.orders"}`)
	s.Policy.Decision, s.Policy.RuleID = "deny", ptr("block-destructive-sql")
	s.Execution, s.Witness, s.Cost = nil, nil, nil
	return mustDraft(deed.ForAction(s))
}

// ActionApproved needed a person: approved, then executed and witnessed L1.
func ActionApproved(org string, n int) deed.Body {
	s := Snapshot(org, n)
	s.Tool = "stripe.refunds.create"
	s.Connector = ptr(ID("con", 2))
	s.RequestDigest = digestOf(`{"charge":"ch_3Q","amount":12000,"currency":"usd"}`)
	s.IntentHash, s.IntentSigned = ptr(digestOf(`{"intent":"refund ch_3Q"}`)), true
	s.Policy.Decision, s.Policy.RuleID = "require_approval", ptr("refunds-over-100")
	at := T0.Add(time.Duration(n-1)*time.Second - 2*time.Second)
	s.Approval = &deed.ApprovalSnapshot{ID: ID("apr", n), Outcome: "approved", DecidedBy: []string{ID("usr", 2), ID("usr", 1)}, DecidedAt: &at}
	s.Witness = &deed.WitnessSection{Level: "L1", Verdict: "VERIFIED", Method: "readback.stripe.get_refund"}
	s.Cost = &deed.Cost{Amount: 12000, Currency: "usd"}
	return mustDraft(deed.ForAction(s))
}

// AdminStopped records a person stopping an agent.
func AdminStopped(org string, n int) deed.Body {
	return mustDraft(deed.ForAdmin(deed.AdminSnapshot{
		Org: org, Agent: &deed.AgentRef{ID: ID("agt", 1), Name: "billing-agent"},
		Event: ID("adm", n), Action: "agent.stopped",
		Actor:  deed.Ref{Type: "user", ID: ID("usr", 1)},
		Target: &deed.Ref{Type: "agent", ID: ID("agt", 1)},
		Reason: ptr("Refund volume looked wrong; stopping until we check."),
	}))
}

// Amendment is a later L2 MISMATCH about original.
func Amendment(original *deed.Body) deed.Body {
	return mustDraft(deed.ForAmendment(original, deed.WitnessSection{Level: "L2", Verdict: "MISMATCH", Method: "postgres.pgoutput",
		DetailDigest: ptr(digestOf(`{"table":"public.orders","expected_rows":1,"observed_rows":301}`))}))
}

// UnicodeAgent is an action whose agent name exercises JCS string escaping.
func UnicodeAgent(org string, n int) deed.Body {
	b := ActionVerified(org, n)
	b.Agent.Name = "Priya’s agent — ☕"
	return b
}

//go:build attack

package attack

import (
	"github.com/deedglass/deedglass/attack/harness"
)

// Witness L2 cases H08–H11 (S18 declares them; S21's witness makes them observable). Their
// fixtures are attack/fixtures/sql/witness_h08_h11.sql. Until S21 lands each reports
// "waiting on S21": never matched, never differs, never counted as passed.
//
// S21: remove `needs` from H08–H11 in attack_meta.json (outside the digest, no ADR), load the
// fixture file into h.Target before each case, run the attack below, and observe the verdict on
// the action (witness.level L2, witness.verdict) and the alerts.

func witnessCase(id string, attack string) harness.Case {
	return harness.Case{ID: id, Run: func(h *harness.H) harness.Observed {
		h.RequireWitness("S21")
		// Reached only once the decoder route exists (S20) without S21's observation code.
		return harness.Observed{Outcome: "NOT OBSERVED", Detail: "S21 implements: " + attack}
	}}
}

func init() {
	register(
		witnessCase("H08", "UPDATE public.orders SET status = 'shipped' WHERE id = 1 (the trigger touches 300 order_events rows)"),
		witnessCase("H09", "UPDATE public.orders SET total_cents = total_cents + 1 WHERE id = 2 (the trigger sets internal_note)"),
		witnessCase("H10", "a second session emits pg_logical_emit_message(true, 'deedglass', '<a real act_ id>') with no ledger row"),
		witnessCase("H11", "ALTER TABLE public.customers REPLICA IDENTITY DEFAULT, then UPDATE public.customers SET note = 'x' WHERE id = 3"),
	)
}

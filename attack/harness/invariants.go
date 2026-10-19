//go:build attack

package harness

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// sweepTables are the control-plane columns the sentinel sweep reads (S18 §3.5 invariant 4):
// every recorded action and event, every deed and admin event, connector configs and credential
// hints. A credential's ciphertext is encrypted, so it is never swept.
var sweepTables = []struct{ table, expr string }{
	{"actions", "t::text"},
	{"action_events", "t::text"},
	{"deeds", "t::text"},
	{"admin_events", "t::text"},
	{"connectors", "t.config::text || coalesce(t.health::text, '') || t.name || coalesce(t.description, '')"},
	{"credentials", "coalesce(t.hint, '')"},
}

// checkInvariants runs the universal invariants after a case. Each broken one is a problem.
func (h *H) checkInvariants(obs Observed) []string {
	var p []string
	// 1. Chains: both orgs' deed chains are intact.
	for _, o := range []*Org{h.Acme, h.Globex} {
		c := o.Admin.Do("GET", "/v1/ledger/check", nil).Map()
		if Str(c, "status") != "intact" {
			p = append(p, fmt.Sprintf("invariant 1: %s's chain is %q (%v)", o.Slug, Str(c, "status"), Get(c, "break")))
		}
	}
	// 2. Target: nothing changed when nothing should have run.
	switch obs.Outcome {
	case "DENIED", "REJECTED", "BLOCKED", "REDACTED":
		if !obs.TargetMayChange {
			if fp := h.Target.Fingerprint(); fp != h.Target.Before {
				p = append(p, "invariant 2: the target changed ("+h.Target.Before+" → "+fp+")")
			}
			if n := h.Target.LedgerRows(); n != h.Target.Ledger {
				p = append(p, fmt.Sprintf("invariant 2: deedglass.ledger gained %d rows", n-h.Target.Ledger))
			}
		}
	}
	// 3. Terminal states: every action the case made is sealed within 5 s.
	for _, id := range obs.ActionIDs {
		org := h.Acme
		if a := h.Action(org, id); Str(a, "id") == "" {
			org = h.Globex
			// HC-P02's I11 acts in an org of its own (PHI mode on): look the action up in every org.
			for _, o := range h.Env.Orgs {
				if o != h.Acme && Str(h.Action(o, id), "id") != "" {
					org = o
					break
				}
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			a := h.Action(org, id)
			if Str(a, "state") == "sealed" {
				break
			}
			if time.Now().After(deadline) {
				p = append(p, fmt.Sprintf("invariant 3: %s is %s, not sealed", id, Str(a, "state")))
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	// 4. Secrets: the sentinel appears nowhere.
	if n, where := h.Env.SentinelHits(); n > 0 {
		p = append(p, fmt.Sprintf("invariant 4: the sentinel appeared %d times (%s)", n, where))
	}
	// 5. Forbidden listeners saw nothing.
	for _, name := range obs.Forbidden {
		if d := h.Up.Hits(name) - h.hits0[name]; d != 0 {
			p = append(p, fmt.Sprintf("invariant 5: %s was hit %d times", name, d))
		}
	}
	return p
}

// HitsSince is how many requests an upstream saw since the case started.
func (h *H) HitsSince(name string) int64 { return h.Up.Hits(name) - h.hits0[name] }

// SentinelHits sweeps the captured logs, every response body, and the control-plane tables.
func (e *Env) SentinelHits() (int, string) {
	total, where := 0, ""
	add := func(n int, w string) {
		if n > 0 {
			total += n
			where += fmt.Sprintf("%s: %d; ", w, n)
		}
	}
	add(CountSentinel(e.Logs.Bytes(), e.Sentinel), "server logs")
	nb := 0
	for _, b := range e.Bodies.Snapshot() {
		nb += CountSentinel(b, e.Sentinel)
	}
	add(nb, "HTTP bodies")
	for _, st := range sweepTables {
		n, _ := e.sweepTable(st.table, st.expr)
		add(n, st.table)
	}
	return total, where
}

// RowsScanned is the number of control-plane rows the sweep reads.
func (e *Env) RowsScanned() int {
	total := 0
	for _, st := range sweepTables {
		_, rows := e.sweepTable(st.table, st.expr)
		total += rows
	}
	return total
}

func (e *Env) sweepTable(table, expr string) (hits, rows int) {
	q := fmt.Sprintf("SELECT count(*) FILTER (WHERE strpos(%[1]s, $1) > 0 OR strpos(%[1]s, $2) > 0 OR strpos(%[1]s, $3) > 0)::text, count(*)::text FROM deedglass.%[2]s AS t", expr, table)
	forms := SentinelForms(e.Sentinel)
	for len(forms) < 3 {
		forms = append(forms, forms[0])
	}
	r := e.CPQuery(q, string(forms[0]), string(forms[2]), string(forms[3%len(forms)]))
	if len(r) == 1 {
		_, _ = fmt.Sscan(r[0][0], &hits)
		_, _ = fmt.Sscan(r[0][1], &rows)
	}
	return hits, rows
}

// InvariantSelfTest shows the invariants can fail (S18 §6): a planted secret in a log line breaks
// invariant 4, and a change to the target in a DENIED case breaks invariant 2. It plants a
// different sentinel than the run's, so the run's own sweep stays clean.
func (e *Env) InvariantSelfTest(t *testing.T) {
	h := e.newH(t, "selftest")
	real := e.Sentinel
	planted := NewSentinel()
	e.Sentinel = planted
	_, _ = e.Logs.Write([]byte("level=INFO msg=\"a deliberately planted secret for the self-test\" value=" + planted + "\n"))
	p := h.checkInvariants(Observed{Outcome: "DENIED"})
	e.Sentinel = real
	if !containsPrefix(p, "invariant 4") {
		t.Errorf("a planted secret in a log line didn't break invariant 4: %v", p)
	}
	c := h.Target.Conn()
	_, err := c.Exec(context.Background(), "UPDATE public.orders SET total_cents = total_cents + 1 WHERE id = 1")
	c.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p = h.checkInvariants(Observed{Outcome: "DENIED"})
	if !containsPrefix(p, "invariant 2") {
		t.Errorf("a changed target in a DENIED case didn't break invariant 2: %v", p)
	}
	if containsPrefix(p, "invariant 4") {
		t.Errorf("the run's own sentinel was seen: %v", p)
	}
}

func containsPrefix(ps []string, prefix string) bool {
	for _, p := range ps {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

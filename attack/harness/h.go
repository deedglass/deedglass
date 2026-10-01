//go:build attack

package harness

//deedglass:allow-sql-build test harness: database and table names are generated here or fixed, and quoted with pgx.Identifier.Sanitize; every value is a $n parameter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// H is what a case works with: the clients, this case's target database, the upstreams and the
// observers. Every case gets its own target clone.
type H struct {
	T        testing.TB
	Env      *Env
	Acme     *Org
	Globex   *Org
	Up       *Upstreams
	Target   *Target
	Sentinel string

	caseID   string
	problems []string
	hits0    map[string]int64
	logs0    int
	bodies0  int
}

// Target is one case's clone of the target database.
type Target struct {
	Name   string
	DSN    string // the agent role's DSN on this clone
	Before string // the fingerprint when the case started
	Ledger int    // deedglass.ledger rows when the case started
	env    *Env
}

func (e *Env) newH(t testing.TB, id string) *H {
	h := &H{T: t, Env: e, Acme: e.Orgs["acme"], Globex: e.Orgs["globex"], Up: e.Up, Sentinel: e.Sentinel, caseID: id,
		hits0: map[string]int64{}}
	for _, n := range []string{"orders", "redirector", "other", "secret-echo", "mcp"} {
		h.hits0[n] = e.Up.Hits(n)
	}
	h.Target = e.cloneTarget(t, strings.ToLower(id))
	h.logs0, h.bodies0 = len(e.Logs.Bytes()), len(e.Bodies.Snapshot())
	return h
}

// cloneTarget makes case_<id> from the template and points the acme postgres connector at it.
func (e *Env) cloneTarget(t testing.TB, id string) *Target {
	name := "case_" + id
	e.dropDB(name)
	c := e.adminConn("postgres")
	_, err := c.Exec(context.Background(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE attack_target_tpl")
	c.Close(context.Background())
	if err != nil {
		t.Fatalf("clone the target: %v", err)
	}
	t.Cleanup(func() { e.dropDB(name) })
	tg := &Target{Name: name, DSN: withDB(e.Target.AgentDSN, name), env: e}
	secret := map[string]string{"dsn": tg.DSN}
	if e.Target.WitnessDSN != "" {
		secret["witness_dsn"] = withDB(e.Target.WitnessDSN, name)
	}
	acme := e.Orgs["acme"]
	r := acme.Admin.Do("PUT", "/v1/connectors/"+acme.Conns["postgres"]+"/credentials", map[string]any{"kind": "dsn", "secret": secret})
	if r.Status != 200 && r.Status != 201 {
		t.Fatalf("point the connector at %s: HTTP %d %s", name, r.Status, r.Body)
	}
	tg.Before, tg.Ledger = tg.Fingerprint(), tg.LedgerRows()
	return tg
}

// Conn opens a superuser connection to this clone.
func (tg *Target) Conn() *pgx.Conn { return tg.env.adminConn(tg.Name) }

// Fingerprint is sha256 over every watched table's rows in key order, plus the table list (a
// dropped table changes it too).
func (tg *Target) Fingerprint() string {
	c := tg.Conn()
	defer c.Close(context.Background())
	h := sha256.New()
	for _, tbl := range tg.env.Target.Tables {
		var exists bool
		_ = c.QueryRow(context.Background(), "SELECT pg_catalog.to_regclass($1) IS NOT NULL", tbl).Scan(&exists)
		fmt.Fprintf(h, "table %s exists=%t\n", tbl, exists)
		if !exists {
			continue
		}
		rows, err := c.Query(context.Background(), "SELECT t::text FROM "+pgx.Identifier(strings.Split(tbl, ".")).Sanitize()+" AS t ORDER BY 1")
		if err != nil {
			fmt.Fprintf(h, "error %v\n", err)
			continue
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			h.Write([]byte(s + "\n"))
		}
		rows.Close()
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// LedgerRows counts deedglass.ledger on this clone.
func (tg *Target) LedgerRows() int {
	c := tg.Conn()
	defer c.Close(context.Background())
	var n int
	_ = c.QueryRow(context.Background(), "SELECT count(*) FROM deedglass.ledger").Scan(&n)
	return n
}

// TableExists reports whether a table exists on this clone.
func (tg *Target) TableExists(tbl string) bool {
	c := tg.Conn()
	defer c.Close(context.Background())
	var ok bool
	_ = c.QueryRow(context.Background(), "SELECT pg_catalog.to_regclass($1) IS NOT NULL", tbl).Scan(&ok)
	return ok
}

// Problem records a problem that fails the case whatever its headline.
func (h *H) Problem(format string, args ...any) {
	h.problems = append(h.problems, fmt.Sprintf(format, args...))
}

// RequireWitness stops a case that needs S21's L2 witness. Until S21 lands there is no witness
// to observe, so the case reports "waiting on S21" (never matched, never differs).
func (h *H) RequireWitness(session string) {
	if !h.WitnessAvailable() {
		panic(errWaiting{session: session})
	}
}

// WitnessAvailable probes for the L2 witness: S20/S21 add the decoder route.
func (h *H) WitnessAvailable() bool {
	r := h.Acme.Admin.Do("GET", "/v1/connectors/"+h.Acme.Conns["postgres"]+"/decoder", nil)
	return r.Status == 200
}

// Action fetches an action (as the org admin), waiting up to wait seconds for it to settle.
func (h *H) Action(o *Org, id string) map[string]any {
	return o.Admin.Do("GET", "/v1/actions/"+id+"?wait=5", nil).Map()
}

// WaitSealed polls until the action is sealed (5 s) and returns it.
func (h *H) WaitSealed(o *Org, id string) map[string]any {
	deadline := time.Now().Add(5 * time.Second)
	for {
		a := h.Action(o, id)
		if Str(a, "state") == "sealed" || time.Now().After(deadline) {
			return a
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Deed fetches a deed's body (as the org admin).
func (h *H) Deed(o *Org, id string) map[string]any {
	d := o.Admin.Do("GET", "/v1/deeds/"+id, nil).Map()
	if b, ok := d["body"].(map[string]any); ok {
		return b
	}
	return d
}

// Act sends POST /v1/actions as agent and returns the response and the action id (from the body
// or from error.action).
func (h *H) Act(a *Agent, tool string, args any, hdr ...string) (Response, string) {
	r := a.API.Do("POST", "/v1/actions", map[string]any{"tool": tool, "args": args, "mode": "gateway", "wait": 10}, hdr...)
	m := r.Map()
	id := Str(m, "id")
	if id == "" {
		id = Str(m, "error", "action", "id")
	}
	return r, id
}

// DeniedBy checks an action the Gate refused: sealed, decision deny with ruleID (a rule id, or
// "default" for the policy's default), a deed whose body says the same, and no execution. It
// returns "" or what differed.
func (h *H) DeniedBy(o *Org, actionID, ruleID string) string {
	if actionID == "" {
		return "no action was recorded"
	}
	a := h.WaitSealed(o, actionID)
	got := Str(a, "decision", "rule_id")
	if ruleID == "default" {
		got = Str(a, "decision", "source")
	}
	switch {
	case Str(a, "state") != "sealed":
		return "state " + Str(a, "state")
	case Str(a, "decision", "outcome") != "deny":
		return "decision " + Str(a, "decision", "outcome")
	case got != ruleID:
		return fmt.Sprintf("rule %q (source %q)", Str(a, "decision", "rule_id"), Str(a, "decision", "source"))
	case Get(a, "execution", "started_at") != nil:
		return "it started executing"
	}
	deed := Str(a, "deed")
	if deed == "" {
		return "no deed"
	}
	body := h.Deed(o, deed)
	if Str(body, "policy", "decision") != "deny" {
		return "the deed's decision is " + Str(body, "policy", "decision")
	}
	return ""
}

// FailedWith checks an action that ran into a refusal: sealed, execution failed with code.
func (h *H) FailedWith(o *Org, actionID, code string) string {
	if actionID == "" {
		return "no action was recorded"
	}
	a := h.WaitSealed(o, actionID)
	switch {
	case Str(a, "state") != "sealed":
		return "state " + Str(a, "state")
	case Str(a, "execution", "status") != "failed":
		return "execution " + Str(a, "execution", "status")
	case Str(a, "execution", "error_code") != code:
		return "error code " + Str(a, "execution", "error_code")
	}
	return ""
}

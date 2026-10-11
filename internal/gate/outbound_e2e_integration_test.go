//go:build integration && outboundmail

package gate

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/outbound"
	"github.com/deedglass/deedglass/internal/connector/outbound/fakegmail"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// S40 end to end on Postgres: mail.send through the Gate (gateway mode) to the ops build's
// outbound_mail connector and a fake Gmail; the approval path with a schedule (mode B) and the
// automatic path (mode A); each sealed with Gmail's Sent copy read back at L1.

const mailPolicy = `deedglass: policy/v1
name: mail-e2e
description: S40's two modes on one tool.

defaults:
  decision: deny
  reason: No rule allows this action.

budgets:
  - id: mailbox-day
    match: {tool: mail.send}
    window: day
    max_calls: 5

rules:
  - id: never-from-deedglass-com
    match: {tool: mail.send}
    when: '!request.mail.enriched || request.mail.from_domain == "deedglass.com" || request.mail.from_domain.endsWith(".deedglass.com")'
    decision: deny
    reason: Cold email never leaves from deedglass.com.
  - id: first-touch-has-no-link
    match: {tool: mail.send}
    when: args.step == 1 && (request.mail.links > 0 || request.mail.bare_domains > 0)
    decision: deny
    reason: The first email carries no link.
  - id: lane-auto-outreach
    match: {tool: mail.send}
    when: has(args.checks.auto) && args.checks.auto == true
    decision: allow
    verify: L1
  - id: send-needs-countersign
    match: {tool: mail.send}
    decision: require_approval
    reason: One tap per email.
    approval: {approvers: [role:owner], required: 1, expires_in: 24h}
    notify: []
    verify: L1
`

type lateResolver struct{ r *connector.Registry }

func (l *lateResolver) Resolve(ctx context.Context, org ids.OrgID, tool string) (Executor, connector.ToolSpec, string, error) {
	return l.r.Resolve(ctx, org, tool)
}

type rowLoader struct{ row store.Connector }

func (m rowLoader) Head(_ context.Context, _ ids.OrgID, name string) (store.ConnectorHead, error) {
	if name != m.row.Name {
		return store.ConnectorHead{}, store.ErrNotFound
	}
	return store.ConnectorHead{ID: m.row.ID, Type: m.row.Type, Status: m.row.Status, ConfigHash: m.row.ConfigHash}, nil
}
func (m rowLoader) Load(_ context.Context, _ ids.OrgID, id string) (store.Connector, error) {
	return m.row, nil
}

type credOpener struct{}

func (credOpener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	return vault.NewSecretForTest(vault.KindFields, map[string]string{"client_id": "c", "client_secret": "s", "refresh_token": "r"}), nil
}

func mailWorld(t *testing.T) (*world, *fakegmail.Server) {
	fake := fakegmail.New("arjun@trydeedglass.example")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	late := &lateResolver{}
	w := newWorld(t, worldOpts{policy: mailPolicy, resolver: late})
	typ := outbound.New(outbound.Options{APIBase: srv.URL})
	env := connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	cfg, errs := typ.Validate(context.Background(), env, json.RawMessage(`{"mailbox":"arjun@trydeedglass.example","from_name":"Arjun Prabhakaran",
	  "sending_domain":"trydeedglass.example","deny_from_domains":["deedglass.com"],"unsubscribe_base":"https://trydeedglass.example/u/"}`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	row := store.Connector{ID: ids.New[ids.ConnectorKind]().String(), OrgID: w.org.String(), Type: outbound.Type, Name: "mail", Config: cfg,
		ConfigHash: "sha256:e2e", Status: "active"}
	late.r = connector.New(connector.Options{Types: []connector.Connector{typ}, Loader: rowLoader{row}, Opener: credOpener{}, Env: env,
		OrgTypes: map[string][]string{outbound.Type: {w.org.String()}}})
	return w, fake
}

func mailArgs(nb, na time.Time, auto bool) string {
	body := "Hi Maya,\n\nYour jobs post says the support agent updates order records in Postgres.\n\nWould anyone see it?\n\nArjun Prabhakaran\n"
	b, _ := json.Marshal(map[string]any{"to": "maya@acme.example", "subject": "your agent and Postgres", "body_text": body, "body_sha256": outbound.BodySHA256(body),
		"step": 1, "unsubscribe": map[string]any{"token": "k3x7abcdefghijklmnopqrstuvwxyz23"}, "message_ref": "msg_e2e",
		"window": map[string]any{"not_before": nb.UTC().Format(time.RFC3339), "not_after": na.UTC().Format(time.RFC3339)},
		"checks": map[string]any{"auto": auto}})
	return string(b)
}

func TestOutboundModeBApproveThenWindow(t *testing.T) {
	w, fake := mailWorld(t)
	nb := time.Now().Add(2 * time.Second).UTC().Truncate(time.Second)
	na := nb.Add(6 * time.Minute)
	a := w.must(0, scheduled("mail.send", mailArgs(nb, na, false), nb, na))
	if a.Status != "pending_approval" || fake.Sends() != 0 {
		t.Fatalf("%s", mustJSON(a))
	}
	var notify int
	w.adminRow(`SELECT count(*) FROM deedglass.witness_jobs WHERE action_id = $1 AND kind = 'approval_notify'`, []any{a.ID}, &notify)
	if notify != 0 {
		t.Fatal("notify: [] enqueued an email")
	}
	w.approve(a.ID, time.Now())
	if r, _ := w.p.Resume(bg, w.org, mustAction(a.ID)); r.State != "pending_approval" || fake.Sends() != 0 {
		t.Fatalf("ran before not_before: %s", mustJSON(r))
	}
	time.Sleep(time.Until(nb) + 200*time.Millisecond)
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "sealed" || r.Status != "executed" || fake.Sends() != 1 || fake.Writes() != 1 {
		t.Fatalf("%s sends %d", mustJSON(r), fake.Sends())
	}
	if r.Witness == nil || r.Witness.Level != "L1" || r.Witness.Verdict != "VERIFIED" {
		t.Fatalf("witness %s", mustJSON(r.Witness))
	}
	b := w.deedOf(a.ID).Body
	if b.Approval == nil || b.Approval.Outcome != "approved" || b.Witness.Verdict != "VERIFIED" {
		t.Fatalf("%s", mustJSON(b))
	}
	raw := string(fake.Messages()[0].Raw)
	if !strings.Contains(raw, "List-Unsubscribe-Post: List-Unsubscribe=One-Click") || !strings.Contains(raw, "From: \"Arjun Prabhakaran\" <arjun@trydeedglass.example>") {
		t.Fatal(raw)
	}
}

func TestOutboundModeAAutomatic(t *testing.T) {
	w, fake := mailWorld(t)
	now := time.Now().UTC().Truncate(time.Second)
	a := w.must(0, scheduled("mail.send", mailArgs(now.Add(-time.Second), now.Add(6*time.Minute), true), now, now.Add(6*time.Minute)))
	if a.Status != "executed" || fake.Sends() != 1 || a.Witness == nil || a.Witness.Verdict != "VERIFIED" {
		t.Fatalf("%s", mustJSON(a))
	}
	// An allowed send with a later window would run early: refused and sealed.
	if _, err := w.submit(0, scheduled("mail.send", mailArgs(now, now.Add(3*time.Hour), true), now.Add(2*time.Hour), now.Add(3*time.Hour))); err == nil {
		t.Fatal("schedule_needs_approval")
	}
	// The policy reads the connector's own counts: a link in a first email is refused, 0 requests.
	body := "Hi, see https://deedglass.com/play now?\n"
	var m map[string]any
	_ = json.Unmarshal([]byte(mailArgs(now, now.Add(6*time.Minute), true)), &m)
	m["body_text"], m["body_sha256"] = body, outbound.BodySHA256(body)
	bb, _ := json.Marshal(m)
	if _, err := w.submit(0, scheduled("mail.send", string(bb), now, now.Add(6*time.Minute))); err == nil || fake.Sends() != 1 {
		t.Fatalf("a link in the first email: %v", err)
	}
	// A tampered Sent copy is a MISMATCH.
	fake.SetMode(fakegmail.ModeTamperBody)
	b := w.must(0, scheduled("mail.send", mailArgs(now.Add(-time.Second), now.Add(6*time.Minute), true), now, now.Add(6*time.Minute)))
	if b.Witness == nil || b.Witness.Verdict != "MISMATCH" {
		t.Fatalf("%s", mustJSON(b))
	}
}

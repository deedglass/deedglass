//go:build integration

package slack_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	slackc "github.com/deedglass/deedglass/internal/connector/slack"
	"github.com/deedglass/deedglass/internal/connector/stripe"
	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/notify"
	slacknotify "github.com/deedglass/deedglass/internal/notify/slack"
	"github.com/deedglass/deedglass/internal/notify/slack/slackstore"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/vault"
)

// The Stripe sandbox key sentinel: the sweep looks for it everywhere.
const stripeKey = "rk_test_51SENTINELstripeLOOPkey0000000009f2c"

// fakeStripe is a sandbox: charges, refunds that remember their idempotency keys, and a charge
// that answers in live mode.
type fakeStripe struct {
	mu      sync.Mutex
	charges map[string]map[string]any
	refunds map[string]map[string]any
	byKey   map[string]string
	posts   int
	auth    []string
	reqs    int
}

func newFakeStripe() *fakeStripe {
	return &fakeStripe{charges: map[string]map[string]any{}, refunds: map[string]map[string]any{}, byKey: map[string]string{}}
}

func (f *fakeStripe) charge(id string, amount int64, cur string) {
	f.charges[id] = map[string]any{"id": id, "object": "charge", "amount": amount, "amount_refunded": int64(0), "currency": cur,
		"customer": "cus_Q2demoCust01", "payment_intent": "pi_3QxZ8f2LdemoPI", "status": "succeeded", "disputed": false,
		"created": time.Now().Add(-48 * time.Hour).Unix(), "livemode": strings.Contains(id, "Live")}
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs++
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", fmt.Sprintf("req_fake%04d", f.reqs))
	w.Header().Set("Stripe-Version", r.Header.Get("Stripe-Version"))
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	switch {
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/charges/"):
		c, ok := f.charges[strings.TrimPrefix(r.URL.Path, "/v1/charges/")]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == "POST" && r.URL.Path == "/v1/refunds":
		if id, ok := f.byKey[r.Header.Get("Idempotency-Key")]; ok {
			_ = json.NewEncoder(w).Encode(f.refunds[id])
			return
		}
		f.posts++
		c := f.charges[form.Get("charge")]
		amt, _ := strconv.ParseInt(form.Get("amount"), 10, 64)
		id := fmt.Sprintf("re_3QxZ9aDemo%03d", f.posts)
		rf := map[string]any{"id": id, "object": "refund", "amount": amt, "currency": c["currency"], "status": "succeeded",
			"charge": form.Get("charge"), "payment_intent": c["payment_intent"],
			"metadata": map[string]any{"deedglass_action_id": form.Get("metadata[deedglass_action_id]")}, "livemode": false}
		f.refunds[id] = rf
		f.byKey[r.Header.Get("Idempotency-Key")] = id
		c["amount_refunded"] = c["amount_refunded"].(int64) + amt
		_ = json.NewEncoder(w).Encode(rf)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/refunds/"):
		_ = json.NewEncoder(w).Encode(f.refunds[strings.TrimPrefix(r.URL.Path, "/v1/refunds/")])
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing"}}`)
	}
}

// refundStack is one throwaway org with the Stripe and Slack connectors, the stripe-refunds
// starter, approvals in Slack, the worker, and an agent.
type refundStack struct {
	e      *storetest.Env
	org    ids.OrgID
	owner  ids.UserID
	led    *ledger.Ledger
	signer *deed.FileSigner
	p      *gate.Pipeline
	ap     *approve.Service
	reg    *connector.Registry
	sn     *slacknotify.Notifier
	st     slackstore.DB
	pr     httpx.Principal
	logs   *syncBuf
	stripe *fakeStripe
	sl     *fakeSlack
	slURL  string
	conID  string
}

func newRefundStack(t *testing.T, stripeCfg map[string]any) *refundStack {
	e := storetest.New(t, storetest.Options{MaxConns: 30})
	s := &refundStack{e: e, logs: &syncBuf{}, stripe: newFakeStripe(), sl: &fakeSlack{}}
	log := slog.New(vault.NewRedactingHandler(slog.NewJSONHandler(s.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	seed, _ := deed.NewSeed(rand.Reader)
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	s.signer = signer
	if _, _, err := e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	s.led = ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))
	keys, err := identity.NewKeyAuth(identity.KeyAuthOptions{DB: e.DB, Pepper: bytes.Repeat([]byte{9}, 32), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	stSrv, slSrv := httptest.NewServer(s.stripe), httptest.NewServer(s.sl)
	t.Cleanup(stSrv.Close)
	t.Cleanup(slSrv.Close)
	s.slURL = slSrv.URL
	o, owner, _, err := e.DB.CreateOrg(bg, store.NewOrg{Name: "Acme", Slug: "acme", OwnerEmail: "owner@acme.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	s.org, s.owner = o.ID, owner.ID
	br := &connectorapi.Breaker{DB: e.DB, Ledger: s.led, Log: log}
	env := connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}, Breaker: br}
	stType, slType := stripe.New(), slackc.New()
	cons := map[string]*vault.Secret{}
	add := func(impl connector.Connector, name string, cfg map[string]any, sec *vault.Secret) string {
		raw, _ := json.Marshal(cfg)
		norm, ferrs := impl.Validate(bg, env, raw)
		if len(ferrs) > 0 {
			t.Fatalf("%s config: %v", name, ferrs)
		}
		hash, _ := deed.DigestJSON(norm)
		id := ids.New[ids.ConnectorKind]().String()
		if err := e.DB.OrgTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
			return store.InsertConnector(ctx, tx, store.Connector{ID: id, OrgID: s.org.String(), Type: impl.Type(), Name: name, Config: norm,
				ConfigHash: hash, Status: "active", CreatedBy: "test"})
		}); err != nil {
			t.Fatal(err)
		}
		cons[id] = sec
		return id
	}
	if stripeCfg == nil {
		stripeCfg = map[string]any{}
	}
	stripeCfg["api_base_url"] = stSrv.URL
	s.conID = add(stType, "stripe", stripeCfg, vault.NewSecretForTest("bearer", map[string]string{"token": stripeKey}))
	add(slType, "slack", map[string]any{"team_id": "T0123ABCD", "app_id": "A0123ABCD", "channels": []string{"#agent-updates"},
		"approvals": map[string]any{"enabled": true, "channel": "#deedglass-approvals"}, "api_base_url": slSrv.URL + "/api"},
		vault.NewSecretForTest("fields", map[string]string{"bot_token": botToken, "signing_secret": signingSecret}))
	s.reg = connector.New(connector.Options{Types: []connector.Connector{stType, slType}, Loader: connector.DBLoader{DB: e.DB},
		Opener: opener{kinds: cons}, Env: env, Logger: log})
	br.Registry = s.reg

	ch := policystore.NewChanges(keys.Listener())
	preg := policy.NewRegistry(policystore.NewRepo(e.DB, nil), ch, policy.WithRegistryLogger(log))
	svc := policy.NewService(policystore.NewRepo(e.DB, s.led), policy.WithRegistry(preg))
	s.st = slackstore.DB{DB: e.DB}
	s.sn = &slacknotify.Notifier{Store: s.st, Conns: s.reg, BaseURL: "https://deedglass.example.test", Log: log}
	s.ap, err = approve.New(approve.Options{DB: e.DB, Ledger: s.led, Notifiers: []notify.Notifier{s.sn}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	s.p, err = gate.New(gate.Options{DB: e.DB, Ledger: s.led, Policies: preg, Usage: policystore.NewUsage(e.DB),
		Intents: identity.NewIntents(e.DB, keys, nil), Guard: keys, Resolver: s.reg, Approvals: s.ap, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	s.ap.Attach(s.p)
	s.p.SetJobHandler(gate.JobApprovalNotify, s.ap.NotifyJob)
	s.p.SetJobHandler(slacknotify.JobNotifyUpdate, s.sn.UpdateJob)
	worker := s.p.NewWorker("refund-loop")
	worker.PollEvery, worker.SweepEvery = 50*time.Millisecond, time.Hour
	bus := ledger.NewBus()
	bus.Handle(s.p.Broker().OnBus)
	keys.Listener().Listen(ledger.Channel, bus.Notify, bus.Reset)
	keys.Listener().Listen(store.JobsChannel, worker.Wake, func() { worker.Wake("") })
	rctx, cancel := context.WithCancel(bg)
	var wg sync.WaitGroup
	if _, _, err := ch.Subscribe(rctx); err != nil {
		t.Fatal(err)
	}
	wg.Go(func() { _ = preg.Run(rctx) })
	wg.Go(func() { keys.Run(rctx) })
	wg.Go(func() { _ = worker.Run(rctx) })
	wg.Go(func() { s.sn.Watch(rctx, bus) })
	t.Cleanup(func() {
		cctx, c2 := context.WithTimeout(bg, 10*time.Second)
		defer c2()
		_ = s.p.Close(cctx)
		cancel()
		wg.Wait()
	})
	for deadline := time.Now().Add(5 * time.Second); !keys.Listener().Healthy(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the listener never became healthy")
		}
	}
	src, err := os.ReadFile("../../../examples/policies/stripe-refunds.yaml")
	if err != nil {
		t.Fatal(err)
	}
	caller := policy.Caller{Org: s.org.String(), Actor: deed.Ref{Type: "user", ID: owner.ID.String()}}
	pol, ver, err := svc.Create(bg, caller, policy.PushRequest{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(bg, caller, pol.ID, policy.VersionRef{Number: ver.Version}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "refund loop"}); err != nil {
		t.Fatal(err)
	}
	var agent store.Agent
	if err := e.DB.SystemTx(bg, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		u, err := store.UpsertUserByEmail(ctx, tx, "priya@acme.example.test", "Priya Raman")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'admin', 'active', pg_catalog.now())`, ids.New[ids.MembershipKind](), s.org, u.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE deedglass.users SET status = 'active' WHERE id = $1`, u.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.OrgTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		agent, err = store.CreateAgent(ctx, tx, s.org, store.NewAgent{Name: "support-bot", CreatedBy: "system"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s.pr = httpx.Principal{Kind: httpx.KindAgentKey, OrgID: s.org, AgentID: agent.ID, AgentName: agent.Name, AgentStatus: "active", Scopes: httpx.DefaultAgentScopes}
	return s
}

func (s *refundStack) refund(t *testing.T, charge string, amount int64, extra string) (*gate.Action, error) {
	args := fmt.Sprintf(`{"charge":%q,"amount":%d%s}`, charge, amount, extra)
	return s.p.Submit(bg, s.pr, gate.ActionRequest{Tool: "stripe.create_refund", Args: json.RawMessage(args), WaitSet: true, Wait: 5 * time.Second})
}

func (s *refundStack) sealed(t *testing.T, id string) (verdict, level, deedID string) {
	var state string
	waitFor(t, 20*time.Second, "the seal of "+id, func() bool {
		_ = s.e.DB.OrgReadTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT state, COALESCE(witness_verdict, ''), COALESCE(witness_level, ''), COALESCE(deed_id, '')
			    FROM deedglass.actions WHERE org_id = $1 AND id = $2`, s.org, id).Scan(&state, &verdict, &level, &deedID)
		})
		return state == "sealed"
	})
	return
}

func (s *refundStack) deedJSON(t *testing.T, id string) string {
	var b string
	if err := s.e.DB.OrgReadTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT body::text FROM deedglass.deeds WHERE org_id = $1 AND id = $2`, s.org, id).Scan(&b)
	}); err != nil {
		t.Fatal(err)
	}
	return b
}

func (s *refundStack) budgetUsed(t *testing.T) int64 {
	var v int64
	if err := s.e.DB.OrgReadTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(sum(value), 0) FROM deedglass.usage_counters WHERE org_id = $1 AND counter = 'budget:refunds-per-day'`, s.org).Scan(&v)
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

// verifyOffline exports the org's deeds and verifies them with only the public key.
func (s *refundStack) verifyOffline(t *testing.T) *deed.Report {
	var buf bytes.Buffer
	if _, _, _, err := s.led.ExportJSONL(bg, s.org, ledger.Range{}, &buf); err != nil {
		t.Fatal(err)
	}
	ks, err := deed.NewKeySet("https://deedglass.example.test", []deed.KeyRecord{{ID: s.signer.KeyID(), PublicKey: s.signer.PublicKey(),
		Status: deed.KeyActive, ActivatedAt: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := deed.VerifyStream(bg, &buf, deed.StreamOptions{Keys: ks, KeysPinned: true, Org: s.org.String()})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestStripeRefundLoop is S30 §4 step 15: $20 → allow → L1 VERIFIED with cost $20.00; $120 →
// pending → the Slack card with the summary → a signed Approve → executes → VERIFIED; a live key on
// PUT is refused; a live-mode answer trips the breaker; the deeds verify offline; no key leaks.
func TestStripeRefundLoop(t *testing.T) {
	s := newRefundStack(t, nil)
	s.stripe.charge("ch_3QxZ8f2LdemoA1", 14900, "usd")

	// 1. $20.00 runs and is read back.
	a, err := s.refund(t, "ch_3QxZ8f2LdemoA1", 2000, "")
	if err != nil {
		t.Fatal(err)
	}
	verdict, level, deedID := s.sealed(t, a.ID)
	if verdict != "VERIFIED" || level != "L1" {
		t.Fatalf("$20: %s %s", verdict, level)
	}
	if body := s.deedJSON(t, deedID); !strings.Contains(body, `"cost": {"amount": 2000, "currency": "usd"}`) && !strings.Contains(body, `"cost":{"amount":2000,"currency":"usd"}`) {
		t.Fatalf("the deed's cost: %s", body)
	}

	// 2. $120.00 waits; the card says the amounts from Stripe.
	b, err := s.refund(t, "ch_3QxZ8f2LdemoA1", 12000, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != ledger.StatusPendingApproval || b.Approval == nil {
		t.Fatalf("$120: %s", b.Status)
	}
	waitFor(t, 10*time.Second, "the card", func() bool { return s.sl.postCount() == 1 })
	blocks := s.sl.posts[0].Get("blocks")
	if !strings.Contains(blocks, "$120.00") || !strings.Contains(blocks, "$149.00") || !strings.Contains(blocks, "ch_3QxZ8f2LdemoA1") ||
		!strings.Contains(blocks, "large-refunds-need-approval") {
		t.Fatalf("card %s", blocks)
	}
	value := ""
	var bks []map[string]any
	_ = json.Unmarshal([]byte(blocks), &bks)
	for _, blk := range bks {
		els, _ := blk["elements"].([]any)
		for _, x := range els {
			m, _ := x.(map[string]any)
			if m["action_id"] == "deedglass_approve" {
				value, _ = m["value"].(string)
			}
		}
	}
	h := &slacknotify.Handler{Store: s.st, Conns: s.reg, Approvals: s.ap, Log: slog.New(slog.DiscardHandler)}
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(h.Routes()...)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	payload, _ := json.Marshal(map[string]any{"type": "block_actions", "team": map[string]any{"id": "T0123ABCD"}, "api_app_id": "A0123ABCD",
		"user": map[string]any{"id": "U0PRIYA", "team_id": "T0123ABCD"}, "trigger_id": "1.2.3", "response_url": s.slURL + "/api/respond",
		"actions": []map[string]any{{"action_id": "deedglass_approve", "value": value}}})
	body := "payload=" + url.QueryEscape(string(payload))
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/slack/interact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", slackc.Sign([]byte(signingSecret), ts, []byte(body)))
	resp, err := srv.Client().Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("approve: %v %v", err, resp)
	}
	resp.Body.Close()
	verdict, level, _ = s.sealed(t, b.ID)
	if verdict != "VERIFIED" || level != "L1" {
		t.Fatalf("$120: %s %s", verdict, level)
	}
	waitFor(t, 10*time.Second, "the sealed card", func() bool { return strings.Contains(s.sl.lastUpdateText(), "Deed sealed") })
	if s.stripe.posts != 2 {
		t.Fatalf("%d refunds; want 2", s.stripe.posts)
	}

	// 3. A live key on PUT …/credentials is refused before it reaches the vault.
	puts := &countingVault{}
	capi := &connectorapi.API{DB: s.e.DB, Registry: s.reg, Ledger: s.led, Vault: puts}
	cs := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: adminWho{s.org}, Keys: adminWho{s.org}})
	cs.Mount(connectorapi.Routes(capi)...)
	csrv := httptest.NewServer(cs)
	t.Cleanup(csrv.Close)
	for _, k := range []string{"rk_live_51abcdefghijklmnopqrstuv", "sk_live_51abcdefghijklmnopqrstuv"} {
		preq, _ := http.NewRequest("PUT", csrv.URL+"/v1/connectors/stripe/credentials", strings.NewReader(`{"kind":"bearer","secret":{"token":"`+k+`"}}`))
		preq.Header.Set("Content-Type", "application/json")
		preq.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_"+strings.Repeat("B", 32))
		presp, err := csrv.Client().Do(preq)
		if err != nil {
			t.Fatal(err)
		}
		pb, _ := io.ReadAll(presp.Body)
		presp.Body.Close()
		if presp.StatusCode != 400 || !strings.Contains(string(pb), `"stripe_live_key_refused"`) || strings.Contains(string(pb), k[8:]) {
			t.Fatalf("PUT %s…: %d %s", k[:8], presp.StatusCode, pb)
		}
	}
	if puts.n != 0 {
		t.Fatalf("the vault saw %d writes", puts.n)
	}

	// 4. Chain intact and the deeds verify offline with the public key.
	rep, err := s.led.CheckChain(bg, s.org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || rep.Status != "intact" {
		t.Fatalf("chain %+v %v", rep, err)
	}
	if vr := s.verifyOffline(t); vr.Result != "valid" {
		t.Fatalf("offline verify: %+v", vr)
	}

	// 5. A live-mode answer trips the breaker: disabled, health.reason, connector.disabled sealed,
	// and nothing more is sent.
	s.stripe.charge("ch_LiveModeDemo01", 1000, "usd")
	c, err := s.refund(t, "ch_LiveModeDemo01", 500, "")
	if err != nil && httpx.AsError(err) == nil {
		t.Fatal(err)
	}
	_ = c
	var status, health string
	waitFor(t, 5*time.Second, "the breaker", func() bool {
		_ = s.e.DB.OrgReadTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status, health::text FROM deedglass.connectors WHERE org_id = $1 AND id = $2`, s.org, s.conID).Scan(&status, &health)
		})
		return status == "disabled"
	})
	if !strings.Contains(health, "livemode_detected") {
		t.Fatalf("health %s", health)
	}
	var disabled int
	_ = s.e.DB.OrgReadTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = 'connector.disabled'`, s.org).Scan(&disabled)
	})
	if disabled != 1 {
		t.Fatalf("%d connector.disabled deeds", disabled)
	}
	before := s.stripe.reqs
	if _, err := s.refund(t, "ch_3QxZ8f2LdemoA1", 100, ""); err == nil && false {
		t.Fatal("unreachable")
	}
	if s.stripe.reqs != before {
		t.Fatalf("%d requests after the breaker tripped", s.stripe.reqs-before)
	}
	if !strings.Contains(s.logs.String(), `"alert":"connector_disabled","reason":"livemode_detected"`) {
		t.Fatal("no alert in the log")
	}
	if vr := s.verifyOffline(t); vr.Result != "valid" {
		t.Fatalf("offline verify after the breaker: %+v", vr)
	}

	// 6. The sentinel sweep.
	var dump strings.Builder
	dump.WriteString(s.logs.String())
	if err := s.e.DB.OrgReadTx(bg, s.org, func(ctx context.Context, tx pgx.Tx) error {
		for _, q := range []string{`SELECT COALESCE(pg_catalog.jsonb_agg(a)::text, '') FROM deedglass.actions a`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(d)::text, '') FROM deedglass.deeds d`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(e)::text, '') FROM deedglass.action_events e`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(x)::text, '') FROM deedglass.admin_events x`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(j)::text, '') FROM deedglass.witness_jobs j`} {
			var v string
			if err := tx.QueryRow(ctx, q).Scan(&v); err != nil {
				return err
			}
			dump.WriteString(v)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, f := range s.sl.posts {
		dump.WriteString(f.Get("blocks"))
	}
	for _, sec := range []string{stripeKey, botToken, signingSecret, "rk_live_51abcdefghijklmnopqrstuv"} {
		if strings.Contains(dump.String(), sec) {
			t.Fatalf("a credential leaked: %.12s…", sec)
		}
	}
	for _, a := range s.stripe.auth {
		if a != "Bearer "+stripeKey {
			t.Fatalf("Stripe saw %.20q", a)
		}
	}
}

// TestStripeRefundBudget is S30 §4 step 8: with a $5,000.00 charge, 25 refunds of $20.00 run, the
// 26th is budget_exceeded with Retry-After, and a refund the connector refuses releases its
// reservation.
func TestStripeRefundBudget(t *testing.T) {
	s := newRefundStack(t, map[string]any{"refund_reasons": []string{"requested_by_customer"}})
	s.stripe.charge("ch_3QxZ8f2LdemoBG", 500000, "usd")
	for i := 1; i <= 25; i++ {
		if i == 10 {
			before := s.budgetUsed(t)
			a, err := s.refund(t, "ch_3QxZ8f2LdemoBG", 2000, `,"reason":"duplicate"`)
			if err != nil {
				t.Fatal(err)
			}
			if a.Status != ledger.StatusFailed {
				t.Fatalf("the refused refund: %s", a.Status)
			}
			if after := s.budgetUsed(t); after != before {
				t.Fatalf("the refused refund kept its reservation: %d → %d", before, after)
			}
			t.Logf("refused refund: reserved then released, budget %d → %d", before, s.budgetUsed(t))
		}
		a, err := s.refund(t, "ch_3QxZ8f2LdemoBG", 2000, "")
		if err != nil {
			t.Fatalf("refund %d: %v", i, err)
		}
		if a.Status != ledger.StatusExecuted {
			t.Fatalf("refund %d: %s", i, a.Status)
		}
	}
	if used := s.budgetUsed(t); used != 50000 {
		t.Fatalf("budget used %d; want 50000", used)
	}
	_, err := s.refund(t, "ch_3QxZ8f2LdemoBG", 2000, "")
	he := httpx.AsError(err)
	if he == nil || he.Code != httpx.CodeBudgetExceeded || he.Header.Get("Retry-After") == "" || !strings.Contains(he.Message, "$520.00 of $500.00") {
		t.Fatalf("26th: %v", err)
	}
	if used := s.budgetUsed(t); used != 50000 {
		t.Fatalf("budget after the 26th %d; want 50000", used)
	}
	if s.stripe.posts != 25 {
		t.Fatalf("%d refunds at Stripe; want 25", s.stripe.posts)
	}
	t.Logf("budget: 25 × $20.00 recorded = %d minor units; the 26th refused (Retry-After %s)", s.budgetUsed(t), he.Header.Get("Retry-After"))
}

type countingVault struct{ n int }

func (c *countingVault) Put(context.Context, vault.Ref, string, map[string][]byte, vault.Actor) (vault.Meta, error) {
	c.n++
	return vault.Meta{}, nil
}

func (c *countingVault) Delete(context.Context, vault.Ref, vault.Actor) (vault.Meta, error) {
	return vault.Meta{}, nil
}

func (c *countingVault) DeleteTx(context.Context, pgx.Tx, store.Connector, vault.Actor) (*string, error) {
	return nil, nil
}

type adminWho struct{ org ids.OrgID }

func (w adminWho) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
}

func (w adminWho) AuthenticateKey(context.Context, string) (httpx.Principal, error) {
	return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: w.org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin, Scopes: []string{httpx.ScopeAdmin}}, nil
}

func (c *countingVault) Meta(context.Context, vault.Ref) (vault.Meta, error) {
	return vault.Meta{}, vault.ErrNotFound
}

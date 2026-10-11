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
	"github.com/deedglass/deedglass/internal/connector/github"
	slackc "github.com/deedglass/deedglass/internal/connector/slack"
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

var bg = context.Background()

// Sentinel credentials: the sweep at the end looks for them everywhere.
const (
	ghToken       = "github_pat_11SENTINELSENTINEL0123_abcdefghijklmnopqrstuvwxyzABCDEFGH"
	botToken      = "xoxb-1234567890-SENTINELbottoken99"
	signingSecret = "5e171ae15e171ae15e171ae15e171ae1"
)

// opener hands each connector its test credential (the vault's role in production).
type opener struct{ kinds map[string]*vault.Secret }

func (o opener) Open(_ context.Context, r vault.Ref) (*vault.Secret, error) {
	switch o.kinds[r.Connector.String()] {
	case nil:
		return nil, vault.ErrNotFound
	}
	s := o.kinds[r.Connector.String()]
	fields := map[string]string{}
	for _, n := range s.Names() {
		fields[n] = s.Field(n)
	}
	return vault.NewSecretForTest(s.Kind(), fields), nil
}

// fakeGitHub keeps the pull requests posted to it, so the read-back sees what was written.
type fakeGitHub struct {
	mu    sync.Mutex
	prs   map[int]map[string]any
	posts int
	auth  []string
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && r.URL.Path == "/repos/arjun/deedglass-sandbox":
		io.WriteString(w, `{"full_name":"arjun/deedglass-sandbox","default_branch":"main","private":true,"permissions":{"push":true}}`)
	case r.Method == "POST" && r.URL.Path == "/repos/arjun/deedglass-sandbox/pulls":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.posts++
		n := 6 + g.posts
		pr := map[string]any{"number": n, "state": "open", "title": in["title"], "body": in["body"],
			"html_url": fmt.Sprintf("https://github.com/arjun/deedglass-sandbox/pull/%d", n),
			"head":     map[string]any{"ref": in["head"], "sha": "9f2c4e6a8b0d1f3a5c7e9b1d3f5a7c9e1b3d5f7a"},
			"base":     map[string]any{"ref": in["base"], "sha": "5b1f3a9c2e7d4b6a8f0c1e3d5a7b9c2e4f6a8b0c"}, "merged": false,
			"user": map[string]any{"login": "arjun-sandbox-bot"}}
		if g.prs == nil {
			g.prs = map[int]map[string]any{}
		}
		g.prs[n] = pr
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(pr)
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/arjun/deedglass-sandbox/pulls/"):
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/repos/arjun/deedglass-sandbox/pulls/"))
		pr, ok := g.prs[n]
		if !ok {
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(pr)
	case r.Method == "GET" && r.URL.Path == "/user":
		io.WriteString(w, `{"login":"arjun-sandbox-bot"}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"Not Found"}`)
	}
}

// fakeSlack records the approval card's posts and edits.
type fakeSlack struct {
	mu       sync.Mutex
	posts    []url.Values
	updates  []url.Values
	responds []string
	auth     []string
}

func (s *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	switch strings.TrimPrefix(r.URL.Path, "/api/") {
	case "conversations.list":
		io.WriteString(w, `{"ok":true,"channels":[{"id":"C0APPROVALS","name":"deedglass-approvals","is_member":true},{"id":"C0AGENTUPD","name":"agent-updates","is_member":true}],"response_metadata":{"next_cursor":""}}`)
	case "chat.postMessage":
		s.posts = append(s.posts, form)
		io.WriteString(w, `{"ok":true,"channel":"C0APPROVALS","ts":"1760069640.000100"}`)
	case "chat.update":
		s.updates = append(s.updates, form)
		io.WriteString(w, `{"ok":true,"channel":"C0APPROVALS","ts":"1760069640.000100"}`)
	case "users.info":
		io.WriteString(w, `{"ok":true,"user":{"id":"U0PRIYA","team_id":"T0123ABCD","name":"priya","real_name":"Priya Raman","deleted":false,"is_bot":false,`+
			`"is_restricted":false,"is_ultra_restricted":false,"is_email_confirmed":true,"profile":{"email":"priya@acme.example.test"}}}`)
	case "respond":
		s.responds = append(s.responds, string(body))
		io.WriteString(w, `ok`)
	default:
		io.WriteString(w, `{"ok":false,"error":"unknown_method"}`)
	}
}

func (s *fakeSlack) lastUpdateText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.updates) == 0 {
		return ""
	}
	return s.updates[len(s.updates)-1].Get("blocks")
}

func (s *fakeSlack) postCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.posts)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestSlackApprovalLoop is S29 §4 step 14: github.create_pr into main → pending approval → the card in
// Slack → a signed Approve → the worker resumes → the pull request → L1 VERIFIED → the card says the
// deed is sealed; the chain verifies, and no credential is anywhere it shouldn't be.
func TestSlackApprovalLoop(t *testing.T) {
	e := storetest.New(t, storetest.Options{MaxConns: 30})
	logs := &syncBuf{}
	log := slog.New(vault.NewRedactingHandler(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	seed, _ := deed.NewSeed(rand.Reader)
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	led := ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))
	keys, err := identity.NewKeyAuth(identity.KeyAuthOptions{DB: e.DB, Pepper: bytes.Repeat([]byte{9}, 32), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	gh, sl := &fakeGitHub{}, &fakeSlack{}
	ghSrv, slSrv := httptest.NewServer(gh), httptest.NewServer(sl)
	t.Cleanup(ghSrv.Close)
	t.Cleanup(slSrv.Close)

	o, owner, _, err := e.DB.CreateOrg(bg, store.NewOrg{Name: "Acme", Slug: "acme", OwnerEmail: "owner@acme.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	org := o.ID
	env := connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	ghType, slType := github.New(), slackc.New()
	cons := map[string]*vault.Secret{}
	addConnector := func(impl connector.Connector, name string, cfg map[string]any, sec *vault.Secret) string {
		raw, _ := json.Marshal(cfg)
		norm, ferrs := impl.Validate(bg, env, raw)
		if len(ferrs) > 0 {
			t.Fatalf("%s config: %v", name, ferrs)
		}
		hash, _ := deed.DigestJSON(norm)
		id := ids.New[ids.ConnectorKind]().String()
		if err := e.DB.OrgTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
			return store.InsertConnector(ctx, tx, store.Connector{ID: id, OrgID: org.String(), Type: impl.Type(), Name: name, Config: norm,
				ConfigHash: hash, Status: "active", CreatedBy: "test"})
		}); err != nil {
			t.Fatal(err)
		}
		cons[id] = sec
		return id
	}
	addConnector(ghType, "github", map[string]any{"auth": "pat", "repos": []string{"arjun/deedglass-sandbox"}, "api_base_url": ghSrv.URL},
		vault.NewSecretForTest("bearer", map[string]string{"token": ghToken}))
	addConnector(slType, "slack", map[string]any{"team_id": "T0123ABCD", "app_id": "A0123ABCD", "channels": []string{"#agent-updates"},
		"approvals": map[string]any{"enabled": true, "channel": "#deedglass-approvals"}, "api_base_url": slSrv.URL + "/api"},
		vault.NewSecretForTest("fields", map[string]string{"bot_token": botToken, "signing_secret": signingSecret}))
	reg := connector.New(connector.Options{Types: []connector.Connector{ghType, slType}, Loader: connector.DBLoader{DB: e.DB},
		Opener: opener{kinds: cons}, Env: env, Logger: log})

	// The Gate, approvals with the Slack notifier, the worker and the bus.
	ch := policystore.NewChanges(keys.Listener())
	preg := policy.NewRegistry(policystore.NewRepo(e.DB, nil), ch, policy.WithRegistryLogger(log))
	svc := policy.NewService(policystore.NewRepo(e.DB, led), policy.WithRegistry(preg))
	st := slackstore.DB{DB: e.DB}
	sn := &slacknotify.Notifier{Store: st, Conns: reg, BaseURL: "https://deedglass.example.test", Log: log}
	ap, err := approve.New(approve.Options{DB: e.DB, Ledger: led, Notifiers: []notify.Notifier{sn}, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	p, err := gate.New(gate.Options{DB: e.DB, Ledger: led, Policies: preg, Usage: policystore.NewUsage(e.DB),
		Intents: identity.NewIntents(e.DB, keys, nil), Guard: keys, Resolver: reg, Approvals: ap, Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ap.Attach(p)
	p.SetJobHandler(gate.JobApprovalNotify, ap.NotifyJob)
	p.SetJobHandler(slacknotify.JobNotifyUpdate, sn.UpdateJob)
	worker := p.NewWorker("slack-loop")
	worker.PollEvery, worker.SweepEvery = 50*time.Millisecond, time.Hour
	bus := ledger.NewBus()
	bus.Handle(p.Broker().OnBus)
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
	wg.Go(func() { sn.Watch(rctx, bus) })
	t.Cleanup(func() {
		cctx, c2 := context.WithTimeout(bg, 10*time.Second)
		defer c2()
		_ = p.Close(cctx)
		cancel()
		wg.Wait()
	})
	for deadline := time.Now().Add(5 * time.Second); !keys.Listener().Healthy(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the listener never became healthy")
		}
	}

	// The starter policy, an admin who uses Slack, and the agent.
	src, err := os.ReadFile("../../../examples/policies/github-read-mostly.yaml")
	if err != nil {
		t.Fatal(err)
	}
	caller := policy.Caller{Org: org.String(), Actor: deed.Ref{Type: "user", ID: owner.ID.String()}}
	pol, ver, err := svc.Create(bg, caller, policy.PushRequest{Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(bg, caller, pol.ID, policy.VersionRef{Number: ver.Version},
		policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "slack loop"}); err != nil {
		t.Fatal(err)
	}
	var agent store.Agent
	if err := e.DB.SystemTx(bg, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		u, err := store.UpsertUserByEmail(ctx, tx, "priya@acme.example.test", "Priya Raman")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'admin', 'active', pg_catalog.now())`, ids.New[ids.MembershipKind](), org, u.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE deedglass.users SET status = 'active' WHERE id = $1`, u.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.DB.OrgTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		agent, err = store.CreateAgent(ctx, tx, org, store.NewAgent{Name: "support-bot", CreatedBy: "system"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pr := httpx.Principal{Kind: httpx.KindAgentKey, OrgID: org, AgentID: agent.ID, AgentName: agent.Name, AgentStatus: "active",
		Scopes: httpx.DefaultAgentScopes}

	// 1. The agent opens a pull request into main: the starter pauses it for a person.
	args := `{"repo":"arjun/deedglass-sandbox","head":"agent/fix-typo","base":"main","title":"Fix typo in README","body":"Fixes the typo."}`
	a, err := p.Submit(bg, pr, gate.ActionRequest{Tool: "github.create_pr", Args: json.RawMessage(args), WaitSet: true, Wait: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != ledger.StatusPendingApproval || a.Approval == nil {
		t.Fatalf("status %s approval %v", a.Status, a.Approval)
	}
	if gh.posts != 0 {
		t.Fatal("the pull request was created before anyone approved")
	}
	// 2. The card is posted.
	waitFor(t, 10*time.Second, "the card", func() bool { return sl.postCount() == 1 })
	blocks := sl.posts[0].Get("blocks")
	if !strings.Contains(blocks, "Approval needed") || !strings.Contains(blocks, "pull-requests-to-main-need-approval") ||
		!strings.Contains(sl.posts[0].Get("channel"), "C0APPROVALS") {
		t.Fatalf("card %s", blocks)
	}
	value := ""
	var bks []map[string]any
	_ = json.Unmarshal([]byte(blocks), &bks)
	for _, b := range bks {
		els, _ := b["elements"].([]any)
		for _, x := range els {
			m, _ := x.(map[string]any)
			if m["action_id"] == "deedglass_approve" {
				value, _ = m["value"].(string)
			}
		}
	}
	if !strings.HasPrefix(value, a.Approval.ID+"|sha256:") {
		t.Fatalf("approve value %q", value)
	}

	// 3. Priya taps Approve in Slack: a signed request to POST /v1/slack/interact.
	h := &slacknotify.Handler{Store: st, Conns: reg, Approvals: ap, Log: log}
	api := httpx.New(httpx.Options{Logger: log})
	api.Mount(h.Routes()...)
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	payload, _ := json.Marshal(map[string]any{"type": "block_actions", "team": map[string]any{"id": "T0123ABCD"}, "api_app_id": "A0123ABCD",
		"user": map[string]any{"id": "U0PRIYA", "team_id": "T0123ABCD"}, "trigger_id": "1.2.3", "response_url": slSrv.URL + "/api/respond",
		"actions": []map[string]any{{"action_id": "deedglass_approve", "value": value}}})
	body := "payload=" + url.QueryEscape(string(payload))
	send := func(sig string) int {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req, _ := http.NewRequest("POST", srv.URL+"/v1/slack/interact", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		if sig == "" {
			sig = slackc.Sign([]byte(signingSecret), ts, []byte(body))
		}
		req.Header.Set("X-Slack-Signature", sig)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := send("v0=" + strings.Repeat("0", 64)); code != 401 {
		t.Fatalf("a forged click: %d", code)
	}
	start := time.Now()
	if code := send(""); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("the handler took %v", took)
	}

	// 4. The worker resumes the action: the pull request, an L1 read-back, a sealed deed.
	var state, verdict, level, deedID string
	waitFor(t, 20*time.Second, "the seal", func() bool {
		_ = e.DB.OrgReadTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT state, COALESCE(witness_verdict, ''), COALESCE(witness_level, ''), COALESCE(deed_id, '')
			    FROM deedglass.actions WHERE org_id = $1 AND id = $2`, org, a.ID).Scan(&state, &verdict, &level, &deedID)
		})
		return state == "sealed"
	})
	if verdict != "VERIFIED" || level != "L1" || deedID == "" || gh.posts != 1 {
		t.Fatalf("verdict %s level %s deed %s posts %d", verdict, level, deedID, gh.posts)
	}
	// 5. The card follows to the seal.
	waitFor(t, 30*time.Second, "the sealed card", func() bool { return strings.Contains(sl.lastUpdateText(), "Deed sealed") })
	if !strings.Contains(sl.lastUpdateText(), "Approved by Priya") {
		t.Fatalf("card %s", sl.lastUpdateText())
	}
	// The vote is a slack vote with its admin deed; the chain verifies.
	var via string
	if err := e.DB.OrgReadTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT via FROM deedglass.approval_votes WHERE org_id = $1 AND approval_id = $2`, org, a.Approval.ID).Scan(&via)
	}); err != nil || via != "slack" {
		t.Fatalf("vote via %q: %v", via, err)
	}
	rep, err := led.CheckChain(bg, org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || rep.Status != "intact" {
		t.Fatalf("chain %+v %v", rep, err)
	}
	// A replay of the same click does nothing more.
	if code := send(""); code != 200 {
		t.Fatalf("replay: %d", code)
	}

	// The sentinel sweep: logs, rows, deeds and the card never hold a credential.
	var dump strings.Builder
	dump.WriteString(logs.String())
	if err := e.DB.OrgReadTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
		for _, q := range []string{`SELECT COALESCE(pg_catalog.jsonb_agg(a)::text, '') FROM deedglass.actions a`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(d)::text, '') FROM deedglass.deeds d`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(e)::text, '') FROM deedglass.action_events e`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(x)::text, '') FROM deedglass.admin_events x`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(m)::text, '') FROM deedglass.notification_messages m`,
			`SELECT COALESCE(pg_catalog.jsonb_agg(j)::text, '') FROM deedglass.witness_jobs j`} {
			var s string
			if err := tx.QueryRow(ctx, q).Scan(&s); err != nil {
				return err
			}
			dump.WriteString(s)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sl.mu.Lock()
	for _, f := range append(append([]url.Values{}, sl.posts...), sl.updates...) {
		dump.WriteString(f.Get("blocks") + f.Get("text"))
	}
	sl.mu.Unlock()
	for _, s := range []string{ghToken, botToken, signingSecret} {
		if strings.Contains(dump.String(), s) {
			t.Fatalf("a credential leaked: %.12s…", s)
		}
	}
	// Each service got only its own credential.
	for _, h := range gh.auth {
		if h != "Bearer "+ghToken {
			t.Fatalf("GitHub saw %q", h)
		}
	}
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !ok(); time.Sleep(25 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

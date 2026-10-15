package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/deedglass/deedglass/ee/agency"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/api"
	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/approve/sections"
	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/console"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/mcp/upstream"
	"github.com/deedglass/deedglass/internal/notify"
	slacknotify "github.com/deedglass/deedglass/internal/notify/slack"
	"github.com/deedglass/deedglass/internal/notify/slack/slackstore"
	"github.com/deedglass/deedglass/internal/notify/webhook"
	"github.com/deedglass/deedglass/internal/observe"
	"github.com/deedglass/deedglass/internal/observeapi"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/policyapi"
	"github.com/deedglass/deedglass/internal/securitypackapi"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/learnstore"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/undoapi"
	"github.com/deedglass/deedglass/internal/vault"
	"github.com/deedglass/deedglass/internal/weeklyapi"
)

// --- flags ---

type serveOpts struct{ migrate bool }

var serveFlagValues serveOpts

func serveFlags(fs *flag.FlagSet) {
	serveFlagValues = serveOpts{}
	fs.BoolVar(&serveFlagValues.migrate, "migrate", false, "dev only: apply pending migrations first (DEEDGLASS_MIGRATE_DATABASE_URL)")
}

// Seams for tests.
var (
	// serveSignals delivers SIGINT and SIGTERM. The first drains; the second exits at once.
	serveSignals = func() (<-chan os.Signal, func()) {
		ch := make(chan os.Signal, 2)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		return ch, func() { signal.Stop(ch) }
	}
	serveExit = os.Exit
	// serveListening is called once the API listener is open (tests read the address).
	serveListening = func(net.Addr) {}
	// serveGate receives the Gate once it is built (tests decide approvals in process: S22 brings
	// the routes).
	serveGate = func(*gate.Pipeline) {}
)

// Server timings (S03 §3.8).
const (
	openBudget      = 10 * time.Second
	prodDrain       = 5 * time.Second // keep serving after the first signal so load balancers notice
	shutdownTimeout = 20 * time.Second
	pruneEvery      = 10 * time.Minute
)

func runServe(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass serve takes no arguments (got %q)", args[0]), "Run deedglass help serve.")
	}
	started := time.Now()
	cfg, warnings, err := config.Load(e.LoadOptions())
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	if err != nil {
		return err
	}
	if err := cfg.Require(config.NeedDatabase | config.NeedPepper); errors.As(err, &ps) {
		return configProblems(e, ps, warnings)
	}
	prod := cfg.Env == config.Prod
	if prod && serveFlagValues.migrate {
		return usageErr(errors.New("--migrate is for dev"), "In prod, run deedglass migrate up as a separate step, then start the server.")
	}
	printWarnings(e, warnings)
	s := e.Style
	// The S03 logger with the vault's redaction on top (S16): each call's credential values,
	// carried in the context, and secrets by their shape.
	log := slog.New(vault.NewRedactingHandler(httpx.NewLogger(e.Stderr, prod, cfg.LogLevel).Handler()))
	bi := buildInfo()
	fmt.Fprintf(e.Stdout, "%s %s api %s %s env %s\n", s.Bold("deedglass "+bi.Version), ui.Sep, buildinfo.LatestAPIVersion, ui.Sep, cfg.Env)

	// 1–2. The database, within 10 s.
	t := targetOf(cfg.DatabaseURL)
	octx, cancel := context.WithTimeout(ctx, openBudget)
	db, err := store.Open(octx, cfg.DatabaseURL, store.Options{Logger: log})
	cancel()
	if err != nil {
		cause := err
		if u := errors.Unwrap(err); u != nil {
			cause = u
		}
		return &ExitError{Code: ExitFailure, ErrCode: "database_unreachable",
			Err:  fmt.Errorf("Can't connect to %s as %s: %s", t.addr(), t.User, connectReason(cause, cfg.DatabaseURL.Reveal())),
			Next: "Is Postgres running? Try make dev."}
	}
	defer db.Close()

	// 3. The runtime role: row-level security must apply to it.
	role, err := db.CheckRuntimeRole(ctx)
	if err != nil {
		return fmt.Errorf("checking the database role: %w", err)
	}
	if !role.Clean() {
		why := "row-level security would not apply"
		if !role.Superuser && !role.BypassRLS {
			why = "it could change the schema"
		}
		if prod {
			return &ExitError{Code: ExitFailure, ErrCode: "unsafe_database_role",
				Err:  fmt.Errorf("Refusing to start: the runtime role %q %s, so %s", role.User, roleProblem(role), why),
				Next: "Connect as deedglass_app (docs/DATABASE_SETUP.md)."}
		}
		fmt.Fprintln(e.Stderr, s.Amber(fmt.Sprintf("Warning: the runtime role %q %s, so %s for this process. Use deedglass_app.", role.User, roleProblem(role), devWhy(role))))
	}

	// 4. Migrations.
	ms, err := migrate.Load(migrations.FS)
	if err != nil {
		return err
	}
	rows, err := db.MigrationRows(ctx, ms)
	if err != nil {
		return fmt.Errorf("reading the schema version: %w", err)
	}
	st := migrationState(rows, ms)
	if st.Changed {
		if prod {
			return &ExitError{Code: ExitFailure, ErrCode: "migration_changed",
				Err: errors.New("Refusing to start: the database's migrations don't match this build"), Next: "Run deedglass migrate verify for details."}
		}
		fmt.Fprintln(e.Stderr, s.Amber("Warning: the database's migrations don't match this build. Run deedglass migrate verify."))
	}
	if pending := st.Expected - st.Applied; pending > 0 {
		switch {
		case prod:
			return &ExitError{Code: ExitFailure, ErrCode: "migrations_pending",
				Err:  fmt.Errorf("Refusing to start: %d %s pending", pending, plural(pending, "migration is", "migrations are")),
				Next: "Run deedglass migrate up first."}
		case serveFlagValues.migrate:
			if err := serveMigrate(ctx, e, cfg); err != nil {
				return err
			}
			if rows, err = db.MigrationRows(ctx, ms); err != nil {
				return err
			}
			st = migrationState(rows, ms)
		default:
			fmt.Fprintln(e.Stderr, s.Amber(fmt.Sprintf("Warning: %d %s pending. Run deedglass migrate up, or serve --migrate.", pending, plural(pending, "migration is", "migrations are"))))
		}
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Database %s postgres %s %s schema at %04d %s role %s",
		ui.Sep, pgVersion(role.ServerVersionNum), ui.Sep, st.Applied, ui.Sep, role.User)))
	// 4b. The seal key: load, check and register it, or refuse to start (S04 §3.7).
	signer, err := loadSealKey(ctx, e, cfg, db, log)
	if err != nil {
		return err
	}
	sk := &serveKey{issuer: strings.TrimSuffix(cfg.BaseURL.String(), "/"), id: signer.KeyID()}

	// 5. People: the mailer, its queue and sign-in (S07).
	led := newLedger(db, signer, cfg, log)
	queue, err := newMailQueue(e, cfg, log)
	if err != nil {
		return err
	}
	queue.Start(context.WithoutCancel(ctx))
	defer queue.Close()
	people, err := newIdentity(cfg, db, led, queue, log)
	if err != nil {
		return err
	}
	// 5a. The Console's pages (S24). In prod a binary built without the Console refuses to start.
	con, err := console.New(console.Options{Prod: prod, Sessions: people, Log: log})
	if errors.Is(err, console.ErrNotBuilt) {
		return &ExitError{Code: ExitFailure, ErrCode: "console_not_built",
			Err:  errors.New("Refusing to start: the Console wasn't built into this binary"),
			Next: "Run make web-build, then make build, and start the new binary."}
	}
	if err != nil {
		return err
	}
	if people.InsecureDevCookies() {
		sc, cc := people.CookieNames()
		log.Info("dev cookies", "session", sc, "csrf", cc, "why", "the base URL is http on localhost, so the cookies drop __Host- and Secure (dev only)")
	}

	// 6. Mount and listen.
	keys, err := newKeyAuth(cfg, db, log)
	if err != nil {
		return err
	}
	// 5b. Policies (S12): the registry rides the same LISTEN connection as Stop (deedglass_policy
	// next to deedglass_identity), so it subscribes before the listener starts.
	pol := newPolicies(db, led, keys, log)
	rctx, stopReg := context.WithCancel(context.WithoutCancel(ctx))
	regDone := make(chan struct{})
	pol.subscribe(rctx)
	go func() { defer close(regDone); _ = pol.reg.Run(rctx) }()
	defer func() { stopReg(); <-regDone }()
	// 5c. The Gate (S13): the pipeline, its wait broker and its worker, which also ride the
	// listener (deedglass_events wakes long polls; deedglass_jobs wakes the worker).
	natives := nativeConnectors(cfg)
	// 5d. Connectors (S16): the egress door, the vault and the registry.
	cw, err := newConnectors(ctx, cfg, db, led, natives, log)
	if err != nil {
		return err
	}
	if cw.vault == nil {
		log.Warn("vault: DEEDGLASS_VAULT_KEY isn't set, so connectors can't store or use credentials")
	}
	pool, err := newPool(cfg, e.Getenv, log, cw.egress)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "mcp_upstream_invalid", Err: err,
			Next: "Fix mcp.upstreams in deedglass.yaml (docs/MCP.md, \"Put the Gate in front of a remote MCP server\")."}
	}
	defer pool.Close()
	// S18 (S16 step 14): remote MCP servers as mcp_http connector rows, credentials in the vault.
	mcpType := upstream.NewType(pool)
	cw.reg.RegisterType(mcpType)
	// S34 (Q01, Q02): a tool the gateway hides is sealed as connector.tool_hidden.
	pool.SetHiddenRecorder(func(ctx context.Context, h upstream.HiddenTool) {
		data := map[string]any{"connector": h.Connector, "tool": h.Tool, "reason": h.Reason, "kind": h.Kind, "digest": h.Digest}
		if h.Field != "" {
			data["field"], data["excerpt"] = h.Field, h.Excerpt
		}
		if h.Pinned != "" {
			data["pinned"] = h.Pinned
		}
		_ = cw.breaker.ToolHidden(ctx, h.Org, h.ConnectorID, h.Sentence, data)
	})
	// 5e. Approvals (S22): the Gate pauses through them, and their notify job and links run beside it.
	// S29: approvals in Slack (the card is posted and edited by the notifier; clicks arrive at
	// POST /v1/slack/interact).
	slackN := &slacknotify.Notifier{Store: slackstore.DB{DB: db}, Conns: cw.reg, BaseURL: cfg.BaseURL.String(), Log: log}
	aps, err := newApprovals(cfg, db, led, queue, log, slackN)
	if err != nil {
		return err
	}
	// 5f. Undo (S23): the planner, the internal resolver the Gate uses for <connector>.undo, and
	// the approval context section.
	planner := undoapi.NewPlanner(db, cw.reg, undoConfig(cfg), log)
	undoapi.RegisterSection()
	sections.RegisterEmail() // S40: email_preview on /approve and in Console › Approvals
	// 5g. Insights (S27): the store, the opt-in anomaly facts the Gate reads (dropped on NOTIFY
	// deedglass_learn), replay jobs and anomaly detection.
	ins := newInsights(db, led, log)
	ins.wireSuggestions(cfg, pol, queue, log) // S28: suggestions, the digest and their schedule
	keys.Listener().Listen(store.LearnChannel, ins.facts.Invalidate, func() { ins.facts.Invalidate("") })
	// 5f'. Observe mode (S50): watched sessions, their serve job, and the session undo (its
	// preview and SQL free; one-step undo Pro, checked by licence.go once at start and daily).
	obs := observe.New(db, led, cfg.Decoder.HeartbeatInterval, log)
	sessionSource := func(ctx context.Context, org ids.OrgID, id string) (undoapi.SessionSubject, error) {
		x, frames, err := obs.Evidence(ctx, org, id)
		if err != nil {
			return undoapi.SessionSubject{}, err
		}
		return observeapi.SubjectOf(x, frames), nil
	}
	pro := undoSessionPro(ctx, e, log)
	gp, err := newGate(cfg, db, led, pol, keys, log, cw.reg, pool, aps, undoapi.Resolver{Reg: cw.reg, Planner: planner, DB: db, Log: log,
		Sessions: sessionSource, SessionUndo: sessionUndoEngine()}, ins.facts)
	if err != nil {
		return err
	}
	ua := &undoapi.API{Planner: planner, DB: db, Policies: pol.svc, Agents: people, Gate: gp}
	oa := &observeapi.API{Service: obs, Planner: planner, DB: db, Agents: people, Gate: gp, Pro: pro.Allowed}
	// 5h. Security review pack (S51): readiness free; the document Pro (licence.go, at start and daily).
	spa := newSecurityPackAPI(ctx, e, cfg, db, led, cw, log)
	// 5i. The weekly summary, the Monday email and the Clients view (S52).
	wk, err := newWeekly(ctx, e, cfg, db, led, queue, people, pro, log)
	if err != nil {
		return err
	}
	aps.Attach(gp)
	gp.SetJobHandler(gate.JobApprovalNotify, aps.NotifyJob)
	gp.SetJobHandler(slacknotify.JobNotifyUpdate, slackN.UpdateJob)
	whs := newWebhooks(cfg, db, led, cw, log)
	whWorker := whs.NewWorker(instanceName())
	mw := newMCP(cfg, gp, natives, pool, cw.reg, mcpType, keys, log)
	serveGate(gp)
	worker := gp.NewWorker(instanceName())
	// 5e. The decoder (S20): reads every witnessed Postgres connector whose lease this instance
	// holds; deedglass_jobs also wakes its runners.
	dec := newDecoder(cfg, db, led, cw, log, l2Hooks(gp))
	cw.decoder = dec
	if dec != nil {
		gp.SetNudge(dec.Nudge) // S21: an action waiting for L2 makes its connector's runner poll fast
	}
	// S24: deedglass_events rides the one LISTEN connection into ledger.Bus, which fans it out: the
	// Gate's long polls and the webhook wake inline, the Console's event hub through its queue. A
	// reconnect reaches all of them as a Gap.
	bus := ledger.NewBus()
	bus.Handle(gp.Broker().OnBus)
	bus.Handle(func(ledger.BusMsg) { whWorker.Wake("") }) // an event may have queued webhook deliveries
	keys.Listener().Listen(ledger.Channel, bus.Notify, bus.Reset)
	hub := console.NewHub(console.HubOptions{Hydrator: console.DBHydrator{Ledger: led, Approve: aps, People: people}, Log: log})
	hubIn, unsubHub := bus.Subscribe(console.BusBuffer)
	hubCtx, stopHub := context.WithCancel(context.WithoutCancel(ctx))
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); _ = hub.Run(hubCtx, hubIn) }()
	defer func() { stopHub(); unsubHub(); <-hubDone }()
	go slackN.Watch(hubCtx, bus) // S29: approval.decided and deed.sealed edit the Slack cards
	keys.Listener().Listen(store.JobsChannel, func(p string) {
		worker.Wake(p)
		if dec != nil {
			dec.NudgeOrg(p)
		}
	}, func() { worker.Wake("") })
	kctx, stopKeys := context.WithCancel(context.WithoutCancel(ctx))
	keysDone := make(chan struct{})
	go func() { defer close(keysDone); keys.Run(kctx) }()
	defer func() { stopKeys(); <-keysDone }()
	wctx, stopWorker := context.WithCancel(context.WithoutCancel(ctx))
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); _ = worker.Run(wctx) }()
	hctx, stopHooks := context.WithCancel(context.WithoutCancel(ctx))
	hooksDone := make(chan struct{})
	go func() { defer close(hooksDone); _ = whWorker.Run(hctx) }()
	defer func() { stopHooks(); <-hooksDone }()
	if dec != nil {
		dctx, stopDec := context.WithCancel(context.WithoutCancel(ctx))
		decDone := make(chan struct{})
		go func() { defer close(decDone); _ = dec.Run(dctx) }()
		defer func() { stopDec(); <-decDone }()
	}
	ictx, stopIns := context.WithCancel(context.WithoutCancel(ctx))
	insDone := make(chan struct{}, 3)
	go func() { _ = ins.runner.Run(ictx); insDone <- struct{}{} }()
	go func() { _ = ins.detector.Run(ictx); insDone <- struct{}{} }()
	go func() { _ = ins.schedule.Run(ictx); insDone <- struct{}{} }()
	defer func() { stopIns(); <-insDone; <-insDone; <-insDone; ins.sugSvc.Suggester.WaitPhrasing() }()
	octx, stopObs := context.WithCancel(context.WithoutCancel(ctx))
	obsDone := make(chan struct{})
	go func() { defer close(obsDone); _ = obs.Run(octx) }()
	defer func() { stopObs(); <-obsDone }()
	wkctx, stopWk := context.WithCancel(context.WithoutCancel(ctx))
	wkDone := make(chan struct{})
	go func() { defer close(wkDone); _ = wk.job.Run(wkctx) }()
	defer func() { stopWk(); <-wkDone }()
	defer func() {
		stopWorker()
		<-workerDone
		cctx, cancel := context.WithTimeout(context.Background(), gate.DefaultDrain)
		defer cancel()
		_ = gp.Close(cctx)
	}()

	api := httpx.New(httpx.Options{
		Logger: log, Prod: prod, BaseURL: cfg.BaseURL, TrustedProxies: cfg.TrustedProxies,
		Idempotency: idemStore{db}, Sessions: people, Keys: keys,
	})
	api.Metrics().Collect(policyMetrics(pol.reg))
	api.Metrics().Collect(gp.Metrics().Collect)
	api.Metrics().Collect(eventMetrics(hub, bus))
	if dec != nil {
		api.Metrics().Collect(dec.Metrics().Collect)
	}
	// S25: GET /v1/overview, with S11's usage counters and the decoder's lag threshold.
	ov := console.NewOverview(console.OverviewOptions{Ledger: led, Usage: console.UsageFromStore(policystore.NewUsage(db)),
		SlotLagAlertBytes: int64(cfg.SlotLagAlertMB) << 20, Log: log})
	api.Mount(serveRoutes(api, db, ms, sk, led, people, keys, pol, gp, mw, cw, aps, whs, ua, &liveWiring{hub: hub, con: con, ov: ov, ins: ins, obs: oa, spa: spa, wk: wk,
		slack: &slacknotify.Handler{Store: slackstore.DB{DB: db}, Conns: cw.reg, Approvals: aps, Log: log}})...)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "listen_failed", Err: fmt.Errorf("Can't listen on %s: %s", cfg.Listen, lastPart(err)),
			Next: "Another process may be using the port. Set DEEDGLASS_LISTEN to a free address."}
	}
	hs := newHTTPServer(api, log)
	hs.RegisterOnShutdown(hub.Close) // open event streams end when shutdown starts; clients reconnect elsewhere
	var metricsSrv *http.Server
	if cfg.MetricsListen != "" {
		mln, err := net.Listen("tcp", cfg.MetricsListen)
		if err != nil {
			ln.Close()
			return &ExitError{Code: ExitFailure, ErrCode: "listen_failed", Err: fmt.Errorf("Can't listen on %s for metrics: %s", cfg.MetricsListen, lastPart(err)),
				Next: "Set DEEDGLASS_METRICS_LISTEN to a free address, or unset it."}
		}
		metricsSrv = &http.Server{Handler: api.Metrics().Handler(), ReadHeaderTimeout: 5 * time.Second, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
		go func() { _ = metricsSrv.Serve(mln) }()
	}
	serveListening(ln.Addr())

	// 7. The summary.
	fmt.Fprintln(e.Stdout, s.OKLine("Listening on http://"+ln.Addr().String()))
	if metricsSrv != nil {
		fmt.Fprintln(e.Stdout, s.OKLine("Metrics on http://"+cfg.MetricsListen+"/metrics"))
	}
	log.Info("started", "listen", ln.Addr().String(), "startup_ms", time.Since(started).Milliseconds(), "env", string(cfg.Env))

	// 8. The prune jobs: idempotency keys, and old sign-in tokens and sessions (personal data).
	pctx, stopPrune := context.WithCancel(context.WithoutCancel(ctx))
	defer stopPrune()
	go pruneLoop(pctx, db, log)
	go nonceLoop(pctx, db, log)

	drain := time.Duration(0)
	if prod {
		drain = prodDrain
	}
	sigs, stopSigs := serveSignals()
	defer stopSigs()
	res, err := serveUntilStopped(ctx, hs, api, ln, sigs, drain, log)
	if metricsSrv != nil {
		_ = metricsSrv.Close()
	}
	if err != nil {
		return err
	}
	if res.cutOff > 0 {
		fmt.Fprintln(e.Stdout, s.FailLine(fmt.Sprintf("Stopped in %.1f s %s %d %s cut off after %d s", res.took.Seconds(), ui.Sep, res.cutOff,
			plural(res.cutOff, "request was", "requests were"), int(shutdownTimeout.Seconds()))))
		return &ExitError{Code: ExitFailure, Err: errors.New("requests were cut off"), printed: true}
	}
	fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("Stopped cleanly in %.1f s %s 0 requests cut off", res.took.Seconds(), ui.Sep)))
	return nil
}

func newHTTPServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // per route, through http.ResponseController
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// serveKey is the loaded seal key as the routes need it.
type serveKey struct {
	issuer string // DEEDGLASS_BASE_URL, keys.json's issuer
	id     string
}

// serveRoutes is everything `serve` mounts. Later sessions add their routes here; the cmd tests
// run httpx.CheckSpec on this list, so api/openapi.yaml must document each one. db and sk are nil
// in route-table tests.
func serveRoutes(api *httpx.Server, db *store.DB, ms []migrate.Migration, sk *serveKey, led *ledger.Ledger, people *identity.Service,
	keys *identity.KeyAuth, pol *policies, gp *gate.Pipeline, mw *mcpWiring, cw *connectors, aps *approve.Service, whs *webhook.Service,
	ua *undoapi.API, lw *liveWiring) []httpx.Route {
	deps := httpx.SystemDeps{Spec: apiSpec()}
	if pol == nil {
		pol = &policies{svc: policy.NewService(nil)}
	} else {
		deps.Policies = func() string { return pol.reg.Health().Status }
	}
	issuer := "http://127.0.0.1:8080"
	if db != nil {
		deps.Health = healthSource{db: db, ms: ms}
	}
	if sk != nil {
		issuer = sk.issuer
		if db != nil {
			deps.SealKey = sealKeyCheck(db, sk.id)
		}
	}
	if cw != nil && cw.decoder != nil {
		deps.Decoder = decoderHealth(cw.decoder)
	}
	if lw != nil && lw.ins != nil && lw.ins.detector != nil {
		deps.Insights = lw.ins.detector.Health
	}
	routes := api.SystemRoutes(deps)
	routes = append(routes, api.WellKnownRoutes(keySetSource(db, issuer), nil)...)
	if led == nil {
		led = ledger.New(db, nil, nil, ledger.WithIssuer(issuer))
	}
	// S06's Ledger routes; GET /v1/actions/{id} takes the Gate's long poll (S13).
	var wait ledger.Waiter
	if gp != nil {
		wait = gp.Wait
	}
	routes = append(routes, ledger.RoutesWith(led, wait)...)
	// S13: the Gate.
	routes = append(routes, gate.Routes(gp)...)
	// S14: tool visibility for MCP wrappers.
	routes = append(routes, gate.ToolRoutes(gp)...)
	if people == nil {
		people = routeListingIdentity(issuer)
	}
	routes = append(routes, people.Routes()...)
	routes = append(routes, people.AgentRoutes()...)
	// S09: intent keys, the conformance check, and MCP authorization readiness.
	routes = append(routes, people.IntentRoutes(identity.NewIntents(db, keys, nil))...)
	base, err := url.Parse(issuer)
	if err != nil {
		panic(err) // DEEDGLASS_BASE_URL was validated by config
	}
	// S15: the MCP gateway (S09's stubs when it isn't wired) and GET /v1/tools.
	routes = append(routes, mcpRoutes(identity.NewMCPAuth(base), mw)...)
	routes = append(routes, toolsRoutes(mw)...)
	// S16: connectors, write-only credentials, the connection test and the types.
	routes = append(routes, connectorRoutes(db, led, cw, nil)...)
	// S22: approvals, approval links, the /approve page and webhooks.
	routes = append(routes, approve.Routes(aps)...)
	routes = append(routes, approve.PageRoutes()...)
	routes = append(routes, webhook.Routes(whs)...)
	// S23: undo (preview and run).
	routes = append(routes, undoapi.Routes(ua)...)
	// S24: the Console's pages and the live event stream.
	if lw == nil {
		lw = &liveWiring{}
	}
	// S50: observe mode (a nil API answers 503 in route-table tests).
	routes = append(routes, observeapi.Routes(lw.obs)...)
	// S51: the security review pack (a nil API answers 503 in route-table tests).
	routes = append(routes, securitypackapi.Routes(lw.spa)...)
	// S52: the weekly summary, its email setting and off page, and the Clients view (nil answers 503).
	if lw.wk == nil {
		lw.wk = &weeklyWiring{}
	}
	routes = append(routes, weeklyapi.Routes(lw.wk.api)...)
	routes = append(routes, agency.Routes(lw.wk.agency)...)
	if lw.con == nil {
		c, err := console.New(console.Options{Sessions: people})
		if err != nil {
			panic(err) // dev mode never refuses
		}
		lw.con = c
	}
	routes = append(routes, lw.con.Routes()...)
	routes = append(routes, console.EventRoutes(console.EventsOptions{Hub: lw.hub, Recheck: api.Recheck})...)
	// S25: the Overview's numbers (a nil Ledger answers 503 in route-table tests).
	if lw.ov == nil {
		lw.ov = console.NewOverview(console.OverviewOptions{})
	}
	routes = append(routes, lw.ov.Routes()...)
	// S27: Insights (replays, shadow, stats, anomalies, settings); a nil service answers 503.
	var insSvc *learn.Service
	if lw.ins != nil {
		insSvc = lw.ins.svc
	}
	routes = append(routes, learn.Routes(insSvc)...)
	// S28: suggestions (generate, accept into shadow, reject); a nil service answers 503.
	var sugSvc *learn.SuggestionService
	if lw.ins != nil {
		sugSvc = lw.ins.sugSvc
	}
	routes = append(routes, learn.SuggestionRoutes(sugSvc)...)
	// S29: Slack interactivity (Approve and Deny on the approval card).
	sh := lw.slack
	if sh == nil {
		sh = &slacknotify.Handler{}
	}
	routes = append(routes, sh.Routes()...)
	// S12: policies, versions, activation deeds, lint, the dry run and the test runner.
	return append(routes, policyapi.Routes(pol.svc, policyapi.Options{})...)
}

// liveWiring is S24's: the Console's page handler and the event hub, and S25's Overview (nil in
// route-table tests).
type liveWiring struct {
	hub   *console.Hub
	con   *console.Console
	ov    *console.Overview
	ins   *insights
	slack *slacknotify.Handler // S29
	obs   *observeapi.API      // S50
	spa   *securitypackapi.API // S51
	wk    *weeklyWiring        // S52
}

// insights is S27's wiring.
type insights struct {
	store    *learnstore.Store
	facts    *learn.FactsCache
	runner   *learn.Runner
	detector *learn.Detector
	svc      *learn.Service
	// S28
	sugSvc   *learn.SuggestionService
	digest   *learn.Digester
	schedule *learn.Scheduler
}

func newInsights(db *store.DB, led *ledger.Ledger, log *slog.Logger) *insights {
	ls := learnstore.New(db, led)
	facts := &learn.FactsCache{Store: ls, Log: log}
	runner := &learn.Runner{Engine: &learn.Engine{Src: ls.Inputs}, Jobs: ls, Worker: instanceName(), Log: log}
	det := &learn.Detector{Store: ls, Log: log}
	return &insights{store: ls, facts: facts, runner: runner, detector: det,
		svc: &learn.Service{Store: ls, Inputs: ls.Inputs, Runner: runner, Facts: facts, Log: log}}
}

// eventMetrics writes the live stream's gauges and counters (S24).
func eventMetrics(hub *console.Hub, bus *ledger.Bus) httpx.Collector {
	return func(w io.Writer) {
		st, bs := hub.Stats(), bus.Stats()
		fmt.Fprintf(w, "# HELP deedglass_events_streams Open GET /v1/events streams on this instance.\n# TYPE deedglass_events_streams gauge\ndeedglass_events_streams %d\n", st.Streams)
		fmt.Fprintf(w, "# HELP deedglass_events_total Events the hub appended to an org's ring.\n# TYPE deedglass_events_total counter\ndeedglass_events_total %d\n", st.Events)
		fmt.Fprintf(w, "# HELP deedglass_events_resyncs_total Resyncs sent (expired or foreign cursors, bus gaps, lagged streams).\n# TYPE deedglass_events_resyncs_total counter\ndeedglass_events_resyncs_total %d\n", st.Resyncs+st.Lagged)
		fmt.Fprintf(w, "# HELP deedglass_events_hydrate_errors_total Hydration reads that failed (events went out thin).\n# TYPE deedglass_events_hydrate_errors_total counter\ndeedglass_events_hydrate_errors_total %d\n", st.HydrateErrors)
		fmt.Fprintf(w, "# HELP deedglass_bus_dropped_total Bus messages dropped for a full subscriber (each turns into a Gap).\n# TYPE deedglass_bus_dropped_total counter\ndeedglass_bus_dropped_total %d\n", bs.Dropped)
	}
}

// policies is S12's wiring: the registry every instance keeps, the service the routes call, and
// the change feed on the instance's one LISTEN connection.
type policies struct {
	reg     *policy.Registry
	svc     *policy.Service
	changes *policystore.Changes
}

func newPolicies(db *store.DB, led *ledger.Ledger, keys *identity.KeyAuth, log *slog.Logger) *policies {
	repo := policystore.NewRepo(db, led)
	ch := policystore.NewChanges(keys.Listener())
	reg := policy.NewRegistry(repo, ch, policy.WithRegistryLogger(log))
	return &policies{reg: reg, svc: policy.NewService(repo, policy.WithRegistry(reg)), changes: ch}
}

// subscribe registers deedglass_policy on the listener before it connects.
func (p *policies) subscribe(ctx context.Context) { _, _, _ = p.changes.Subscribe(ctx) }

// policyMetrics writes deedglass_policy_reloads_total, deedglass_policy_load_failures_total and
// deedglass_policy_active_orgs (S12 §3.5).
func policyMetrics(reg *policy.Registry) httpx.Collector {
	return func(w io.Writer) {
		st := reg.Stats()
		fmt.Fprintf(w, "# HELP deedglass_policy_reloads_total Policy loads into the registry (cold, on NOTIFY or on the safety poll).\n# TYPE deedglass_policy_reloads_total counter\ndeedglass_policy_reloads_total %d\n", st.Reloads)
		fmt.Fprintf(w, "# HELP deedglass_policy_load_failures_total Policy loads that failed their checks (the org then refuses every action).\n# TYPE deedglass_policy_load_failures_total counter\ndeedglass_policy_load_failures_total %d\n", st.LoadFailures)
		fmt.Fprintf(w, "# HELP deedglass_policy_active_orgs Orgs whose policies this instance holds.\n# TYPE deedglass_policy_active_orgs gauge\ndeedglass_policy_active_orgs %d\n", st.ActiveOrgs)
	}
}

// newGate is the action pipeline (S13): the fake connector in dev only (S16 reserves the name
// outside dev), S12's registry, S11's usage store, S09's intent checker and S08's Stop guard.
func newGate(cfg *config.Config, db *store.DB, led *ledger.Ledger, pol *policies, keys *identity.KeyAuth, log *slog.Logger,
	reg *connector.Registry, pool *upstream.Pool, aps *approve.Service, internal gate.Resolver, anomalies gate.AnomalySource) (*gate.Pipeline, error) {
	g := cfg.Gate
	var approvals gate.Approvals
	if aps != nil {
		approvals = aps
	}
	return gate.New(gate.Options{DB: db, Ledger: led, Policies: pol.reg, Usage: policystore.NewUsage(db), Approvals: approvals,
		Intents: identity.NewIntents(db, keys, nil), Guard: keys, Resolver: upstream.Resolver{Native: reg, Pool: pool}, Internal: internal, Logger: log,
		Anomalies: anomalies,
		Config: gate.Config{WaitTimeout: g.WaitTimeout, ExecuteTimeout: g.ExecuteTimeout, EdgeResultTimeout: g.EdgeResultTimeout,
			MaxArgsBytes: g.MaxArgsBytes, MaxResultBytes: g.MaxResultBytes, WorkerConcurrency: g.WorkerConcurrency,
			WorkerMaxAttempts: g.WorkerMaxAttempts,
			Witness: gate.WitnessConfig{InlineBudget: cfg.Witness.InlineBudget, ReadbackTimeout: cfg.Witness.ReadbackTimeout,
				MaxDetailBytes: cfg.Witness.MaxDetailBytes}}})
}

// undoConfig is undo.* (S23).
func undoConfig(cfg *config.Config) undoapi.Config {
	u := cfg.Undo
	return undoapi.Config{MaxRows: u.MaxRows, MaxAge: u.MaxAge, LockTimeout: u.LockTimeout, StatementTimeout: u.StatementTimeout,
		PreviewSteps: u.PreviewSteps}
}

// newApprovals is S22's approvals: the context snapshot, eligibility, links, four-eyes and the
// approval email through S07's mail queue.
func newApprovals(cfg *config.Config, db *store.DB, led *ledger.Ledger, q *mailer.Queue, log *slog.Logger, extra ...notify.Notifier) (*approve.Service, error) {
	a := cfg.Approvals
	return approve.New(approve.Options{DB: db, Ledger: led, BaseURL: cfg.BaseURL, Logger: log,
		Notifiers: append([]notify.Notifier{&notify.Email{Queue: q, SkipReserved: cfg.ResendAPIKey.IsSet()}}, extra...),
		Config: approve.Config{DefaultExpiresIn: a.DefaultExpiresIn, EstimateTimeout: a.EstimateTimeout, ContextMaxBytes: a.ContextMaxBytes,
			LinkMaxTTL: a.LinkMaxTTL, LinkStatusWindow: a.LinkStatusWindow, MaxRecipients: a.MaxRecipients}})
}

// newWebhooks is S22's webhooks: signing secrets in the vault (no key ring, no webhooks), every
// delivery through the egress door.
func newWebhooks(cfg *config.Config, db *store.DB, led *ledger.Ledger, cw *connectors, log *slog.Logger) *webhook.Service {
	w := cfg.Webhooks
	o := webhook.Options{DB: db, Ledger: led, Egress: cw.egress, Logger: log,
		Config: webhook.Config{Timeout: w.Timeout, MaxAttempts: w.MaxAttempts, DisableAfter: w.DisableAfter, MaxEndpoints: w.MaxEndpoints}}
	if cw.ring != nil {
		o.KEK = cw.ring
	}
	return webhook.New(o)
}

// instanceName names this process in witness_jobs.locked_by: the host name and the pid.
func instanceName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "deedglass"
	}
	if len(h) > 64 {
		h = h[:64]
	}
	return fmt.Sprintf("%s-%d", h, os.Getpid())
}

// newKeyAuth is the API-key authenticator and Stop guard (S08). serve runs its listener.
func newKeyAuth(cfg *config.Config, db *store.DB, log *slog.Logger) (*identity.KeyAuth, error) {
	pepper, err := cfg.Pepper()
	if err != nil {
		return nil, err
	}
	return identity.NewKeyAuth(identity.KeyAuthOptions{DB: db, Pepper: pepper, Logger: log})
}

// routeListingIdentity is an identity.Service with no database, for route-table tests.
func routeListingIdentity(base string) *identity.Service {
	u, _ := url.Parse(base)
	s, err := identity.New(identity.Options{BaseURL: u, Pepper: make([]byte, 32)})
	if err != nil {
		panic(err)
	}
	return s
}

// newMailQueue picks the mailer (S07 §3.9): Resend when its key is set; else the log mailer in dev;
// else, in prod, none (with one warning: self-hosters without email use deedglass org login-link).
func newMailQueue(e *Env, cfg *config.Config, log *slog.Logger) (*mailer.Queue, error) {
	var m mailer.Mailer
	switch {
	case cfg.ResendAPIKey.IsSet():
		r, err := mailer.NewResend(mailer.ResendOptions{APIKey: cfg.ResendAPIKey.Reveal(), From: cfg.MailFrom, Logger: log})
		if err != nil {
			return nil, err
		}
		m = r
	case cfg.Env != config.Prod:
		l, err := mailer.NewLog(e.Stderr, false)
		if err != nil {
			return nil, err
		}
		m = l
	default:
		log.Warn("no mailer: DEEDGLASS_RESEND_API_KEY isn't set, so sign-in and invite emails aren't sent. Use deedglass org login-link.")
		m = mailer.None{}
	}
	return mailer.NewQueue(m, mailer.QueueOptions{Logger: log}), nil
}

// newIdentity is sign-in, sessions and members for serve.
func newIdentity(cfg *config.Config, db *store.DB, led *ledger.Ledger, q identity.MailQueue, log *slog.Logger) (*identity.Service, error) {
	pepper, err := cfg.Pepper()
	if err != nil {
		return nil, err
	}
	return identity.New(identity.Options{DB: db, Ledger: led, Mail: q, Pepper: pepper, BaseURL: cfg.BaseURL,
		Signup: cfg.Signup, Prod: cfg.Env == config.Prod, Logger: log})
}

// newLedger is the Ledger serve and the DB-direct CLI commands share. signer may be nil (reads only).
func newLedger(db *store.DB, signer deed.Signer, cfg *config.Config, log *slog.Logger) *ledger.Ledger {
	bi := buildInfo()
	opts := []ledger.Option{ledger.WithIssuer(strings.TrimSuffix(cfg.BaseURL.String(), "/")), ledger.WithLogger(log),
		ledger.WithGenerator(archive.Generator{Name: "deedglass", Version: bi.Version, Commit: bi.Commit}),
		ledger.WithDev(cfg.Env != config.Prod), ledger.WithUndoMaxAge(cfg.Undo.MaxAge)}
	if signer == nil {
		return ledger.New(db, nil, nil, opts...)
	}
	return ledger.New(db, deed.NewSealer(signer), signer, opts...)
}

func apiSpec() []byte { return api.Spec }

type stopResult struct {
	took   time.Duration
	cutOff int
}

// serveUntilStopped serves until the first signal (or ctx is done), then drains: health reports
// draining, requests are still served for drain, then Shutdown waits up to 20 s for in-flight
// requests. A second signal exits 1 at once.
func serveUntilStopped(ctx context.Context, hs *http.Server, api *httpx.Server, ln net.Listener, sigs <-chan os.Signal, drain time.Duration, log *slog.Logger) (stopResult, error) {
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		return stopResult{}, fmt.Errorf("the server stopped: %w", err)
	case <-sigs:
	case <-ctx.Done():
	}
	begin := time.Now()
	api.StartDraining()
	log.Info("draining", "drain_ms", drain.Milliseconds(), "in_flight", api.InFlight())
	go func() {
		<-sigs
		log.Warn("second signal: exiting now")
		serveExit(1)
	}()
	if drain > 0 {
		time.Sleep(drain)
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	res := stopResult{}
	if err := hs.Shutdown(sctx); err != nil {
		res.cutOff = int(api.InFlight())
		_ = hs.Close()
	}
	res.took = time.Since(begin)
	log.Info("stopped", "took_ms", res.took.Milliseconds(), "cut_off", res.cutOff)
	return res, nil
}

func pruneLoop(ctx context.Context, db *store.DB, log *slog.Logger) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pruneOnce(ctx, db, log)
		}
	}
}

// identityPruneBatch is how many sign-in tokens and sessions one prune run deletes at most.
const identityPruneBatch = 5000

// pruneOnce runs every prune job once.
func pruneOnce(ctx context.Context, db *store.DB, log *slog.Logger) {
	if n, err := db.PruneIdempotencyKeys(ctx); err != nil {
		log.Warn("pruning idempotency keys failed", "error", err.Error())
	} else if n > 0 {
		log.Info("pruned idempotency keys", "deleted", n)
	}
	if n, err := db.PreAuth().PruneIdentity(ctx, identityPruneBatch); err != nil {
		log.Warn("pruning sign-in tokens and sessions failed", "error", err.Error())
	} else if n > 0 {
		log.Info("pruned sign-in tokens and sessions", "deleted", n)
	}
}

// nonceLoop prunes signed-intent nonces every minute, in batches of 10,000 (S09 §3.7), until a
// batch comes back short.
func nonceLoop(ctx context.Context, db *store.DB, log *slog.Logger) {
	t := time.NewTicker(nonceEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for range 10 {
				n, err := db.PreAuth().PruneIntentNonces(ctx, noncePruneBatch)
				if err != nil {
					if ctx.Err() == nil {
						log.Warn("pruning intent nonces failed", "error", err.Error())
					}
					break
				}
				if n > 0 {
					log.Debug("pruned intent nonces", "deleted", n)
				}
				if n < noncePruneBatch {
					break
				}
			}
			// S29: Slack signatures past their replay window.
			if n, err := db.PreAuth().PruneInboundReplay(ctx, noncePruneBatch); err != nil {
				if ctx.Err() == nil {
					log.Warn("pruning Slack replay digests failed", "error", err.Error())
				}
			} else if n > 0 {
				log.Debug("pruned Slack replay digests", "deleted", n)
			}
		}
	}
}

// Nonce pruning (S09 §3.7).
const (
	nonceEvery      = time.Minute
	noncePruneBatch = 10_000
)

// serveMigrate applies pending migrations in dev (serve --migrate), as the migrator role.
func serveMigrate(ctx context.Context, e *Env, cfg *config.Config) error {
	url, fallback, err := cfg.MigrateURL(config.Secret{})
	var ps *config.Problems
	if errors.As(err, &ps) {
		return configProblems(e, ps, nil)
	}
	if fallback {
		fmt.Fprintln(e.Stderr, e.Style.UnknownLine("Using DEEDGLASS_DATABASE_URL for migrations (dev only)."))
	}
	m, err := openMigrator(ctx, url)
	if err != nil {
		var ce *connectError
		if errors.As(err, &ce) {
			return &ExitError{Code: ExitFailure, ErrCode: "database_unreachable", Err: ce,
				Next: "Check DEEDGLASS_MIGRATE_DATABASE_URL, or run deedglass migrate up yourself."}
		}
		return err
	}
	defer m.Close()
	res, err := m.Up(ctx, migrate.UpOptions{Env: cfg.Env, AppVersion: buildInfo().Version})
	if err != nil {
		return migrateErr(err, m.Target())
	}
	if len(res.Applied) > 0 {
		fmt.Fprintln(e.Stdout, e.Style.OKLine(fmt.Sprintf("Migrations %s %d applied in %.2f s", ui.Sep, len(res.Applied), res.Duration.Seconds())))
	}
	return nil
}

func migrationState(rows []migrate.Row, ms []migrate.Migration) httpx.MigrationState {
	st := httpx.MigrationState{Expected: len(ms)}
	for _, r := range rows {
		switch r.State {
		case migrate.StateApplied:
			st.Applied++
		case migrate.StateChanged, migrate.StateUnknown:
			st.Applied++
			st.Changed = true
		}
	}
	return st
}

// --- adapters: store ⇄ httpx (store never imports httpx) ---

type healthSource struct {
	db *store.DB
	ms []migrate.Migration
}

func (h healthSource) PingDatabase(ctx context.Context) error { return h.db.Ping(ctx) }
func (h healthSource) Migrations(ctx context.Context) (httpx.MigrationState, error) {
	rows, err := h.db.MigrationRows(ctx, h.ms)
	if err != nil {
		return httpx.MigrationState{}, err
	}
	return migrationState(rows, h.ms), nil
}

type idemStore struct{ db *store.DB }

func idemKey(s httpx.IdemScope) store.IdemKey {
	return store.IdemKey{OrgID: s.OrgID, Principal: s.Principal, Key: s.Key, Method: s.Method, Route: s.Route}
}

func (i idemStore) Begin(ctx context.Context, s httpx.IdemScope, fp string, lockFor time.Duration) (httpx.Outcome, *httpx.StoredResponse, error) {
	out, r, err := i.db.IdemBegin(ctx, idemKey(s), fp, lockFor)
	if err != nil {
		return 0, nil, err
	}
	var sr *httpx.StoredResponse
	if r != nil {
		sr = &httpx.StoredResponse{Status: r.Status, Header: r.Headers, Body: r.Body}
	}
	switch out {
	case store.IdemStarted:
		return httpx.Started, nil, nil
	case store.IdemReplay:
		return httpx.Replay, sr, nil
	case store.IdemInProgress:
		return httpx.InProgress, nil, nil
	case store.IdemMismatch:
		return httpx.Mismatch, nil, nil
	}
	return 0, nil, fmt.Errorf("store returned idempotency outcome %d", out)
}

func (i idemStore) Complete(ctx context.Context, s httpx.IdemScope, r httpx.StoredResponse) error {
	return i.db.IdemComplete(ctx, idemKey(s), store.IdemResponse{Status: r.Status, Headers: r.Header, Body: r.Body})
}

func (i idemStore) Release(ctx context.Context, s httpx.IdemScope) error {
	return i.db.IdemRelease(ctx, idemKey(s))
}

// --- helpers ---

// targetOf describes a Postgres URL for messages, without the password.
func targetOf(u config.Secret) Target {
	cc, err := pgx.ParseConfig(u.Reveal())
	if err != nil {
		return Target{Host: "?", User: "?"}
	}
	return Target{Host: cc.Host, Port: cc.Port, Database: cc.Database, User: cc.User}
}

func roleProblem(r store.RoleReport) string {
	switch {
	case r.Superuser:
		return "is a superuser"
	case r.BypassRLS:
		return "has BYPASSRLS"
	default:
		return "owns schema deedglass"
	}
}

func devWhy(r store.RoleReport) string {
	if r.Superuser || r.BypassRLS {
		return "row-level security is off"
	}
	return "it could change the schema"
}

// pgVersion turns server_version_num (180006) into "18.6".
func pgVersion(n int) string {
	if n <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%d.%d", n/10000, n%10000)
}

func lastPart(err error) string {
	s := err.Error()
	for i := len(s) - 2; i > 0; i-- {
		if s[i] == ':' && s[i+1] == ' ' {
			return s[i+2:]
		}
	}
	return s
}

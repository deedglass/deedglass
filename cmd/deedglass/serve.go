package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/deedglass/deedglass/internal/buildinfo"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/migrate"
	"github.com/deedglass/deedglass/internal/store/migrations"
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
	log := httpx.NewLogger(e.Stderr, prod, cfg.LogLevel)
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
	if people.InsecureDevCookies() {
		sc, cc := people.CookieNames()
		log.Info("dev cookies", "session", sc, "csrf", cc, "why", "the base URL is http on localhost, so the cookies drop __Host- and Secure (dev only)")
	}

	// 6. Mount and listen.
	api := httpx.New(httpx.Options{
		Logger: log, Prod: prod, BaseURL: cfg.BaseURL, TrustedProxies: cfg.TrustedProxies,
		Idempotency: idemStore{db}, Sessions: people,
	})
	api.Mount(serveRoutes(api, db, ms, sk, led, people)...)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return &ExitError{Code: ExitFailure, ErrCode: "listen_failed", Err: fmt.Errorf("Can't listen on %s: %s", cfg.Listen, lastPart(err)),
			Next: "Another process may be using the port. Set DEEDGLASS_LISTEN to a free address."}
	}
	hs := newHTTPServer(api, log)
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
func serveRoutes(api *httpx.Server, db *store.DB, ms []migrate.Migration, sk *serveKey, led *ledger.Ledger, people *identity.Service) []httpx.Route {
	deps := httpx.SystemDeps{Spec: apiSpec()}
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
	routes := api.SystemRoutes(deps)
	routes = append(routes, api.WellKnownRoutes(keySetSource(db, issuer), nil)...)
	if led == nil {
		led = ledger.New(db, nil, nil, ledger.WithIssuer(issuer))
	}
	routes = append(routes, ledger.Routes(led)...)
	if people == nil {
		people = routeListingIdentity(issuer)
	}
	return append(routes, people.Routes()...)
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
		ledger.WithDev(cfg.Env != config.Prod)}
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

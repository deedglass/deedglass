//go:build integration

package decoder_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var bg = context.Background()

// The package holds pgtest's cluster-wide lock for its whole run: it rotates the deedglass_*
// roles' passwords on the shared throwaway cluster.
var (
	lockOnce sync.Once
	unlock   func()
)

func TestMain(m *testing.M) {
	code := m.Run()
	if unlock != nil {
		unlock()
	}
	os.Exit(code)
}

func lockRoles(t *testing.T, admin string) {
	t.Helper()
	lockOnce.Do(func() {
		u, err := pgtest.Lock(bg, admin)
		if err != nil {
			t.Fatalf("lock: %v", err)
		}
		unlock = u
	})
}

// rig is one test's world: a control-plane database with an org and a witnessed postgres
// connector, a customer database set up by S17's planner, and a decoder supervisor.
type rig struct {
	t       *testing.T
	e       *storetest.Env
	led     *ledger.Ledger
	org     ids.OrgID
	con     ids.ConnectorID
	name    string
	cust    *pgtest.DB
	sup     *decoder.Supervisor
	cfg     decoder.Config
	cancel  context.CancelFunc
	done    chan struct{}
	logs    *safeBuf
	hooks   *hookLog
	faults  decoder.Faults
	events  *eventLog
	worker  string
	sens    []string
	watched []string
}

type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type hookLog struct {
	mu      sync.Mutex
	bound   []string
	missing []string
	lost    []string
}

func (h *hookLog) snapshot() (bound, missing, lost []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.bound...), append([]string{}, h.missing...), append([]string{}, h.lost...)
}

type rigOpts struct {
	adminURL string // the customer cluster's superuser URL ("" = the shared throwaway cluster)
	dbName   string
	cfg      func(c *decoder.Config)
	noStart  bool
	worker   string
}

func testConfig() decoder.Config {
	c := decoder.DefaultConfig()
	c.PollActive, c.PollIdle = 20*time.Millisecond, 50*time.Millisecond
	c.HeartbeatInterval, c.HeartbeatDeadline = time.Second, time.Minute
	c.ReconcileInterval = time.Hour
	c.HaltRetry = 2 * time.Second
	return c
}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	shared := pgtest.AdminURL(t)
	lockRoles(t, shared)
	if o.adminURL == "" {
		o.adminURL = shared
	}
	if o.dbName == "" {
		o.dbName = "deedglass_it_dec_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
		if len(o.dbName) > 60 {
			o.dbName = o.dbName[:60]
		}
	}
	if o.worker == "" {
		o.worker = "it-a"
	}
	r := &rig{t: t, name: "postgres", logs: &safeBuf{}, hooks: &hookLog{}, worker: o.worker,
		sens: []string{"public.customers.email"}, watched: []string{"public.bulk", "public.customers", "public.orders", "public.refunds"}}
	r.cust = pgtest.NewDB(t, o.adminURL, o.dbName)
	r.cust.Init(t, pgtest.Desired())
	r.e = storetest.New(t, storetest.Options{MaxConns: 20})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r.led = ledger.New(r.e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))
	o2, _, _, err := r.e.DB.CreateOrg(bg, store.NewOrg{Name: "Acme", Slug: "acme", OwnerEmail: "owner@acme.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	r.org = o2.ID
	r.con = ids.New[ids.ConnectorKind]()
	cfg := json.RawMessage(pgtest.ConfigJSON)
	sum := sha256.Sum256(cfg)
	if err := r.e.DB.OrgTx(bg, r.org, func(ctx context.Context, tx pgx.Tx) error {
		return store.InsertConnector(ctx, tx, store.Connector{ID: r.con.String(), OrgID: r.org.String(), Type: "postgres", Name: r.name,
			Config: cfg, ConfigHash: "sha256:" + hex.EncodeToString(sum[:]), Status: "active", CreatedBy: "test"})
	}); err != nil {
		t.Fatal(err)
	}
	r.cfg = testConfig()
	if o.cfg != nil {
		o.cfg(&r.cfg)
	}
	r.events = listenEvents(t, r.e.AdminURL)
	if !o.noStart {
		r.start()
	}
	t.Cleanup(func() {
		r.stop()
		r.dropSlots()
		if t.Failed() {
			t.Logf("decoder log:\n%s", tail(r.logs.String(), 6000))
		}
	})
	return r
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func (r *rig) target(ctx context.Context, org ids.OrgID, id ids.ConnectorID) (*postgres.WitnessTarget, string, error) {
	c, err := pgx.ParseConfig(r.cust.WitnessDSN)
	if err != nil {
		return nil, "", err
	}
	return &postgres.WitnessTarget{Conn: c, Enabled: true, Watched: r.watched, Sensitive: r.sens}, r.name, nil
}

func (r *rig) deps(worker string) decoder.Deps {
	return decoder.Deps{DB: r.e.DB, Ledger: r.led, Target: r.target, Worker: worker,
		Log:    slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})).With("worker", worker),
		Faults: r.faults,
		Hooks: decoder.Hooks{
			OnBound: func(_ context.Context, _ ids.OrgID, _ ids.ConnectorID, a string, c decoder.Class, _ decoder.LSN) {
				r.hooks.mu.Lock()
				r.hooks.bound = append(r.hooks.bound, a+":"+string(c))
				r.hooks.mu.Unlock()
			},
			OnMissing: func(_ context.Context, _ ids.OrgID, _ ids.ConnectorID, a string) {
				r.hooks.mu.Lock()
				r.hooks.missing = append(r.hooks.missing, a)
				r.hooks.mu.Unlock()
			},
			SlotLost: func(_ context.Context, _ ids.OrgID, _ ids.ConnectorID, why string) {
				r.hooks.mu.Lock()
				r.hooks.lost = append(r.hooks.lost, why)
				r.hooks.mu.Unlock()
			},
		}}
}

// start runs a supervisor (one instance).
func (r *rig) start() {
	r.t.Helper()
	if r.sup != nil {
		return
	}
	r.sup = decoder.NewSupervisor(r.deps(r.worker), r.cfg)
	ctx, cancel := context.WithCancel(bg)
	r.cancel, r.done = cancel, make(chan struct{})
	go func() { defer close(r.done); _ = r.sup.Run(ctx) }()
}

func (r *rig) stop() {
	if r.sup == nil {
		return
	}
	r.cancel()
	<-r.done
	r.sup = nil
}

func (r *rig) restart() {
	r.stop()
	r.start()
}

func (r *rig) dropSlots() {
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		return
	}
	defer c.Close(bg)
	_, _ = c.Exec(bg, `SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots
	    WHERE database = pg_catalog.current_database() AND NOT active`)
}

// act runs S17's transaction as deedglass_agent: begin_action first, then stmt, then COMMIT.
func (r *rig) act(stmt string, args ...any) string {
	r.t.Helper()
	id := ids.New[ids.ActionKind]().String()
	c, err := pgx.Connect(bg, r.cust.AgentDSN)
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close(bg)
	tx, err := c.Begin(bg)
	if err != nil {
		r.t.Fatal(err)
	}
	agent := ids.New[ids.AgentKind]().String()
	var xid8, lsn string
	if err := tx.QueryRow(bg, `SELECT b.xid8::text, b.marker_lsn::text FROM deedglass.begin_action($1, $2, $3, NULL, $4) AS b`,
		id, r.org.String(), agent, "sha256:"+strings.Repeat("0", 64)).Scan(&xid8, &lsn); err != nil {
		r.t.Fatalf("begin_action: %v", err)
	}
	if stmt != "" {
		if _, err := tx.Exec(bg, stmt, args...); err != nil {
			r.t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := tx.Commit(bg); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// admin runs SQL as the customer database's superuser.
func (r *rig) admin(sql string, args ...any) {
	r.t.Helper()
	c, err := pgx.Connect(bg, r.cust.AdminURL)
	if err != nil {
		r.t.Fatal(err)
	}
	defer c.Close(bg)
	if _, err := c.Exec(bg, sql, args...); err != nil {
		r.t.Fatalf("%s: %v", sql, err)
	}
}

func (r *rig) frames() []store.DecodedFrame {
	r.t.Helper()
	var out []store.DecodedFrame
	if err := r.e.DB.OrgReadTx(bg, r.org, func(ctx context.Context, tx pgx.Tx) error {
		fs, err := store.RecentFrames(ctx, tx, r.con.String(), 50)
		out = fs
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return out
}

// framesFor returns the stored frames that name action (with images).
func (r *rig) framesFor(action string) []store.DecodedFrame {
	r.t.Helper()
	var out []store.DecodedFrame
	if err := r.e.DB.OrgReadTx(bg, r.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = store.FramesForAction(ctx, tx, action)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return out
}

// count counts this connector's frames matching where (a constant SQL condition). It uses the
// pool, so the runner's goroutine may call it too (the E01 fault hook).
func (r *rig) count(where string, args ...any) int {
	var n int
	err := r.e.DB.OrgReadTx(bg, r.org, func(ctx context.Context, tx pgx.Tx) error {
		//deedglass:allow-sql-build test helper: where is a constant written in the tests
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.decoded_txs WHERE connector_id = $1 AND `+where,
			append([]any{r.con.String()}, args...)...).Scan(&n)
	})
	if err != nil {
		r.t.Error(err)
	}
	return n
}

func (r *rig) slot() store.DecoderSlot {
	r.t.Helper()
	var s store.DecoderSlot
	if err := r.e.DB.OrgReadTx(bg, r.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		s, err = store.GetDecoderSlot(ctx, tx, r.con.String(), false)
		if errors.Is(err, store.ErrNotFound) {
			s.Status = "(no row)"
			return nil
		}
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return s
}

// eventually polls f until it returns "" or the timeout passes.
func eventually(t *testing.T, timeout time.Duration, f func() string) {
	t.Helper()
	// The whole integration run shares one cluster with every other package: be patient.
	timeout = max(timeout, 60*time.Second)
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		if last = f(); last == "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v: %s", timeout, last)
}

func (r *rig) waitRunning() {
	r.t.Helper()
	eventually(r.t, 30*time.Second, func() string {
		s := r.slot()
		if s.Status != decoder.StatusRunning && s.Status != decoder.StatusDegraded {
			return "status " + s.Status + " " + deref(s.StatusReason) + " " + string(s.StatusDetail)
		}
		return ""
	})
}

func (r *rig) waitFrame(action, class string) store.DecodedFrame {
	r.t.Helper()
	var f store.DecodedFrame
	eventually(r.t, 30*time.Second, func() string {
		fs := r.framesFor(action)
		for _, x := range fs {
			if x.Class == class {
				f = x
				return ""
			}
		}
		return fmt.Sprintf("no %s frame for %s yet (%d frames)", class, action, len(fs))
	})
	return f
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// eventLog collects deedglass_events payloads on the control plane.
type eventLog struct {
	mu  sync.Mutex
	evs []ledger.Event
}

func listenEvents(t *testing.T, url string) *eventLog {
	t.Helper()
	l := &eventLog{}
	c, err := pgx.Connect(bg, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(bg, "LISTEN deedglass_events"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, err := c.WaitForNotification(ctx)
			if err != nil {
				return
			}
			var ev ledger.Event
			if json.Unmarshal([]byte(n.Payload), &ev) == nil {
				l.mu.Lock()
				l.evs = append(l.evs, ev)
				l.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done; _ = c.Close(bg) })
	return l
}

func (l *eventLog) find(typ string, pred func(ledger.Event) bool) *ledger.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range l.evs {
		if l.evs[i].Type == typ && (pred == nil || pred(l.evs[i])) {
			return &l.evs[i]
		}
	}
	return nil
}

// --- a private throwaway cluster, for crashes and server settings ---

// cluster is a Postgres the test starts itself (Homebrew binaries), so it can kill it or change
// settings that need a restart without touching the shared throwaway cluster other packages use.
type cluster struct {
	bin, data, log string
	port           int
	admin          string
}

func pgBin(t *testing.T) string {
	t.Helper()
	if b := os.Getenv("DEEDGLASS_IT_PGBIN"); b != "" {
		return b
	}
	v := os.Getenv("DEEDGLASS_IT_PG")
	if v == "" {
		v = "18"
	}
	b := "/opt/homebrew/opt/postgresql@" + v + "/bin"
	if _, err := os.Stat(filepath.Join(b, "pg_ctl")); err != nil {
		t.Skipf("no Postgres binaries at %s (set DEEDGLASS_IT_PGBIN): this test starts and kills its own cluster", b)
	}
	return b
}

func freePort(t *testing.T) int {
	t.Helper()
	// 55470–55499, picked by the test name so parallel runs of different tests don't collide.
	h := sha256.Sum256([]byte(t.Name() + os.Getenv("DEEDGLASS_IT_PG")))
	return 55470 + int(h[0])%30
}

func newCluster(t *testing.T, extra ...string) *cluster {
	t.Helper()
	bin := pgBin(t)
	dir, err := os.MkdirTemp("", "dgdec")
	if err != nil {
		t.Fatal(err)
	}
	c := &cluster{bin: bin, data: filepath.Join(dir, "data"), log: filepath.Join(dir, "pg.log"), port: freePort(t)}
	run := func(name string, args ...string) {
		cmd := exec.Command(filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run("initdb", "-D", c.data, "-U", "postgres", "--auth=trust", "-E", "UTF8", "--locale=C")
	conf := fmt.Sprintf("\nlisten_addresses = 'localhost'\nport = %d\nunix_socket_directories = ''\nwal_level = logical\n"+
		"max_replication_slots = 10\nmax_wal_senders = 10\nmax_connections = 60\nfsync = off\n%s\n", c.port, strings.Join(extra, "\n"))
	f, err := os.OpenFile(filepath.Join(c.data, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(conf)
	_ = f.Close()
	c.admin = fmt.Sprintf("postgres://postgres@localhost:%d/postgres?sslmode=disable", c.port)
	c.startPG(t)
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(bin, "pg_ctl"), "-D", c.data, "-m", "immediate", "-w", "stop")
		_ = cmd.Run()
		_ = os.RemoveAll(dir)
	})
	return c
}

func (c *cluster) startPG(t *testing.T) {
	t.Helper()
	cmd := exec.Command(filepath.Join(c.bin, "pg_ctl"), "-D", c.data, "-l", c.log, "-w", "start")
	cmd.Env = append(os.Environ(), "LC_ALL=en_US.UTF-8")
	if out, err := cmd.CombinedOutput(); err != nil {
		b, _ := os.ReadFile(c.log)
		t.Fatalf("pg_ctl start: %v\n%s\n%s", err, out, tail(string(b), 2000))
	}
}

// crash stops the cluster with -m immediate (no checkpoint), then starts it again.
func (c *cluster) crash(t *testing.T) {
	t.Helper()
	cmd := exec.Command(filepath.Join(c.bin, "pg_ctl"), "-D", c.data, "-m", "immediate", "-w", "stop")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_ctl stop: %v\n%s", err, out)
	}
	c.startPG(t)
}

func isPgCode(err error, code string) bool {
	pe, ok := err.(*pgconn.PgError)
	return ok && pe.Code == code
}

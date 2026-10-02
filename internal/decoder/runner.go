package decoder

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Runner statuses (decoder_slots.status; S20 §3.11).
const (
	StatusPending    = "pending"
	StatusConnecting = "connecting"
	StatusRunning    = "running"
	StatusDegraded   = "degraded"
	StatusWaiting    = "waiting"
	StatusHalted     = "halted"
	StatusDropped    = "dropped"
	StatusDisabled   = "disabled"
)

// Reasons a runner halts or degrades (status_reason). Halts that lose evidence call SlotLost.
const (
	ReasonWALLevel               = "wal_level"
	ReasonNotReplication         = "not_replication"
	ReasonPublicationMissing     = "publication_missing"
	ReasonPublicationInvalid     = "publication_invalid"
	ReasonPublicationChanged     = "publication_changed"
	ReasonLedgerFunctionChanged  = "ledger_function_changed"
	ReasonSlotInvalidated        = "slot_invalidated"
	ReasonSlotMissing            = "slot_missing"
	ReasonSlotConflict           = "slot_conflict"
	ReasonSystemChanged          = "system_changed"
	ReasonTimelineChanged        = "timeline_changed"
	ReasonMalformedEvidence      = "malformed_evidence"
	ReasonSessionSettingsChanged = "session_settings_changed"
	ReasonLagHigh                = "lag_high"
	ReasonRestartRetained        = "restart_retained"
	ReasonStalled                = "stalled"
	ReasonPeekSlow               = "peek_slow"
	ReasonCredentials            = "credentials_missing"
	ReasonUnreachable            = "unreachable"
	ReasonHeldElsewhere          = "held_elsewhere"
	ReasonDropped                = "dropped"
	ReasonConnectorDeleted       = "connector_deleted"
	ReasonNotWitnessed           = "not_witnessed"
	ReasonSlotOrphaned           = "slot_orphaned"
)

// Config are the decoder.* settings (S20 §3.15).
type Config struct {
	PollActive        time.Duration // 100ms while an L2 wait is pending
	PollIdle          time.Duration // 1s otherwise
	BatchChanges      int           // upto_nchanges, 5000
	MaxImages         int           // per frame, 1000
	HeartbeatInterval time.Duration // 10s
	HeartbeatDeadline time.Duration // 60s
	Lease             time.Duration // 15s, renewed every 5s
	ReconcileInterval time.Duration // 60s
	Retention         time.Duration // 168h (unattributed and forged_* frames: 30 days)
	LagAlertBytes     int64         // DEEDGLASS_SLOT_LAG_ALERT_MB in bytes
	HaltRetry         time.Duration // 5m
	Paused            func() bool   // DEEDGLASS_DECODER_PAUSE (dev only): runners stop polling
}

// DefaultConfig is S20 §3.15's table.
func DefaultConfig() Config {
	return Config{PollActive: 100 * time.Millisecond, PollIdle: time.Second, BatchChanges: 5000, MaxImages: DefaultMaxImages,
		HeartbeatInterval: 10 * time.Second, HeartbeatDeadline: 60 * time.Second, Lease: 15 * time.Second,
		ReconcileInterval: 60 * time.Second, Retention: 168 * time.Hour, LagAlertBytes: 512 << 20, HaltRetry: 5 * time.Minute}
}

// Hooks are how the Witness (S21) hears from the decoder. They run in the runner's goroutine after
// the evidence is committed; keep them short.
type Hooks struct {
	// OnBound runs once per newly stored bound frame (and publication_gap, marker_count and
	// marker_mismatch frames that name an action).
	OnBound func(ctx context.Context, org ids.OrgID, connector ids.ConnectorID, actionID string, class Class, commitLSN LSN)
	// OnMissing runs when reconcile records a ledger row with no frame (class missing).
	OnMissing func(ctx context.Context, org ids.OrgID, connector ids.ConnectorID, actionID string)
	// SlotLost runs when evidence can no longer arrive for this connector: the slot was
	// invalidated, dropped or went missing, or the runner halted on evidence it can't read.
	// Pending L2 waits on it become gaps.
	SlotLost func(ctx context.Context, org ids.OrgID, connector ids.ConnectorID, reason string)
}

// TargetFunc loads a connector's witness target (the witness DSN and witness.* config) and name.
type TargetFunc func(ctx context.Context, org ids.OrgID, connector ids.ConnectorID) (*postgres.WitnessTarget, string, error)

// Faults are test seams (integration tests only).
type Faults struct {
	// AfterPersist runs after a poll's frames commit and before the slot advances (E01). An error
	// stops the runner there.
	AfterPersist func(connector string) error
}

// Deps are what runners share.
type Deps struct {
	DB      *store.DB
	Ledger  *ledger.Ledger
	Target  TargetFunc
	Hooks   Hooks
	Log     *slog.Logger
	Worker  string
	Now     func() time.Time
	Metrics *Metrics
	Faults  Faults
}

// haltError stops a runner with a precise reason until it is dropped or the retry finds the cause
// gone. lost means evidence may be gone: pending L2 waits become gaps (SlotLost).
type haltError struct {
	reason string
	detail map[string]any
	lost   bool
}

func (h *haltError) Error() string { return "decoder halted: " + h.reason }

func halt(reason string, lost bool, kv ...any) *haltError {
	d := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		d[kv[i].(string)] = kv[i+1]
	}
	return &haltError{reason: reason, detail: d, lost: lost}
}

var (
	errStopped = errors.New("decoder: the runner's lease ended or its slot was dropped")
	errWaiting = errors.New("decoder: another process holds this slot")
)

// Runner reads one connector's slot (S20 §3.9, §3.11). One per witnessed connector, on the
// instance that holds its lease.
type Runner struct {
	org  ids.OrgID
	id   ids.ConnectorID
	slot string
	d    *Deps
	cfg  Config
	log  *slog.Logger

	nudge     chan struct{}
	reconcile chan struct{} // ReconcileNow
	resync    chan struct{} // Resync: the connector was written, so a halted runner retries at once
	freshSlot bool          // this session created its slot: earlier relation shapes don't apply

	mu   sync.Mutex
	view RunnerView

	// per session
	sess     *Session
	src      Source
	target   *postgres.WitnessTarget
	name     string
	systemID string
	timeline int
	hb       *Heartbeat
	shapes   map[uint32]string
	pk       map[uint32]pkEntry
	counters map[string]int64
	sens     map[string]bool
	pub      string

	lastHB        time.Time // last valid heartbeat decoded
	lastEmit      time.Time
	lastTimeline  time.Time
	lastHealth    time.Time
	lastReconcile time.Time
	interval      time.Duration
	pubSeen       PubSnapshot
	degraded      map[string]bool // degrading conditions in force (lag_high, stalled, …)
	statusWritten string          // status + "/" + reason last written
	activeUntil   time.Time       // an L2 wait was nudged: poll fast until then
	lastPeekRows  int
	caughtUpAt    time.Time // when the last drained peek started: the decoder had read everything flushed then
	lastTarget    LSN       // the last checkpoint persisted and advanced to
	recon         *pendingReconcile
}

type pkEntry struct {
	shape string
	cols  []string
}

// RunnerView is a runner's in-process state (tests and the supervisor).
type RunnerView struct {
	Status     string
	Reason     string
	Checkpoint LSN
	Counters   map[string]int64
	Attempt    int
	// Polls are the durations of the last polls that persisted something (at most 4,096; the
	// benchmark reads them).
	Polls []time.Duration
}

func newRunner(d *Deps, cfg Config, org ids.OrgID, id ids.ConnectorID, slot string) *Runner {
	return &Runner{org: org, id: id, slot: slot, d: d, cfg: cfg, nudge: make(chan struct{}, 1), reconcile: make(chan struct{}, 1),
		resync: make(chan struct{}, 1), log: d.Log.With("connector", id.String(), "slot", slot)}
}

// View returns the runner's state.
func (r *Runner) View() RunnerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := r.view
	v.Counters = make(map[string]int64, len(r.view.Counters))
	for k, n := range r.view.Counters {
		v.Counters[k] = n
	}
	v.Polls = append([]time.Duration(nil), r.view.Polls...)
	return v
}

func (r *Runner) setView(f func(v *RunnerView)) {
	r.mu.Lock()
	f(&r.view)
	r.mu.Unlock()
}

// Nudge makes the runner poll now and keeps it on decoder.poll_active for a while: S21 calls it
// when an action starts waiting for L2 evidence.
func (r *Runner) Nudge() {
	r.mu.Lock()
	r.activeUntil = r.d.Now().Add(30 * time.Second)
	r.mu.Unlock()
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

// Resync wakes a halted runner at once (S21): its connector was written (new credentials, a
// changed config, init applied again), so the cause of the halt may be gone. Nudges don't wake a
// halted runner; only connector writes do, so a halt is retried at the pace of real changes.
func (r *Runner) Resync() {
	select {
	case r.resync <- struct{}{}:
	default:
	}
	select {
	case r.nudge <- struct{}{}:
	default:
	}
}

func (r *Runner) active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.d.Now().Before(r.activeUntil)
}

// Run runs until ctx ends or the runner's lease or slot row goes away.
func (r *Runner) Run(ctx context.Context) {
	defer r.d.Metrics.forget(r.id.String())
	attempt := 0
	for ctx.Err() == nil {
		err := r.session(ctx)
		r.closeSession()
		if ctx.Err() != nil || errors.Is(err, errStopped) {
			return
		}
		var h *haltError
		switch {
		case errors.As(err, &h):
			attempt = 0
			if serr := r.setStatus(ctx, StatusHalted, h.reason, h.detail, 0); errors.Is(serr, errStopped) {
				return
			}
			r.log.Error("decoder: halted", "reason", h.reason, "detail", h.detail)
			if h.lost && r.d.Hooks.SlotLost != nil {
				r.d.Hooks.SlotLost(ctx, r.org, r.id, h.reason)
			}
			select { // a Resync from before the halt doesn't count
			case <-r.resync:
			default:
			}
			if !sleepCtx(ctx, r.cfg.HaltRetry, r.resync) {
				return
			}
		case errors.Is(err, errWaiting):
			if !sleepCtx(ctx, 5*time.Second, nil) {
				return
			}
		default:
			attempt++
			wait := backoff(attempt)
			reason := ReasonUnreachable
			if errors.Is(err, postgres.ErrWitnessDSNMissing) || strings.Contains(err.Error(), "credentials_missing") {
				reason = ReasonCredentials
			}
			r.setView(func(v *RunnerView) { v.Attempt = attempt })
			if serr := r.setStatus(ctx, StatusConnecting, reason, map[string]any{"attempt": attempt,
				"next_try_ms": wait.Milliseconds(), "message": errorLine(err)}, 0); errors.Is(serr, errStopped) {
				return
			}
			r.log.Warn("decoder: can't read the slot; retrying", "attempt", attempt, "in", wait.String(), "err", errorLine(err))
			// A connector write (new credentials, a changed config) nudges: retry at once.
			if !sleepCtx(ctx, wait, r.nudge) {
				return
			}
		}
	}
}

// backoff is 1 s · 2ⁿ, at most 60 s, ±20%.
func backoff(attempt int) time.Duration {
	d := time.Second << min(attempt-1, 6)
	if d > time.Minute {
		d = time.Minute
	}
	var b [1]byte
	_, _ = crand.Read(b[:])
	j := 0.8 + 0.4*float64(b[0])/255
	return time.Duration(float64(d) * j)
}

func sleepCtx(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-wake:
	}
	return true
}

// errorLine is an error's text for status_detail and logs: one line, no connection string.
func errorLine(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code + " " + pe.Message
	}
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

func (r *Runner) closeSession() {
	if r.sess != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = r.sess.Close(ctx)
		cancel()
		r.sess, r.src = nil, nil
	}
}

// session connects, runs the start checks (creating the slot when needed) and polls until an
// error.
func (r *Runner) session(ctx context.Context) error {
	if err := r.setStatus(ctx, StatusConnecting, "", nil, 0); err != nil {
		return err
	}
	t, name, err := r.d.Target(ctx, r.org, r.id)
	if err != nil {
		return err
	}
	r.target, r.name = t, name
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	sess, err := Connect(cctx, t.Conn)
	cancel()
	if err != nil {
		return err
	}
	r.sess = sess
	r.degraded = map[string]bool{}
	if ok, err := sess.TryLock(ctx, r.slot); err != nil {
		return err
	} else if !ok {
		holder := sess.LockHolder(ctx, r.slot)
		detail := map[string]any{"message": "another Deedglass process holds this slot"}
		if holder != "" {
			detail["holder"] = holder
		}
		if err := r.setStatus(ctx, StatusWaiting, ReasonHeldElsewhere, detail, 0); err != nil {
			return err
		}
		return errWaiting
	}
	row, err := r.row(ctx)
	if err != nil {
		return err
	}
	r.pub = row.Publication
	if err := r.startChecks(ctx, row); err != nil {
		return err
	}
	r.src = &PeekSource{S: sess, Slot: r.slot, Pub: r.pub}
	r.sens = map[string]bool{}
	for _, c := range t.Sensitive {
		r.sens[c] = true
	}
	r.hb = NewHeartbeat(r.id.String(), r.d.Now())
	r.shapes = map[uint32]string{}
	if r.freshSlot {
		// A new slot is a new stream (S21): relation shapes seen through an earlier slot (perhaps
		// in another database) say nothing about this one, so relation_changed starts afresh.
		row.Shapes, r.freshSlot = nil, false
	}
	for k, v := range row.Shapes {
		if n, err := strconv.ParseUint(k, 10, 32); err == nil {
			r.shapes[uint32(n)] = v
		}
	}
	r.pk = map[uint32]pkEntry{}
	r.counters = map[string]int64{}
	for k, v := range row.Frames {
		r.counters[k] = v
	}
	now := r.d.Now()
	r.lastHB, r.lastEmit, r.lastTimeline, r.lastReconcile, r.lastHealth = now, time.Time{}, now, time.Time{}, time.Time{}
	r.interval, r.caughtUpAt, r.lastTarget, r.recon = r.cfg.PollIdle, time.Time{}, 0, nil
	if err := r.setStatus(ctx, StatusRunning, "", nil, 0); err != nil {
		return err
	}
	r.log.Info("decoder: running", "server", sess.Version, "system_id", r.systemID, "timeline", r.timeline)
	return r.loop(ctx)
}

func (r *Runner) row(ctx context.Context) (store.DecoderSlot, error) {
	var row store.DecoderSlot
	err := r.d.DB.OrgReadTx(ctx, r.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		row, err = store.GetDecoderSlot(ctx, tx, r.id.String(), false)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return row, errStopped
	}
	return row, err
}

// startChecks are S20 §3.11's checks, in order; each failure halts with a precise reason.
func (r *Runner) startChecks(ctx context.Context, row store.DecoderSlot) error {
	s := r.sess
	pf, err := s.Preflight(ctx)
	if err != nil {
		return err
	}
	if pf.WALLevel != "logical" {
		return halt(ReasonWALLevel, false, "wal_level", pf.WALLevel)
	}
	if !pf.Replication {
		return halt(ReasonNotReplication, false)
	}
	if pf.BeginActionBody == nil {
		return halt(ReasonLedgerFunctionChanged, true, "message", "deedglass.begin_action is missing")
	}
	sum := sha256.Sum256([]byte(*pf.BeginActionBody))
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != setup.BeginActionBodySHA256 {
		return halt(ReasonLedgerFunctionChanged, true, "body_sha256", got)
	}
	sn, err := s.Snapshot(ctx, r.pub)
	if err != nil {
		return err
	}
	if p := SettingsProblem(sn.Settings); p != "" {
		return halt(ReasonSessionSettingsChanged, true, "setting", p)
	}
	if err := r.checkPublication(sn.Pub); err != nil {
		return err
	}
	r.pubSeen = sn.Pub
	sys, tl, err := s.Identity(ctx)
	if err != nil {
		return err
	}
	if row.SystemID != nil && *row.SystemID != sys && row.SlotState == "created" {
		return halt(ReasonSystemChanged, true, "was", *row.SystemID, "now", sys)
	}
	r.systemID, r.timeline = sys, tl
	if row.TimelineID != nil && *row.TimelineID != tl {
		r.degraded[ReasonTimelineChanged] = true
	}
	st, err := s.Slot(ctx, r.slot)
	if err != nil {
		return err
	}
	db, err := s.CurrentDatabase(ctx)
	if err != nil {
		return err
	}
	switch {
	case !st.Exists && row.SlotState == "created":
		return halt(ReasonSlotMissing, true, "slot", r.slot)
	case !st.Exists:
		// The publication check passed, so creating the slot now is safe (spike order).
		lsn, err := s.CreateSlot(ctx, r.slot)
		if err != nil {
			return err
		}
		if err := r.recordSlotCreated(ctx, lsn); err != nil {
			return err
		}
		r.freshSlot = true
	case st.Plugin != "pgoutput" || st.Database != db:
		return halt(ReasonSlotConflict, false, "plugin", st.Plugin, "database", st.Database)
	case st.Invalidated():
		return halt(ReasonSlotInvalidated, true, "wal_status", st.WALStatus, "invalidation_reason", st.InvalidationReason)
	case row.SlotState != "created":
		// A slot of this name exists but Deedglass never recorded it (a crash between creating it
		// and recording it): adopt it, sealing the creation now.
		if err := r.recordSlotCreated(ctx, st.ConfirmedFlush); err != nil {
			return err
		}
	}
	return nil
}

// checkPublication: the publication exists, publishes all four operations, has no row filter or
// column list, and contains the ledger and every watched table.
func (r *Runner) checkPublication(p PubSnapshot) error {
	if !p.Exists {
		return halt(ReasonPublicationMissing, true, "publication", r.pub)
	}
	if !p.AllOps || p.Filtered {
		return halt(ReasonPublicationInvalid, false, "publication", r.pub, "message", "it must publish insert, update, delete and truncate, with no row filter or column list")
	}
	var missing []string
	for _, t := range append([]string{ledgerName}, r.target.Watched...) {
		if !p.AllTables && !slices.Contains(p.Tables, t) {
			missing = append(missing, t)
		}
	}
	if len(missing) > 0 {
		return halt(ReasonPublicationInvalid, false, "publication", r.pub, "missing", missing)
	}
	return nil
}

// recordSlotCreated marks the row and seals decoder.slot_created in one org transaction.
func (r *Runner) recordSlotCreated(ctx context.Context, at LSN) error {
	return r.d.DB.OrgTx(ctx, r.org, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := store.MarkSlotCreated(ctx, tx, r.id.String(), r.d.Worker, r.systemID, r.timeline)
		if err != nil {
			return err
		}
		if !ok {
			return errStopped
		}
		_, err = r.d.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminDecoderSlotCreated,
			Actor: deed.Ref{Type: "system", ID: "deedglass"}, Target: &deed.Ref{Type: "connector", ID: r.id.String()},
			Data: map[string]any{"connector": r.name, "slot": r.slot, "system_id": r.systemID, "timeline": r.timeline,
				"lsn": at.String(), "reason": "created"}})
		return err
	})
}

// loop polls until an error.
func (r *Runner) loop(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		full := false
		if r.cfg.Paused == nil || !r.cfg.Paused() {
			var err error
			full, err = r.within(ctx, pollTimeout, func(ctx context.Context) (bool, error) { return r.poll(ctx) })
			if err != nil {
				return err
			}
			if _, err := r.within(ctx, opTimeout, func(ctx context.Context) (bool, error) { return false, r.heartbeat(ctx) }); err != nil {
				return err
			}
			if _, err := r.within(ctx, pollTimeout, func(ctx context.Context) (bool, error) { return false, r.reconcileTick(ctx) }); err != nil {
				return err
			}
			if err := r.finishReconcile(ctx); err != nil {
				return err
			}
		}
		if _, err := r.within(ctx, opTimeout, func(ctx context.Context) (bool, error) { return false, r.healthTick(ctx) }); err != nil {
			return err
		}
		if full {
			continue
		}
		wait := r.interval
		if r.active() && wait > r.cfg.PollActive {
			wait = r.cfg.PollActive
		}
		if !sleepCtx(ctx, wait, r.nudge) {
			return ctx.Err()
		}
	}
}

// Every round trip to the watched database runs under a deadline, so a hung connection becomes an
// error (and a reconnect, logged) instead of a silent stall.
const (
	pollTimeout = 2 * time.Minute // a peek can read a large backlog
	opTimeout   = 30 * time.Second
)

// within runs f with a deadline; a deadline that fires while the runner's own context is live is
// reported as a timeout.
func (r *Runner) within(ctx context.Context, d time.Duration, f func(ctx context.Context) (bool, error)) (bool, error) {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	ok, err := f(c)
	if err != nil && ctx.Err() == nil && c.Err() != nil {
		return ok, fmt.Errorf("decoder: no answer from the watched database within %v: %w", d, err)
	}
	return ok, err
}

// poll is one round (S20 §3.9): snapshot, peek, snapshot, classify, keys, persist, advance, hooks.
// It reports whether the batch was full (poll again at once).
func (r *Runner) poll(ctx context.Context) (bool, error) {
	s := r.sess
	a0 := r.d.Now()
	a, err := s.Snapshot(ctx, r.pub)
	if err != nil {
		return false, err
	}
	if p := SettingsProblem(a.Settings); p != "" {
		return false, halt(ReasonSessionSettingsChanged, true, "setting", p)
	}
	asm := NewAssembler(AssembleOptions{Sensitive: r.sens, MaxImages: r.cfg.MaxImages, Shapes: r.shapes})
	start := r.d.Now()
	var bad *haltError
	stats, err := r.src.Peek(ctx, r.cfg.BatchChanges, func(raw Raw) error {
		m, perr := SafeParse(raw.Data)
		if perr != nil {
			var me *MalformedError
			errors.As(perr, &me)
			bad = halt(ReasonMalformedEvidence, true, "lsn", raw.LSN.String(), "type", string(rune(me.Type)), "byte", me.Offset, "why", me.Reason)
			return bad
		}
		if aerr := asm.Add(raw.LSN, m); aerr != nil {
			var me *MalformedError
			errors.As(aerr, &me)
			bad = halt(ReasonMalformedEvidence, true, "lsn", raw.LSN.String(), "type", string(rune(me.Type)), "why", me.Reason)
			return bad
		}
		return nil
	})
	took := r.d.Now().Sub(start)
	r.d.Metrics.peek(r.id.String(), took)
	if bad != nil {
		return false, bad
	}
	if err != nil {
		return false, r.peekError(err)
	}
	batch, err := asm.Finish()
	if err != nil {
		var me *MalformedError
		errors.As(err, &me)
		return false, halt(ReasonMalformedEvidence, true, "type", string(rune(me.Type)), "why", me.Reason)
	}
	r.lastPeekRows = stats.Rows
	r.adapt(took)
	b, err := s.Snapshot(ctx, r.pub)
	if err != nil {
		return false, err
	}
	pubChanged := a.Pub.Fingerprint != b.Pub.Fingerprint || a.Pub.Fingerprint != r.pubSeen.Fingerprint
	for _, n := range stats.Notices {
		if strings.Contains(n, "skipped loading publication") {
			pubChanged = true
		}
	}
	if pubChanged {
		r.degraded[ReasonPublicationChanged] = true
	}
	if b.Pub.Exists {
		r.pubSeen = b.Pub
	}
	// Classify (pure), then the non-transactional messages: heartbeats and forgeries.
	var keep []*Frame
	for _, f := range batch.Frames {
		if Classify(f, r.org.String(), pubChanged) {
			keep = append(keep, f)
		}
	}
	var hbAt *time.Time
	for _, m := range batch.NonTx {
		if m.Prefix != MarkerPrefix {
			r.counters["other_nontx"]++
			continue
		}
		switch r.hb.Check(m.Content) {
		case HeartbeatValid:
			t := r.d.Now()
			hbAt, r.lastHB = &t, t
			r.counters["heartbeats"]++
		case HeartbeatStale, HeartbeatForeign:
			r.counters["heartbeats_ignored"]++
		default:
			keep = append(keep, NonTxFrame(m))
		}
	}
	if err := r.applyKeys(ctx, keep, batch.Relations); err != nil {
		return false, err
	}
	// The advance target (S20 §3.8, ADR-045): the last commit, the last message and, when the peek
	// drained, the flush LSN read before it.
	target := max(batch.LastCommitEnd, batch.LastNonTx)
	drained := stats.Rows < r.cfg.BatchChanges
	if drained && a.FlushLSN > target {
		target = a.FlushLSN
	}
	if drained {
		r.caughtUpAt = start
	}
	if len(keep) == 0 && hbAt == nil && target <= r.lastTarget {
		return !drained, nil // nothing new: no write, no advance
	}
	newFrames, err := r.persist(ctx, keep, target, hbAt)
	if err != nil {
		return false, err
	}
	if f := r.d.Faults.AfterPersist; f != nil {
		if err := f(r.id.String()); err != nil {
			return false, err
		}
	}
	if target > r.lastTarget {
		if _, err := r.src.Advance(ctx, target); err != nil {
			return false, r.peekError(err)
		}
		r.lastTarget = target
		r.setView(func(v *RunnerView) { v.Checkpoint = target })
	}
	pollTook := r.d.Now().Sub(a0)
	r.setView(func(v *RunnerView) {
		if len(v.Polls) >= 4096 {
			v.Polls = v.Polls[1:]
		}
		v.Polls = append(v.Polls, pollTook)
	})
	for _, f := range newFrames {
		r.d.Metrics.frame(r.id.String(), string(f.Class))
		if f.ActionID != "" && f.Class != ClassMissing && r.d.Hooks.OnBound != nil {
			r.d.Hooks.OnBound(ctx, r.org, r.id, f.ActionID, f.Class, f.CommitLSN)
		}
	}
	return !drained, nil
}

// adapt doubles the interval when a peek takes more than 25% of it (at most 5 s), and degrades
// with peek_slow above 2 s.
func (r *Runner) adapt(took time.Duration) {
	switch {
	case took > r.interval/4 && r.interval < 5*time.Second:
		r.interval = min(r.interval*2, 5*time.Second)
	case took < r.cfg.PollIdle/8 && r.interval > r.cfg.PollIdle:
		r.interval = max(r.interval/2, r.cfg.PollIdle)
	}
	r.degraded[ReasonPeekSlow] = took > 2*time.Second
}

// peekError maps a peek or advance failure: a missing publication on 16/17, an invalidated or
// missing slot halt; anything else is a connection problem and retried.
func (r *Runner) peekError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg := pe.Message
		switch {
		case strings.Contains(msg, "publication") && strings.Contains(msg, "does not exist"):
			return halt(ReasonPublicationMissing, true, "message", msg)
		case strings.Contains(msg, "replication slot") && strings.Contains(msg, "does not exist"):
			return halt(ReasonSlotMissing, true, "slot", r.slot)
		case strings.Contains(msg, "can no longer") || strings.Contains(msg, "invalidated") || pe.Code == "55000" && strings.Contains(msg, "slot"):
			return halt(ReasonSlotInvalidated, true, "message", msg)
		}
	}
	return err
}

// applyKeys looks up the primary keys of relations seen for the first time (or with a new shape)
// and fills each frame's keys.
func (r *Runner) applyKeys(ctx context.Context, fs []*Frame, rels map[uint32]string) error {
	var need []uint32
	for id, shape := range rels {
		if e, ok := r.pk[id]; !ok || e.shape != shape {
			need = append(need, id)
		}
	}
	if len(need) > 0 {
		got, err := r.sess.PrimaryKeys(ctx, need)
		if err != nil {
			return err
		}
		for _, id := range need {
			r.pk[id] = pkEntry{shape: rels[id], cols: got[id]}
		}
	}
	pk := make(map[uint32][]string, len(r.pk))
	for id, e := range r.pk {
		pk[id] = e.cols
	}
	ApplyKeys(fs, pk)
	return nil
}

// persist is step 6: the frames, the checkpoint and the counters in one org transaction, with a
// deedglass_jobs NOTIFY when a frame names an action, and decoder.alert for each new security
// signal. It returns the frames that were new (a re-delivered frame is skipped).
func (r *Runner) persist(ctx context.Context, fs []*Frame, target LSN, hbAt *time.Time) ([]*Frame, error) {
	rows := make([]store.DecodedFrame, len(fs))
	for i, f := range fs {
		row, err := r.frameRow(f)
		if err != nil {
			return nil, err
		}
		rows[i] = row
	}
	var fresh []*Frame
	var lastCommit *string
	var lastAt *time.Time
	for _, f := range fs {
		if !f.NonTx && f.CommitLSN > 0 {
			s, t := f.CommitLSN.String(), f.CommitTime
			lastCommit, lastAt = &s, &t
		}
	}
	err := r.d.DB.OrgTx(ctx, r.org, func(ctx context.Context, tx pgx.Tx) error {
		fresh = fresh[:0]
		news, err := store.InsertFrames(ctx, tx, rows)
		if err != nil {
			return err
		}
		counters := make(map[string]int64, len(r.counters)+len(Classes))
		for k, v := range r.counters {
			counters[k] = v
		}
		notify := false
		for i, f := range fs {
			if !news[i] {
				continue
			}
			fresh = append(fresh, f)
			counters[string(f.Class)]++
			if f.ActionID != "" {
				notify = true
			}
			if kind := alertKind(f.Class); kind != "" {
				if err := r.publish(ctx, tx, ledger.EventDecoderAlert, "", kind, f.CommitLSN, f.ActionID); err != nil {
					return err
				}
			}
		}
		shapes := make(map[string]string, len(r.shapes))
		for k, v := range r.shapes {
			shapes[strconv.FormatUint(uint64(k), 10)] = v
		}
		cp := ""
		if target > 0 {
			cp = target.String()
		} else {
			cp = "0/0"
		}
		ok, err := store.SaveProgress(ctx, tx, r.id.String(), r.d.Worker, store.SlotProgress{CheckpointLSN: cp, LastCommitLSN: lastCommit,
			LastCommitAt: lastAt, LastHeartbeatAt: hbAt, Frames: counters, Shapes: shapes})
		if err != nil {
			return err
		}
		if !ok {
			return errStopped
		}
		if notify {
			if err := store.NotifyJobs(ctx, tx, r.org.String()); err != nil {
				return err
			}
		}
		r.counters = counters
		return nil
	})
	if err != nil {
		return nil, err
	}
	r.setView(func(v *RunnerView) {
		v.Counters = make(map[string]int64, len(r.counters))
		for k, n := range r.counters {
			v.Counters[k] = n
		}
	})
	return fresh, nil
}

func alertKind(c Class) string {
	switch c {
	case ClassForgedMarker, ClassForgedMarkerNonTx, ClassForeignLedger:
		return string(c)
	}
	return ""
}

// frameRow is the stored form of a frame.
func (r *Runner) frameRow(f *Frame) (store.DecodedFrame, error) {
	row := store.DecodedFrame{OrgID: r.org.String(), ConnectorID: r.id.String(), SystemID: r.systemID, TimelineID: r.timeline,
		Class: string(f.Class), NonTx: f.NonTx, Markers: f.Markers, ChangeCount: f.Total, Flags: f.Flags}
	if len(row.Markers) > 64 {
		row.Markers = row.Markers[:64]
		row.Flags = append(row.Flags, "markers_truncated")
	}
	if len(row.Flags) > 64 {
		row.Flags = row.Flags[:64]
	}
	cl, el := f.CommitLSN.String(), f.EndLSN.String()
	row.CommitLSN, row.EndLSN = &cl, &el
	if !f.NonTx {
		x, t := int64(f.Xid), f.CommitTime
		row.Xid, row.CommitTime = &x, &t
	}
	if f.ActionID != "" {
		a := f.ActionID
		row.ActionID = &a
	}
	if f.MarkerLSN > 0 {
		m := f.MarkerLSN.String()
		row.MarkerLSN = &m
	}
	if f.Digest != "" && f.Total > 0 {
		d := f.Digest
		row.ChangesDigest = &d
	}
	var err error
	if len(f.Ledger) > 0 {
		if row.Ledger, err = json.Marshal(f.Ledger); err != nil {
			return row, err
		}
	}
	if row.Tables, err = json.Marshal(f.Tables); err != nil {
		return row, err
	}
	if row.Counts, err = json.Marshal(f.Counts); err != nil {
		return row, err
	}
	changes := f.Changes
	if changes == nil {
		changes = []Change{}
	}
	if row.Changes, err = json.Marshal(changes); err != nil {
		return row, err
	}
	return row, nil
}

// publish sends a decoder event on deedglass_events inside tx.
func (r *Runner) publish(ctx context.Context, tx pgx.Tx, typ, status, reasonOrKind string, lsn LSN, action string) error {
	ev := ledger.Event{Type: typ, Org: r.org.String(), ID: r.id.String()}
	if status != "" {
		ev.State = &status
	}
	if reasonOrKind != "" {
		k := reasonOrKind
		if typ == ledger.EventDecoderAlert {
			ev.Kind = &k
		} else {
			ev.Reason = &k
		}
	}
	if lsn > 0 {
		l := lsn.String()
		ev.LSN = &l
	}
	if action != "" {
		ev.Action = &action
	}
	return r.d.Ledger.Publish(ctx, tx, ev)
}

// heartbeat emits one heartbeat every decoder.heartbeat_interval.
func (r *Runner) heartbeat(ctx context.Context) error {
	now := r.d.Now()
	if !r.lastEmit.IsZero() && now.Sub(r.lastEmit) < r.cfg.HeartbeatInterval {
		return nil
	}
	r.lastEmit = now
	if err := r.sess.EmitHeartbeat(ctx, r.hb.Content(now)); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42501" {
			// pg_logical_emit_message was revoked from this role (S17's optional hardening
			// grants it to deedglass_witness too): no heartbeat, so stalls show up as lag only.
			r.counters["heartbeat_refused"]++
			return nil
		}
		return err
	}
	return nil
}

// healthTick reads the slot every 2 s (and after a status change), applies the thresholds, checks
// the timeline every 30 s, and writes the status.
func (r *Runner) healthTick(ctx context.Context) error {
	now := r.d.Now()
	if now.Sub(r.lastHealth) < 2*time.Second {
		return nil
	}
	r.lastHealth = now
	st, err := r.sess.Slot(ctx, r.slot)
	if err != nil {
		return err
	}
	switch {
	case !st.Exists:
		return halt(ReasonSlotMissing, true, "slot", r.slot)
	case st.Invalidated():
		return halt(ReasonSlotInvalidated, true, "wal_status", st.WALStatus, "invalidation_reason", st.InvalidationReason)
	}
	if now.Sub(r.lastTimeline) >= 30*time.Second {
		r.lastTimeline = now
		sys, tl, err := r.sess.Identity(ctx)
		if err != nil {
			return err
		}
		if sys != r.systemID {
			return halt(ReasonSystemChanged, true, "was", r.systemID, "now", sys)
		}
		if tl != r.timeline {
			r.timeline = tl
			r.degraded[ReasonTimelineChanged] = true
		}
	}
	pf, err := r.sess.Preflight(ctx)
	if err != nil {
		return err
	}
	alert := r.cfg.LagAlertBytes
	if pf.MaxSlotKeep > 0 && pf.MaxSlotKeep/4 < alert {
		alert = pf.MaxSlotKeep / 4
	}
	r.degraded[ReasonLagHigh] = alert > 0 && st.LagBytes > alert
	r.degraded[ReasonRestartRetained] = alert > 0 && st.RetainedBytes > 4*alert
	paused := r.cfg.Paused != nil && r.cfg.Paused()
	r.degraded[ReasonStalled] = now.Sub(r.lastHB) > r.cfg.HeartbeatDeadline && (paused || r.counters["heartbeat_refused"] == 0)
	r.d.Metrics.slot(r.id.String(), st.LagBytes, st.RetainedBytes)
	status, reason := StatusRunning, ""
	for _, k := range []string{ReasonStalled, ReasonLagHigh, ReasonRestartRetained, ReasonPublicationChanged, ReasonTimelineChanged, ReasonPeekSlow} {
		if r.degraded[k] {
			status, reason = StatusDegraded, k
			break
		}
	}
	// publication_changed and timeline_changed are one-shot: shown until the next healthy tick.
	r.degraded[ReasonPublicationChanged], r.degraded[ReasonTimelineChanged] = false, false
	detail := map[string]any{}
	switch reason {
	case ReasonLagHigh, ReasonRestartRetained:
		detail["alert_bytes"] = alert
	case ReasonStalled:
		detail["last_heartbeat_s"] = int(now.Sub(r.lastHB).Seconds())
	}
	lagS := 0.0
	if !r.caughtUpAt.IsZero() {
		lagS = max(now.Sub(r.caughtUpAt).Seconds(), 0)
	}
	return r.writeHealth(ctx, status, reason, detail, st, lagS)
}

// writeHealth writes the status and the slot's numbers, publishing decoder.degraded or
// decoder.recovered when the status changes.
func (r *Runner) writeHealth(ctx context.Context, status, reason string, detail map[string]any, st SlotState, lagS float64) error {
	h := store.SlotHealth{Status: status, Watching: append([]string{}, r.target.Watched...)}
	if reason != "" {
		h.Reason = &reason
	}
	h.Detail, _ = json.Marshal(detail)
	sys, tl, ver := r.systemID, r.timeline, r.sess.VersionNum
	h.SystemID, h.TimelineID, h.ServerVersionNum = &sys, &tl, &ver
	cf, rs, cur := st.ConfirmedFlush.String(), st.Restart.String(), st.Current.String()
	h.ConfirmedFlushLSN, h.RestartLSN, h.CurrentWALLSN = &cf, &rs, &cur
	lag, ret := st.LagBytes, st.RetainedBytes
	h.LagBytes, h.RetainedBytes, h.LagSeconds = &lag, &ret, &lagS
	if st.WALStatus != "" {
		ws := st.WALStatus
		h.WALStatus = &ws
	}
	return r.save(ctx, h, st.ConfirmedFlush)
}

// setStatus writes a status without slot numbers (connecting, waiting, halted).
func (r *Runner) setStatus(ctx context.Context, status, reason string, detail map[string]any, lsn LSN) error {
	h := store.SlotHealth{Status: status}
	if reason != "" {
		h.Reason = &reason
	}
	if detail == nil {
		detail = map[string]any{}
	}
	h.Detail, _ = json.Marshal(detail)
	if r.target != nil {
		h.Watching = append([]string{}, r.target.Watched...)
	}
	return r.save(ctx, h, lsn)
}

func (r *Runner) save(ctx context.Context, h store.SlotHealth, lsn LSN) error {
	reason := ""
	if h.Reason != nil {
		reason = *h.Reason
	}
	key := h.Status + "/" + reason
	prev := r.statusWritten
	ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := r.d.DB.OrgTx(ctx2, r.org, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := store.SaveHealth(ctx, tx, r.id.String(), r.d.Worker, h)
		if err != nil {
			return err
		}
		if !ok {
			return errStopped
		}
		if key == prev {
			return nil
		}
		wasBad := strings.HasPrefix(prev, StatusDegraded+"/") || strings.HasPrefix(prev, StatusHalted+"/")
		switch {
		case h.Status == StatusDegraded || h.Status == StatusHalted:
			return r.publish(ctx, tx, ledger.EventDecoderDegraded, h.Status, reason, lsn, "")
		case h.Status == StatusRunning && wasBad:
			return r.publish(ctx, tx, ledger.EventDecoderRecovered, h.Status, "", lsn, "")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if key != prev {
		r.log.Info("decoder: status", "status", h.Status, "reason", reason)
	}
	r.statusWritten = key
	r.d.Metrics.setStatus(r.id.String(), h.Status, reason)
	r.setView(func(v *RunnerView) { v.Status, v.Reason = h.Status, reason })
	return nil
}

// LSNString formats a LSN the way Postgres does.
func LSNString(l LSN) string { return l.String() }

// ParseLSN parses X/Y.
func ParseLSN(s string) (LSN, error) { return pglogrepl.ParseLSN(s) }

// String for logs.
func (r *Runner) String() string { return fmt.Sprintf("runner %s (%s)", r.id, r.slot) }

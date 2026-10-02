package decoder

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Supervisor runs one Runner per witnessed connector whose lease this instance holds (S20 §3.11).
// Every 5 s it claims or renews leases through claim_decoder_leases (FOR UPDATE SKIP LOCKED, so
// two instances never run the same connector), starts runners for new leases and stops runners
// whose lease it lost. Each runner also takes an advisory lock on the watched database, and every
// write it makes is fenced by its lease.
type Supervisor struct {
	d   Deps
	cfg Config

	mu      sync.Mutex
	runners map[string]*handle // connector id → runner
	wake    chan struct{}
}

type handle struct {
	r       *Runner
	cancel  context.CancelFunc
	done    chan struct{}
	renewed time.Time
}

// NewSupervisor returns a supervisor; Run starts it.
func NewSupervisor(d Deps, cfg Config) *Supervisor {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Metrics == nil {
		d.Metrics = NewMetrics()
	}
	return &Supervisor{d: d, cfg: cfg, runners: map[string]*handle{}, wake: make(chan struct{}, 1)}
}

// Metrics are the supervisor's metrics.
func (s *Supervisor) Metrics() *Metrics { return s.d.Metrics }

// Run syncs the slot rows, then claims leases every 5 s until ctx ends; then it stops every
// runner and releases their leases.
func (s *Supervisor) Run(ctx context.Context) error {
	if _, err := s.d.DB.SyncDecoderSlots(ctx); err != nil {
		s.d.Log.Warn("decoder: syncing slot rows", "err", err)
	}
	lastPrune := time.Time{}
	for {
		s.claim(ctx)
		if s.d.Now().Sub(lastPrune) > time.Hour {
			lastPrune = s.d.Now()
			s.prune(ctx)
		}
		if !sleepCtx(ctx, 5*time.Second, s.wake) {
			break
		}
	}
	s.stopAll()
	return nil
}

// Sync re-reads which connectors are witnessed (after a connector write), claims at once, and
// wakes local runners that are waiting to retry (new credentials may have arrived).
func (s *Supervisor) Sync(ctx context.Context) error {
	_, err := s.d.DB.SyncDecoderSlots(ctx)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	s.NudgeOrg("")
	return err
}

func (s *Supervisor) claim(ctx context.Context) {
	leases, err := s.d.DB.ClaimDecoderLeases(ctx, s.d.Worker, s.cfg.Lease, 100)
	if err != nil {
		if ctx.Err() == nil {
			s.d.Log.Warn("decoder: claiming leases", "err", err)
		}
		return
	}
	now := s.d.Now()
	got := map[string]store.DecoderLease{}
	for _, l := range leases {
		got[l.ConnectorID.String()] = l
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, l := range got {
		h := s.runners[id]
		if h != nil {
			select {
			case <-h.done:
				delete(s.runners, id) // it stopped (dropped, halted fence); start again below
				h = nil
			default:
				h.renewed = now
				continue
			}
		}
		if l.Status == StatusDisabled {
			go s.retireRow(context.WithoutCancel(ctx), l)
			continue
		}
		rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r := newRunner(&s.d, s.cfg, l.OrgID, l.ConnectorID, l.SlotName)
		h = &handle{r: r, cancel: cancel, done: make(chan struct{}), renewed: now}
		s.runners[id] = h
		go func() {
			defer close(h.done)
			r.Run(rctx)
		}()
	}
	// A renewal can miss a row a runner is writing (SKIP LOCKED); stop a runner only once its
	// lease has really run out.
	for id, h := range s.runners {
		if _, ok := got[id]; ok {
			continue
		}
		select {
		case <-h.done:
			delete(s.runners, id)
			continue
		default:
		}
		if now.Sub(h.renewed) > s.cfg.Lease {
			h.cancel()
			<-h.done
			delete(s.runners, id)
		}
	}
}

func (s *Supervisor) stopAll() {
	s.mu.Lock()
	hs := s.runners
	s.runners = map[string]*handle{}
	s.mu.Unlock()
	for _, h := range hs {
		h.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for id, h := range hs {
		<-h.done
		_ = s.d.DB.OrgTx(ctx, h.r.org, func(ctx context.Context, tx pgx.Tx) error {
			return store.ReleaseDecoderLease(ctx, tx, id, s.d.Worker)
		})
	}
}

// stop stops the local runner of connectorID, if any, and waits for it.
func (s *Supervisor) stop(connectorID string) {
	s.mu.Lock()
	h := s.runners[connectorID]
	delete(s.runners, connectorID)
	s.mu.Unlock()
	if h != nil {
		h.cancel()
		<-h.done
	}
}

// Runner returns the local runner of connectorID (nil when another instance runs it).
func (s *Supervisor) Runner(connectorID string) *Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.runners[connectorID]; h != nil {
		return h.r
	}
	return nil
}

// Nudge makes connectorID's runner poll now and stay on decoder.poll_active for a while (S21:
// an action is waiting for L2). Runners on other instances wake through deedglass_jobs.
func (s *Supervisor) Nudge(connectorID string) {
	if r := s.Runner(connectorID); r != nil {
		r.Nudge()
	}
}

// NudgeOrg nudges every local runner of org (the deedglass_jobs NOTIFY payload is the org id).
func (s *Supervisor) NudgeOrg(org string) {
	s.mu.Lock()
	var rs []*Runner
	for _, h := range s.runners {
		if h.r.org.String() == org || org == "" {
			rs = append(rs, h.r)
		}
	}
	s.mu.Unlock()
	for _, r := range rs {
		r.Nudge()
	}
}

func (s *Supervisor) prune(ctx context.Context) {
	if s.cfg.Retention <= 0 {
		return
	}
	now := s.d.Now()
	before, keep := now.Add(-s.cfg.Retention), now.Add(-30*24*time.Hour)
	for range 100 {
		n, err := s.d.DB.PruneDecodedTxs(ctx, before, keep, 1000)
		if err != nil {
			s.d.Log.Warn("decoder: pruning frames", "err", err)
			return
		}
		if n < 1000 {
			return
		}
	}
}

// ErrSlotBusy is a drop refused because another process holds the slot active.
var ErrSlotBusy = errors.New("decoder: the slot is active in another process")

// DropResult is what Drop did.
type DropResult struct {
	Slot string
	Deed string
}

// Drop stops the connector's runner, drops its slot (as the witness role), seals
// decoder.slot_dropped, signals SlotLost and marks the row dropped; the next sync starts a new
// slot, a new baseline (DELETE /v1/connectors/{id}/decoder, deedglass decoder drop).
func (s *Supervisor) Drop(ctx context.Context, org ids.OrgID, connectorID ids.ConnectorID, actor deed.Ref, requestID string) (DropResult, error) {
	id := connectorID.String()
	var row store.DecoderSlot
	// 1. Mark the row dropped first: a runner on any instance stops at its next write.
	err := s.d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if row, err = store.GetDecoderSlot(ctx, tx, id, true); err != nil {
			return err
		}
		return store.MarkSlotDropped(ctx, tx, id, "dropping", false, nil)
	})
	if err != nil {
		return DropResult{}, err
	}
	s.stop(id)
	res := DropResult{Slot: row.SlotName}
	// 2. Drop the slot, waiting out a peek in flight elsewhere (at most 15 s).
	t, name, err := s.d.Target(ctx, org, connectorID)
	if err != nil {
		s.restore(ctx, org, id, row)
		return res, err
	}
	sess, err := Connect(ctx, t.Conn)
	if err != nil {
		s.restore(ctx, org, id, row)
		return res, err
	}
	defer sess.Close(context.WithoutCancel(ctx))
	deadline := time.Now().Add(15 * time.Second)
	for {
		err = sess.DropSlot(ctx, row.SlotName)
		var pe *pgconn.PgError
		switch {
		case err == nil:
		case errors.As(err, &pe) && pe.Code == "42704": // already gone
			err = nil
		case errors.As(err, &pe) && pe.Code == "55006" && time.Now().Before(deadline): // active
			time.Sleep(300 * time.Millisecond)
			continue
		case errors.As(err, &pe) && pe.Code == "55006":
			s.restore(ctx, org, id, row)
			return res, ErrSlotBusy
		default:
			s.restore(ctx, org, id, row)
			return res, err
		}
		break
	}
	// 3. Seal it.
	err = s.d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if err := store.MarkSlotDropped(ctx, tx, id, ReasonDropped, false, nil); err != nil {
			return err
		}
		env, err := s.d.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminDecoderSlotDropped, Actor: actor,
			Target: &deed.Ref{Type: "connector", ID: id}, RequestID: requestID,
			Data: map[string]any{"connector": name, "slot": row.SlotName, "system_id": deref(row.SystemID), "reason": ReasonDropped}})
		if err != nil {
			return err
		}
		res.Deed = env.ID
		st := StatusDropped
		r := ReasonDropped
		return s.d.Ledger.Publish(ctx, tx, ledger.Event{Type: ledger.EventDecoderDegraded, Org: org.String(), ID: id, State: &st, Reason: &r})
	})
	if err != nil {
		return res, err
	}
	if s.d.Hooks.SlotLost != nil {
		s.d.Hooks.SlotLost(ctx, org, connectorID, ReasonDropped)
	}
	s.d.Log.Info("decoder: slot dropped", "connector", id, "slot", row.SlotName)
	return res, s.Sync(ctx)
}

// restore puts a row back as it was after a drop that couldn't finish (status halted, so a runner
// starts again and re-checks the slot).
func (s *Supervisor) restore(ctx context.Context, org ids.OrgID, id string, row store.DecoderSlot) {
	_ = s.d.DB.OrgTx(context.WithoutCancel(ctx), org, func(ctx context.Context, tx pgx.Tx) error {
		return store.RestoreDecoderSlot(ctx, tx, id, row.SlotState, row.CheckpointLSN)
	})
}

// Retire drops a connector's slot before the connector goes away or stops being witnessed, and
// seals decoder.slot_dropped with reason. When the slot can't be dropped (the database is
// unreachable), the row says slot_orphaned and names the slot, so a person can drop it by hand;
// the connector change goes ahead either way.
func (s *Supervisor) Retire(ctx context.Context, org ids.OrgID, connectorID ids.ConnectorID, reason string) error {
	id := connectorID.String()
	var row store.DecoderSlot
	err := s.d.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		row, err = store.GetDecoderSlot(ctx, tx, id, false)
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	s.stop(id)
	if row.SlotState != "created" {
		return s.d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return store.MarkSlotDropped(ctx, tx, id, reason, true, nil)
		})
	}
	dropErr := func() error {
		t, _, err := s.d.Target(ctx, org, connectorID)
		if err != nil {
			return err
		}
		sess, err := Connect(ctx, t.Conn)
		if err != nil {
			return err
		}
		defer sess.Close(context.WithoutCancel(ctx))
		err = sess.DropSlot(ctx, row.SlotName)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "42704" {
			return nil
		}
		return err
	}()
	return s.d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if dropErr != nil {
			detail, _ := json.Marshal(map[string]any{"slot": row.SlotName, "message": errorLine(dropErr),
				"fix": "SELECT pg_catalog.pg_drop_replication_slot('" + row.SlotName + "');"})
			s.d.Log.Warn("decoder: couldn't drop the slot of a connector that is going away", "connector", id, "slot", row.SlotName, "err", errorLine(dropErr))
			return store.MarkSlotDropped(ctx, tx, id, ReasonSlotOrphaned, true, detail)
		}
		if err := store.MarkSlotDropped(ctx, tx, id, reason, true, nil); err != nil {
			return err
		}
		_, err := s.d.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminDecoderSlotDropped,
			Actor: deed.Ref{Type: "system", ID: "deedglass"}, Target: &deed.Ref{Type: "connector", ID: id},
			Data: map[string]any{"slot": row.SlotName, "system_id": deref(row.SystemID), "reason": reason}})
		return err
	})
}

// retireRow drops the slot of a row sync disabled (the connector stopped being witnessed).
func (s *Supervisor) retireRow(ctx context.Context, l store.DecoderLease) {
	if err := s.Retire(ctx, l.OrgID, l.ConnectorID, ReasonNotWitnessed); err != nil {
		s.d.Log.Warn("decoder: retiring a slot", "connector", l.ConnectorID.String(), "err", err)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// IsNotWitnessed reports whether err says the connector has no decoder row.
func IsNotWitnessed(err error) bool { return errors.Is(err, store.ErrNotFound) }

// Health is /v1/health's decoder check on this instance: degraded when a local runner is degraded
// or halted, else ok.
func (s *Supervisor) Health() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.runners {
		switch h.r.View().Status {
		case StatusDegraded, StatusHalted:
			return "degraded"
		}
	}
	return "ok"
}

package observe

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Run is the serve job: every AttachPeriod it attaches frames to open and closing sessions, closes
// sessions past max_session, seals closing sessions once the decoder has passed their close time,
// and finishes sessions whose undo has ended. It returns when ctx ends.
func (s *Service) Run(ctx context.Context) error {
	t := time.NewTicker(AttachPeriod)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Warn("observe: a pass failed; the next one retries", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Tick runs one pass over every org with live sessions.
func (s *Service) Tick(ctx context.Context) error {
	orgs, err := s.DB.ClaimObserveOrgs(ctx, 1000)
	if err != nil {
		return err
	}
	var errs []error
	for _, org := range orgs {
		if err := s.TickOrg(ctx, org); err != nil {
			errs = append(errs, err)
			s.Log.Warn("observe: an org's pass failed", "org_id", org.String(), "err", err)
		}
	}
	return errors.Join(errs...)
}

// TickOrg runs one pass over one org's live sessions, in one transaction.
func (s *Service) TickOrg(ctx context.Context, org ids.OrgID) error {
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.now()`).Scan(&now); err != nil {
			return err
		}
		live, err := store.LiveObserveSessions(ctx, tx)
		if err != nil {
			return err
		}
		// The decoder's progress is read before any frame: the tick runs at READ COMMITTED, and the
		// decoder commits frames and progress together, so progress read first never runs ahead of
		// the frames the attach reads next. Read after, it could show a commit past the close
		// whose earlier frames the attach missed, and the seal would leave them out.
		slots, err := s.decoderFacts(ctx, tx, live)
		if err != nil {
			return err
		}
		conns := map[string]ConnectorFacts{}
		fact := func(id string) (ConnectorFacts, error) {
			if cf, ok := conns[id]; ok {
				return cf, nil
			}
			c, err := store.ConnectorAny(ctx, tx, id)
			if err != nil {
				return ConnectorFacts{}, err
			}
			cf := connectorFacts(c)
			conns[id] = cf
			return cf, nil
		}
		var attachedTo []string
		for i := range live {
			row := &live[i]
			cf, err := fact(row.ConnectorID)
			if err != nil {
				return err
			}
			switch row.State {
			case StateOpen:
				if now.Sub(row.OpenedAt) >= cf.MaxSession {
					end := row.OpenedAt.Add(cf.MaxSession)
					if _, err := store.CloseObserveSession(ctx, tx, row.ID, &end, System.Type, nil, []string{FlagMaxSession}); err != nil {
						return err
					}
					row.State, row.ClosedAt = StateClosing, &end
					row.ClosedByType = &System.Type
				}
				if err := s.attach(ctx, tx, row, cf, now); err != nil {
					return err
				}
				attachedTo = append(attachedTo, row.ID)
			case StateClosing:
				if err := s.attach(ctx, tx, row, cf, now); err != nil {
					return err
				}
				attachedTo = append(attachedTo, row.ID)
			}
		}
		// Overlap: a frame in two sessions of one connector flags both.
		over, err := store.ObserveOverlaps(ctx, tx, attachedTo)
		if err != nil {
			return err
		}
		for _, id := range over {
			if err := store.AddObserveFlags(ctx, tx, id, []string{FlagOverlap}); err != nil {
				return err
			}
		}
		for i := range live {
			row := &live[i]
			if slices.Contains(over, row.ID) && !slices.Contains(row.Flags, FlagOverlap) {
				row.Flags = append(row.Flags, FlagOverlap)
			}
			cf := conns[row.ConnectorID]
			switch row.State {
			case StateOpen, StateClosing:
				if err := s.refresh(ctx, tx, row, cf, slots[row.ConnectorID], now); err != nil {
					return err
				}
			case StateUndoing:
				if err := finishUndo(ctx, tx, org, row); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// attach adds every unattributed frame in the session's window that touches a watched table.
func (s *Service) attach(ctx context.Context, tx pgx.Tx, row *store.ObserveSession, cf ConnectorFacts, now time.Time) error {
	w := Window{Opened: row.OpenedAt, Closed: row.ClosedAt, Now: now}
	cands, err := store.ObserveCandidates(ctx, tx, row.ConnectorID, w.Opened, w.End())
	if err != nil {
		return err
	}
	var add []string
	for _, c := range cands {
		if Attachable(Candidate{ID: c.ID, CommitTime: c.CommitTime, Tables: c.Tables}, w, cf.Watched) {
			add = append(add, c.ID)
		}
	}
	_, err = store.AttachObserveFrames(ctx, tx, row.OrgID, row.ID, add)
	return err
}

// decoderFacts reads the decoder slot row of every connector with an open or closing session.
func (s *Service) decoderFacts(ctx context.Context, tx pgx.Tx, live []store.ObserveSession) (map[string]DecoderFacts, error) {
	out := map[string]DecoderFacts{}
	for i := range live {
		row := &live[i]
		if row.State != StateOpen && row.State != StateClosing {
			continue
		}
		if _, ok := out[row.ConnectorID]; ok {
			continue
		}
		d := DecoderFacts{Heartbeat: s.Heartbeat}
		if ds, err := store.GetDecoderSlot(ctx, tx, row.ConnectorID, false); err == nil {
			d.LastCommitAt, d.LastHeartbeatAt = ds.LastCommitAt, ds.LastHeartbeatAt
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		out[row.ConnectorID] = d
	}
	return out, nil
}

// refresh writes the running summary, and for a closing session flags or seals it. slot is the
// decoder's progress as read before this tick attached any frame.
func (s *Service) refresh(ctx context.Context, tx pgx.Tx, row *store.ObserveSession, cf ConnectorFacts, slot DecoderFacts, now time.Time) error {
	fr, err := store.ObserveFrames(ctx, tx, row.ID, false)
	if err != nil {
		return err
	}
	facts := factsOf(fr)
	sum := Summarize(row.ID, facts, cf.CaughtRows, false)
	if row.State == StateOpen {
		return writeSummary(ctx, tx, row.ID, sum)
	}
	switch NextCloseStep(*row.ClosedAt, now, slot, slices.Contains(row.Flags, FlagDecoderBehind)) {
	case StepFlagBehind:
		if err := store.AddObserveFlags(ctx, tx, row.ID, []string{FlagDecoderBehind}); err != nil {
			return err
		}
		return writeSummary(ctx, tx, row.ID, sum)
	case StepWait:
		return writeSummary(ctx, tx, row.ID, sum)
	}
	return s.seal(ctx, tx, row, cf, facts, sum)
}

func writeSummary(ctx context.Context, tx pgx.Tx, id string, sum Summary) error {
	b, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	return store.SetObserveSummary(ctx, tx, id, b)
}

// seal records observe.session_sealed and moves the session to sealed.
func (s *Service) seal(ctx context.Context, tx pgx.Tx, row *store.ObserveSession, cf ConnectorFacts, facts []FrameFacts, sum Summary) error {
	flags := append([]string{}, row.Flags...)
	if len(sum.Caught) > 0 && !slices.Contains(flags, FlagCaught) {
		flags = append(flags, FlagCaught)
	}
	slices.Sort(flags)
	actor := System
	if row.ClosedByType != nil && *row.ClosedByType != "system" && row.ClosedByID != nil {
		actor = Actor{Type: *row.ClosedByType, ID: *row.ClosedByID}
	}
	data := Seal(row.ID, row.ConnectorID, row.OpenedAt, *row.ClosedAt, facts, flags)
	env, err := s.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: AdminSessionSealed, Actor: actor.ref(),
		Target: &deed.Ref{Type: "observe_session", ID: row.ID}, Data: data})
	if err != nil {
		return err
	}
	sum.Caught = Summarize(row.ID, facts, cf.CaughtRows, true).Caught
	b, err := json.Marshal(sum)
	if err != nil {
		return err
	}
	return store.SealObserveSession(ctx, tx, row.ID, b, env.ID, flags)
}

// finishUndo moves an undoing session on once its undo action is sealed: undone when the undo
// applied, sealed again when it was denied, expired, cancelled or failed (nothing changed).
func finishUndo(ctx context.Context, tx pgx.Tx, org ids.OrgID, row *store.ObserveSession) error {
	if row.UndoActionID == nil {
		return store.FinishObserveUndo(ctx, tx, row.ID, false)
	}
	a, err := store.GetUndoSubject(ctx, tx, org, *row.UndoActionID)
	if errors.Is(err, store.ErrNotFound) {
		return store.FinishObserveUndo(ctx, tx, row.ID, false)
	}
	if err != nil {
		return err
	}
	if a.State != "sealed" {
		return nil
	}
	applied := a.ExecutionStatus != nil && *a.ExecutionStatus == "succeeded"
	return store.FinishObserveUndo(ctx, tx, row.ID, applied)
}

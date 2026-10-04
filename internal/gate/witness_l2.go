package gate

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// L2 in the Gate (S21 §3.2): an eligible write waits in witnessing for the decoder's frame, and
// the first of these ends the wait: a frame bound to the action (the decoder's OnBound, or the
// post-commit check), the l2_match job's run_after (l2_timeout), the slot lost (SlotLost), or
// reconcile's missing frame (OnMissing). Each runs finalizeL2, which is idempotent because every
// transition is a compare-and-set. A frame that arrives after the deed is sealed amends it.

// JobL2Match is the job that ends an L2 wait at witness.l2_timeout (migration 0007 allows it).
const JobL2Match = "l2_match"

// MaxAmendments is the most amendment deeds one action gets (S21 §3.8).
const MaxAmendments = 3

// L2 triggers: what ended (or nudged) a wait.
const (
	l2Check   = "check"   // the post-commit look, and the decoder's OnBound
	l2Timeout = "timeout" // the l2_match job fell due
	l2Lost    = "lost"    // SlotLost
	l2Missing = "missing" // OnMissing (reconcile)
)

// Event reasons on action_events for L2 notes.
const (
	ReasonL2LateConfirmed = "l2_late_confirmed"
	ReasonAmendmentLimit  = "amendment_limit"
)

// l2Plan is an eligible action's L2 target (the connector's settings and the authorized digest).
type l2Plan struct {
	target *connector.L2Target
	wait   bool            // Tx B: the decoder can deliver, so the action waits in witnessing
	gap    *witness.Reason // Tx B: the decoder was already halted, so the gap now and no wait
}

// reasons are the reasons decided before any check ran: the payload's, and L2's plan-time gap.
func (wc witnessCase) reasons() []witness.Reason {
	out := append([]witness.Reason{}, wc.extra...)
	if wc.l2 != nil && wc.l2.gap != nil {
		out = append(out, *wc.l2.gap)
	}
	return out
}

// planL2 decides whether a gateway execution waits for L2 (S21 §3.2): a connector witnessed at L2,
// a tool that may write, a succeeded or unknown execution. The decoder's status is read in Tx B.
func planL2(s Subject, e Execution, ex Executor, call connector.Call, side string) *l2Plan {
	if s.Mode != ModeGateway || (e.Status != ExecSucceeded && e.Status != ExecUnknown) {
		return nil
	}
	if se := connector.SideEffect(side); se != connector.SideEffectWrite && se != connector.SideEffectDestructive {
		return nil
	}
	w, ok := ex.(connector.L2Witnessing)
	if !ok {
		return nil
	}
	t, err := w.L2Target(call)
	if err != nil || t == nil || t.ConnectorID == "" {
		return nil
	}
	return &l2Plan{target: t}
}

// l2Status reads the connector's decoder status in tx: waiting is true when the decoder can
// deliver (running, connecting, degraded, waiting, pending); a halted decoder is the gap
// decoder_halted now; dropped or disabled (or no slot row) means L2 doesn't apply.
func l2Status(ctx context.Context, tx pgx.Tx, connectorID string) (waiting bool, gap *witness.Reason, err error) {
	slot, err := store.GetDecoderSlot(ctx, tx, connectorID, false)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	switch slot.Status {
	case "dropped", "disabled":
		return false, nil, nil
	case "halted":
		why := "halted"
		if slot.StatusReason != nil {
			why = *slot.StatusReason
		}
		r := l2.Lost(why).Reasons[0]
		return false, &r, nil
	}
	return true, nil, nil
}

// l2Payload is what the l2_match job carries besides S19's read-back evidence: nothing the
// action row doesn't have, so a restart rebuilds the case from the row and the connector.
type l2Payload struct {
	jobPayload
	TimeoutMS int64 `json:"l2_timeout_ms"`
}

// enqueueL2 is B1's L2 half: the l2_match job due at executed_at + l2_timeout.
func (p *Pipeline) enqueueL2(ctx context.Context, tx pgx.Tx, s Subject, wc witnessCase) error {
	pl := l2Payload{TimeoutMS: wc.l2.target.Timeout.Milliseconds()}
	if err := json.Unmarshal(payloadOf(wc), &pl.jobPayload); err != nil {
		return err
	}
	b, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	due := p.now().Add(wc.l2.target.Timeout)
	return store.EnqueueJobPayload(ctx, tx, s.Org, s.ID, JobL2Match, due, p.cfg.WorkerMaxAttempts, b)
}

// afterL2B1 runs once B1 has committed with an L2 wait: it makes the decoder poll fast and looks
// for a frame the decoder may already have persisted (the frame-before-B1 race, S21 §3.2).
func (p *Pipeline) afterL2B1(ctx context.Context, wc witnessCase) {
	if p.o.Nudge != nil {
		p.o.Nudge(wc.l2.target.ConnectorID)
	}
	org, _ := ids.Parse[ids.OrgKind](wc.sub.Org)
	id, _ := ids.Parse[ids.ActionKind](wc.sub.ActionID)
	if err := p.finalizeL2(ctx, org, id, l2Check, ""); err != nil && !errors.Is(err, ErrStateConflict) {
		p.log.Warn("gate: the post-commit L2 look failed; the decoder or the job finishes it", "org_id", wc.sub.Org,
			"action_id", wc.sub.ActionID, "err", redact(err))
	}
}

// L2Evidence is the decoder's OnBound hook: a frame naming the action was stored. It finalizes
// the wait, or amends a sealed deed.
func (p *Pipeline) L2Evidence(ctx context.Context, org ids.OrgID, actionID string) {
	id, err := ids.Parse[ids.ActionKind](actionID)
	if err != nil {
		return
	}
	if err := p.finalizeL2(ctx, org, id, l2Check, ""); err != nil && !errors.Is(err, ErrStateConflict) && !errors.Is(err, store.ErrNotFound) {
		p.log.Warn("gate: L2 evidence couldn't be recorded; the l2_match job retries", "org_id", org.String(), "action_id", actionID, "err", redact(err))
	}
}

// L2Missing is the decoder's OnMissing hook: reconcile found the action's ledger row with no frame.
func (p *Pipeline) L2Missing(ctx context.Context, org ids.OrgID, actionID string) {
	id, err := ids.Parse[ids.ActionKind](actionID)
	if err != nil {
		return
	}
	if err := p.finalizeL2(ctx, org, id, l2Missing, ""); err != nil && !errors.Is(err, ErrStateConflict) && !errors.Is(err, store.ErrNotFound) {
		p.log.Warn("gate: a missing frame couldn't be recorded", "org_id", org.String(), "action_id", actionID, "err", redact(err))
	}
}

// L2SlotLost is the decoder's SlotLost hook: every action of the connector still waiting for L2
// is decided now, with the gap that names why.
func (p *Pipeline) L2SlotLost(ctx context.Context, org ids.OrgID, connectorID ids.ConnectorID, reason string) {
	var waiting []ids.ActionID
	err := p.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		waiting, err = store.PendingL2(ctx, tx, org, connectorID.String())
		return err
	})
	if err != nil {
		p.log.Warn("gate: the actions waiting on a lost slot couldn't be listed; their jobs finish them", "org_id", org.String(),
			"connector_id", connectorID.String(), "err", redact(err))
		return
	}
	for _, id := range waiting {
		if err := p.finalizeL2(ctx, org, id, l2Lost, reason); err != nil && !errors.Is(err, ErrStateConflict) {
			p.log.Warn("gate: an L2 wait couldn't be ended", "org_id", org.String(), "action_id", id.String(), "err", redact(err))
		}
	}
}

// l2Read is everything finalizeL2 reads before deciding, in one read transaction.
type l2Read struct {
	ga      *store.GateAction
	frames  []store.DecodedFrame
	l2Job   *store.JobRow
	l1Open  bool            // an l1_readback job is still open: L1 finishes first, then calls finalizeL2
	detail  json.RawMessage // witness_detail: the partial L0/L1 detail while witnessing, the latest after sealing
	amended int
	level   string // the sealed witness section (amendments compare against it)
	verdict string
}

func (p *Pipeline) readL2(ctx context.Context, org ids.OrgID, id ids.ActionID) (*l2Read, error) {
	r := &l2Read{}
	err := p.o.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if r.ga, err = store.GetGateAction(ctx, tx, org, id, false); err != nil {
			return err
		}
		if r.frames, err = store.FramesForAction(ctx, tx, id.String()); err != nil {
			return err
		}
		if j, err := store.ActionJob(ctx, tx, org, id, JobL2Match); err == nil {
			r.l2Job = &j
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if _, err := store.ActionJob(ctx, tx, org, id, JobL1ReadBack); err == nil {
			r.l1Open = true
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		r.detail, r.amended, r.level, r.verdict, err = store.WitnessState(ctx, tx, org, id)
		return err
	})
	return r, err
}

// framesOf converts the stored frames that name the action into l2's plain frames.
func framesOf(fs []store.DecodedFrame) ([]l2.Frame, error) {
	var out []l2.Frame
	for _, f := range fs {
		switch f.Class {
		case l2.ClassBound, l2.ClassMarkerCount, l2.ClassMarkerMismatch, l2.ClassPublicationGap:
		default:
			continue
		}
		x := l2.Frame{ID: f.ID, Class: f.Class, SystemID: f.SystemID, Timeline: f.TimelineID, Markers: f.Markers, Total: f.ChangeCount,
			Flags: f.Flags}
		if f.CommitLSN != nil {
			x.CommitLSN = *f.CommitLSN
		}
		if f.Xid != nil {
			x.Xid = *f.Xid
		}
		if f.CommitTime != nil {
			x.CommitTime = *f.CommitTime
		}
		if f.MarkerLSN != nil {
			x.MarkerLSN = *f.MarkerLSN
		}
		if f.ChangesDigest != nil {
			x.Digest = *f.ChangesDigest
		}
		if err := x.ParseParts(f.Ledger, f.Tables, f.Counts, f.Changes); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

// l2Case is the witness case and L2 target of a stored action (the job's payload, or the row).
func (p *Pipeline) l2Case(ctx context.Context, r *l2Read) (witnessCase, *connector.L2Target, error) {
	var payload json.RawMessage
	if r.l2Job != nil {
		payload = r.l2Job.Payload
	}
	wc, err := p.jobCase(ctx, r.ga, payload)
	if err != nil {
		return wc, nil, err
	}
	w, ok := wc.ex.(connector.L2Witnessing)
	if !ok {
		return wc, nil, errors.New("gate: the action's connector isn't witnessed at L2 any more")
	}
	t, err := w.L2Target(wc.call)
	if err != nil {
		return wc, nil, err
	}
	if t == nil {
		return wc, nil, errors.New("gate: the action's connector isn't witnessed at L2 any more")
	}
	return wc, t, nil
}

// evaluateL2 runs l2.Evaluate for frames (the trigger lookup through the connector, with no
// transaction open: S21 invariant 5).
func (p *Pipeline) evaluateL2(ctx context.Context, wc witnessCase, t *connector.L2Target, frames []l2.Frame) l2.Result {
	in := l2.Input{
		Action: l2.Action{ID: wc.sub.ActionID, Org: wc.sub.Org, Agent: wc.sub.Agent, SQLDigest: t.SQLDigest},
		Frames: frames,
		Derive: l2.DeriveInput{Tool: t.Tool, SQL: t.SQL, Params: t.Params, DefaultSchema: t.DefaultSchema, Explicit: wc.expect},
		Config: l2.Config{AutoColumns: t.AutoColumns, MaxRows: t.MaxRows, Watched: t.Watched, StrictTriggers: t.StrictTriggers,
			DiffRows: t.DiffRows},
	}
	if wc.call.IntentHash != "" {
		h := wc.call.IntentHash
		in.Action.IntentHash = &h
	}
	if n, ok := rowsAffected(wc.sub.Result.Meta); ok {
		in.Derive.Reported = &n
	}
	in.Derive.Returning = returningRows(wc.sub.Result.Output)
	for _, w := range t.Writes {
		in.Derive.Contract = append(in.Derive.Contract, l2.ContractWrite{Table: w.Table, Ops: w.Ops, MaxRows: w.MaxRows, Columns: w.Columns})
	}
	res := l2.Evaluate(in)
	// The trigger note: only for the footprint's tables, through the connector, cached 60 s.
	var tables []string
	for _, te := range res.Footprint.Tables {
		tables = append(tables, te.Table)
	}
	if w, ok := wc.ex.(connector.L2Witnessing); ok && len(tables) > 0 {
		fresh := false
		for _, r := range res.Reasons {
			fresh = fresh || r.Class == witness.Violation
		}
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		trig, err := w.L2Triggers(tctx, tables, fresh)
		cancel()
		if err == nil && len(trig) > 0 {
			in.Triggers = trig
			res = l2.Evaluate(in)
		}
	}
	return res
}

// rowsAffected reads Result.Meta["rows_affected"] (a number after JSON or as the connector set it).
func rowsAffected(meta map[string]any) (int64, bool) {
	switch v := meta["rows_affected"].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), v == float64(int64(v))
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}

// returningRows reads postgres.execute's RETURNING rows ({"returning": {"columns": [...], "rows":
// [[...]]}}) as maps, for an INSERT's exact keys.
func returningRows(out json.RawMessage) []map[string]any {
	if len(out) == 0 {
		return nil
	}
	var o struct {
		Returning *struct {
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
			Rows []json.RawMessage `json:"rows"`
		} `json:"returning"`
	}
	if json.Unmarshal(out, &o) != nil || o.Returning == nil {
		return nil
	}
	var rows []map[string]any
	for _, raw := range o.Returning.Rows {
		var arr []any
		var obj map[string]any
		switch {
		case json.Unmarshal(raw, &obj) == nil:
			rows = append(rows, obj)
		case json.Unmarshal(raw, &arr) == nil:
			m := map[string]any{}
			for i, c := range o.Returning.Columns {
				if i < len(arr) {
					m[c.Name] = arr[i]
				}
			}
			rows = append(rows, m)
		}
	}
	return rows
}

// priorLevels reads the L0 and L1 reasons, methods and the L1 read-back from a stored detail
// (the partial detail B2 left, or the latest sealed one).
func priorLevels(raw json.RawMessage) (map[witness.Level][]witness.Reason, map[witness.Level]string, *witness.ReadbackDetail, bool) {
	d, ok := witness.ParseDetail(raw)
	if !ok || d.Object != "witness_detail" {
		return nil, nil, nil, false
	}
	by := map[witness.Level][]witness.Reason{}
	for _, r := range d.Reasons {
		if r.Level == witness.L2 || r.Code == witness.CodeLevelBelowRequired || r.Code == witness.CodeNoChecks {
			continue
		}
		by[r.Level] = append(by[r.Level], r)
	}
	methods := map[witness.Level]string{}
	var rb *witness.ReadbackDetail
	if l := d.Levels.L0; l != nil {
		methods[witness.L0] = l.Method
	}
	if l := d.Levels.L1; l != nil {
		methods[witness.L1] = l.Method
		rb = l.Readback
	}
	return by, methods, rb, true
}

// priorL2Frames are the frame ids a stored detail's L2 read, sorted.
func priorL2Frames(raw json.RawMessage) []string {
	d, ok := witness.ParseDetail(raw)
	if !ok || d.Levels.L2 == nil || d.Levels.L2.L2Evidence == nil {
		return nil
	}
	return d.Levels.L2.FrameIDs
}

// finalizeL2 ends an L2 wait (or amends a sealed deed) for one action. trigger says what called
// it; reason is SlotLost's reason. It is safe to call any number of times from any instance.
func (p *Pipeline) finalizeL2(ctx context.Context, org ids.OrgID, id ids.ActionID, trigger, reason string) error {
	r, err := p.readL2(ctx, org, id)
	if err != nil {
		return err
	}
	state := State(r.ga.State)
	if state != Witnessing && state != Sealed {
		return nil
	}
	if state == Witnessing && r.l2Job == nil {
		return nil // no L2 wait (S19's paths own it)
	}
	if state == Witnessing && r.l1Open {
		return nil // the read-back finishes first and calls back (S21 §3.2)
	}
	frames, err := framesOf(r.frames)
	if err != nil {
		return err
	}
	var res l2.Result
	switch {
	case len(frames) > 0:
		wc, t, err := p.l2Case(ctx, r)
		if err != nil {
			if state == Witnessing && trigger == l2Timeout {
				res = l2.Gap(witness.CodeInternalError, nil)
				break
			}
			return err
		}
		res = p.evaluateL2(ctx, wc, t, frames)
		if len(res.Reasons) == 0 {
			return nil // frames of no class L2 reads
		}
	case state == Sealed:
		return nil // a late gap never amends
	case trigger == l2Timeout:
		d := wcTimeout(r)
		res = l2.Timeout(d)
	case trigger == l2Lost:
		res = l2.Lost(reason)
	case trigger == l2Missing:
		res = l2.NoEvidence()
	default:
		return nil // keep waiting
	}
	if state == Sealed {
		return p.amendL2(ctx, org, id, r, res)
	}
	return p.sealL2(ctx, org, id, r, res)
}

// wcTimeout is the wait's length, for l2_timeout's message.
func wcTimeout(r *l2Read) time.Duration {
	if r.l2Job != nil {
		var pl l2Payload
		if json.Unmarshal(r.l2Job.Payload, &pl) == nil && pl.TimeoutMS > 0 {
			return time.Duration(pl.TimeoutMS) * time.Millisecond
		}
	}
	return 10 * time.Second
}

// decideL2 is Decide across L0 (recomputed from the case), L1 (the stored reasons) and L2.
func (p *Pipeline) decideL2(ctx context.Context, r *l2Read, res l2.Result) (witness.Outcome, error) {
	by, methods, rb, ok := priorLevels(r.detail)
	if !ok {
		by, methods = map[witness.Level][]witness.Reason{}, map[witness.Level]string{}
	}
	if len(by[witness.L0]) == 0 {
		wc := p.storedCase(r.ga)
		if r.l2Job != nil {
			if c, err := p.jobCase(ctx, r.ga, r.l2Job.Payload); err == nil {
				wc = c
			}
		}
		l0, m0 := witness.EvaluateL0(wc.sub)
		by[witness.L0], methods[witness.L0] = l0, m0
	}
	by[witness.L2] = res.Reasons
	methods[witness.L2] = l2.Method
	d := witness.Decide(requiredOf(r.ga), by, methods)
	return witness.FinishWith(d, methods, witness.Extras{Readback: rb, L2: res.Evidence}, p.now(), p.wcfg.MaxDetailBytes)
}

// sealL2 is the end of an L2 wait: witnessing → the verdict (compare-and-set), the jobs gone, the
// deed sealed, in one transaction.
func (p *Pipeline) sealL2(ctx context.Context, org ids.OrgID, id ids.ActionID, r *l2Read, res l2.Result) error {
	out, err := p.decideL2(ctx, r, res)
	if err != nil {
		return err
	}
	run := func(seal bool) error {
		return p.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if err := p.recordVerdict(ctx, tx, id, out, "system", ""); err != nil {
				return err
			}
			for _, k := range []string{JobL2Match, JobL1ReadBack} {
				if err := store.DeleteActionJob(ctx, tx, org, id, k); err != nil {
					return err
				}
			}
			if !seal {
				return nil
			}
			return p.seal(ctx, tx, id)
		})
	}
	err = run(true)
	if errors.Is(err, errSeal) {
		p.log.Error("gate: the L2 verdict is recorded, but its deed couldn't be sealed; the sweeper retries", "org_id", org.String(),
			"action_id", id.String(), "err", err)
		err = run(false)
	}
	if errors.Is(err, ledger.ErrStateConflict) {
		return ErrStateConflict
	}
	if err != nil {
		return err
	}
	p.m.l2(out.Decision)
	p.broker.Notify(id.String())
	return nil
}

// amendL2 is late evidence about a sealed action (S21 §3.8): the action deed never changes; a
// different (level, verdict) is an amendment deed, at most three; the same one is a note.
func (p *Pipeline) amendL2(ctx context.Context, org ids.OrgID, id ids.ActionID, r *l2Read, res l2.Result) error {
	if !res.HasFrame() {
		return nil
	}
	ids0 := priorL2Frames(r.detail)
	if res.Evidence != nil && slices.Equal(ids0, res.Evidence.FrameIDs) {
		return nil // the same evidence the latest verdict already read
	}
	out, err := p.decideL2(ctx, r, res)
	if err != nil {
		return err
	}
	d := out.Decision
	err = p.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		detail, amended, level, verdict, err := store.WitnessState(ctx, tx, org, id)
		if err != nil {
			return err
		}
		if !slices.Equal(priorL2Frames(detail), ids0) {
			return ErrStateConflict // another instance amended it meanwhile: look again
		}
		if string(d.Level) == level && string(d.Verdict) == verdict {
			lv, vd := level, verdict
			return p.o.Ledger.AppendEvent(ctx, tx, id.String(), ledger.EventData{Actor: "system", At: p.now(), Reason: strp(ReasonL2LateConfirmed),
				Level: &lv, Verdict: &vd})
		}
		if amended >= MaxAmendments {
			k := ReasonAmendmentLimit
			a := id.String()
			return p.o.Ledger.Publish(ctx, tx, ledger.Event{Type: ledger.EventDecoderAlert, Org: org.String(), ID: id.String(), Action: &a, Kind: &k})
		}
		lvl, vrd, req := string(d.Level), string(d.Verdict), string(d.Required)
		env, err := p.o.Ledger.SealAmendment(ctx, tx, id.String(), deed.WitnessSection{Level: lvl, Verdict: vrd, Method: d.Method}, json.RawMessage(out.JSON))
		if err != nil {
			return err
		}
		if err := store.IncrementAmendments(ctx, tx, org, id); err != nil {
			return err
		}
		a := id.String()
		st := string(Sealed)
		ev := ledger.Event{Type: ledger.EventActionWitnessed, Org: org.String(), ID: id.String(), Action: &a, State: &st, Deed: &env.ID,
			Level: &lvl, Verdict: &vrd, RequiredLevel: &req, Flags: out.Flags()}
		return p.o.Ledger.Publish(ctx, tx, ev)
	})
	if errors.Is(err, ErrStateConflict) {
		return p.finalizeL2(ctx, org, id, l2Check, "")
	}
	if err != nil {
		return err
	}
	p.broker.Notify(id.String())
	return nil
}

// storeL1Partial is B2 when L2 is also waiting (S21 §3.2): the L0 and L1 reasons go into
// witness_detail with no claim, the read-back's job ends, and nothing is sealed; finalizeL2
// decides across all three levels and seals once.
func (p *Pipeline) storeL1Partial(ctx context.Context, org ids.OrgID, id ids.ActionID, out witness.Outcome) error {
	det := out.Detail
	det.Claim = witness.Claim{}
	det.Summary = ""
	var rs []witness.Reason
	for _, r := range det.Reasons {
		if r.Code != witness.CodeLevelBelowRequired && r.Code != witness.CodeNoChecks {
			rs = append(rs, r)
		}
	}
	det.Reasons = rs
	b, err := json.Marshal(det)
	if err != nil {
		return err
	}
	return p.o.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := store.SetPartialWitness(ctx, tx, org, id, b)
		if err != nil {
			return err
		}
		if !ok {
			return ErrStateConflict
		}
		return store.DeleteActionJob(ctx, tx, org, id, JobL1ReadBack)
	})
}

// l2Waiting reports whether an action has an open l2_match job (the worker's l1_readback handler
// asks before it decides).
func l2Waiting(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ActionID) (bool, error) {
	_, err := store.ActionJob(ctx, tx, org, id, JobL2Match)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// handleL2Job is the l2_match job falling due: the wait ends with whatever is there (a frame that
// arrived without a hook, or l2_timeout). When the read-back is still open, the job waits for it.
func (w *Worker) handleL2Job(ctx context.Context, j store.ClaimedJob) {
	p := w.p
	err := p.finalizeL2(ctx, j.OrgID, j.ActionID, l2Timeout, "")
	if err != nil && !errors.Is(err, ErrStateConflict) {
		p.m.job(j.Kind, "error")
		p.log.Warn("gate: an l2_match job failed; it retries", "org_id", j.OrgID.String(), "action_id", j.ActionID.String(), "err", redact(err))
		_ = p.o.DB.OrgTx(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
			job, err := store.GetJob(ctx, tx, j.OrgID, j.ID)
			if err != nil {
				return err
			}
			if job.Attempts >= job.MaxAttempts {
				return store.CompleteJob(ctx, tx, j.OrgID, j.ID)
			}
			return store.RetryJob(ctx, tx, j.OrgID, j.ID, p.now().Add(backoff(job.Attempts)), redact(err))
		})
		return
	}
	// Still waiting (the read-back is open): look again shortly, without counting an attempt.
	_ = p.o.DB.OrgTx(ctx, j.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.GetJob(ctx, tx, j.OrgID, j.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if st, _, _, err := store.ActionState(ctx, tx, j.OrgID, j.ActionID); err != nil || State(st) != Witnessing {
			return store.CompleteJob(ctx, tx, j.OrgID, j.ID) // nothing waits any more
		}
		return store.DeferJob(ctx, tx, j.OrgID, j.ID, p.now().Add(time.Second))
	})
	p.m.job(j.Kind, "done")
	p.broker.Notify(j.ActionID.String())
}

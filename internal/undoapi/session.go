package undoapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// Session undo (S50 §3.6). One engine, two sources: a watched session's transactions (observe
// mode) now; a caught Gate session's actions later. The plan, the preview and the checks are free
// (the undo SQL a person runs comes from the same plan); running it in one step is Pro: the Gate
// executor for "<connector>.undo_session" hands the plan to the SessionUndo port, which
// ee/undosession fills when the licence allows it (cmd/deedglass/licence.go is the only reader).

// SessionTool is the internal tool's unqualified name.
const SessionTool = "undo_session"

// SessionSubject is everything a session undo plans from.
type SessionSubject struct {
	ID          string
	ConnectorID string
	State       string    // the session's state (sealed is the only one an undo starts from)
	ClosedAt    time.Time // the age check counts from here
	Frames      []store.DecodedFrame
	Excluded    []string
	Expired     []string // transactions whose frames aged out (not excluded)
	UndoAction  string   // the undo that holds the session (undoing or undone)
}

// SessionSource loads a session for the executor (observe.Service, through serve's wiring).
type SessionSource func(ctx context.Context, org ids.OrgID, id string) (SessionSubject, error)

// ErrSessionNotFound: the id isn't a session of the org.
var ErrSessionNotFound = errors.New("undoapi: no such session")

// SessionRun is one session undo the Pro engine runs.
type SessionRun struct {
	Call     connector.Call
	Plan     *undo.Plan
	Digest   string // what the person approved (args.plan_digest)
	Instance *connector.Instance
	Runner   Runner
	Options  undo.RunOptions
	// Record stores the plan's exact footprint on the undo action before anything runs (S21 reads
	// actions.witness_expect), in its own short control-plane transaction.
	Record func(ctx context.Context, footprint []byte) error
}

// SessionUndo is the Pro port: run a session's plan in one transaction. nil when not entitled.
type SessionUndo interface {
	Run(ctx context.Context, in SessionRun) (connector.Result, error)
}

// sessionSubject is a session as the shared planner reads it.
func (p *Planner) sessionSubject(ctx context.Context, org ids.OrgID, subj SessionSubject) (*subject, error) {
	s := &subject{action: store.UndoSubject{ID: subj.ID, State: "sealed", Mode: "gateway"}}
	err := p.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		c, err := store.GetConnector(ctx, tx, subj.ConnectorID, false)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		s.conn, s.connName, s.witnessOn = &c, c.Name, witnessEnabled(c.Config)
		s.hasCreds, err = store.HasCredential(ctx, tx, c.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	tool := SessionTool
	if s.conn != nil {
		tool = s.conn.Name + "." + SessionTool
	}
	s.action.Tool = tool
	closed := subj.ClosedAt
	s.action.SealedAt = &closed
	exec := "succeeded"
	s.action.ExecutionStatus = &exec
	lvl := "L2"
	s.action.WitnessLevel = &lvl
	if s.frames, err = framesOf(subj.Frames); err != nil {
		return nil, err
	}
	return s, nil
}

// buildSession plans a session: its transactions as one evidence, the catalog now, the plan.
func (p *Planner) buildSession(ctx context.Context, org ids.OrgID, subj SessionSubject) (*built, error) {
	s, err := p.sessionSubject(ctx, org, subj)
	if err != nil {
		return nil, err
	}
	out := &built{sub: s}
	connID := ""
	if s.conn != nil {
		connID = s.conn.ID
	}
	out.ev = undo.FromSessionFrames(undo.SessionInput{Session: subj.ID, ConnectorID: connID, Frames: s.frames})
	if len(subj.Expired) > 0 {
		out.ev.Problems = append(out.ev.Problems, undo.Finding{Check: undo.CheckEvidence, Text: MsgFramesExpired})
	}
	out.b, out.run = p.bound(ctx, org, s)
	if out.run != nil && len(out.ev.Problems) == 0 {
		relids := make([]uint32, 0, len(out.ev.Tables))
		for _, t := range out.ev.TableNames() {
			relids = append(relids, out.ev.Tables[t].RelID)
		}
		slices.Sort(relids)
		cctx, cancel := context.WithTimeout(ctx, p.cfg.StatementTimeout)
		out.cat, out.catErr = out.run.UndoCatalog(cctx, out.b.Instance(), relids)
		cancel()
	}
	if out.cat != nil {
		out.plan, out.findings = undo.Build(undo.BuildInput{Undoes: subj.ID, ConnectorID: connID, Evidence: out.ev, Catalog: out.cat})
	} else {
		out.findings = append([]undo.Finding{}, out.ev.Problems...)
	}
	return out, nil
}

// MsgFramesExpired is the evidence failure when a transaction's row images aged out.
const MsgFramesExpired = "Frames expired: undo no longer possible. Deedglass keeps a session's row images for the decoder's retention, and some are gone."

// SessionPreview is a session undo's preview: the shared preview plus the conflict count.
type SessionPreview struct {
	*Preview
	Conflicts int // rows that changed again after the session
}

// PreviewSession plans a session's undo and runs every check without locks. It writes nothing.
func (p *Planner) PreviewSession(ctx context.Context, org ids.OrgID, subj SessionSubject, allowTriggers bool) (*SessionPreview, error) {
	bt, err := p.buildSession(ctx, org, subj)
	if err != nil {
		return nil, err
	}
	s := bt.sub
	var cur undo.Current
	conflicts := 0
	if bt.plan != nil && bt.run != nil {
		cctx, cancel := context.WithTimeout(ctx, p.cfg.StatementTimeout)
		cur, err = bt.run.UndoRows(cctx, bt.b.Instance(), bt.plan)
		cancel()
		if err != nil {
			cur = nil
			bt.findings = append(bt.findings, undo.Finding{Check: undo.CheckPrivileges,
				Text: "Deedglass couldn't read the rows the undo would change as deedglass_agent (" + shortErr(err) + ")."})
		} else {
			bad, _ := undo.Compare(bt.plan, cur)
			conflicts = len(bad)
		}
	}
	if bt.catErr != nil {
		bt.findings = append(bt.findings, undo.Finding{Check: undo.CheckPrivileges,
			Text: "Deedglass couldn't read the catalog as deedglass_agent (" + shortErr(bt.catErr) + ")."})
	}
	var other *undo.OtherUndo
	switch subj.State {
	case "undoing":
		other = &undo.OtherUndo{ID: subj.UndoAction, State: "running"}
	case "undone":
		other = &undo.OtherUndo{ID: subj.UndoAction, State: "applied"}
	}
	facts := actionFacts(s)
	facts.Sealed = subj.State != "open" && subj.State != "closing"
	in := undo.CheckInput{Action: facts, Connector: connectorFacts(s), Now: p.now(), MaxAge: p.cfg.MaxAge, MaxRows: p.cfg.MaxRows,
		Other: other, Evidence: bt.ev, Plan: bt.plan, Findings: bt.findings, Catalog: bt.cat, Current: cur, AllowTriggers: allowTriggers}
	checks := undo.Checks(in)
	for i := range checks {
		// A watched session has no claim and no level: say what the evidence is.
		if checks[i].ID == undo.CheckEvidence && checks[i].Status == undo.StatusPass {
			checks[i].Title = "Observed evidence"
			checks[i].Detail = fmt.Sprintf("Observed · %s · %s row images", plural(len(s.frames), "transaction"), undo.Thousands(len(bt.ev.Changes)))
		}
		if checks[i].ID == undo.CheckEvidence && checks[i].Status != undo.StatusPass {
			checks[i].Title = "Observed evidence"
		}
	}
	pv := &Preview{Action: subj.ID, Tool: s.action.Tool, Plan: bt.plan, Evidence: bt.ev, Checks: checks}
	pv.Available = undo.Available(pv.Checks)
	pv.Summary = sessionSummary(bt.plan, subj.ID)
	if s.conn != nil {
		pv.Connector = &ConnectorRef{ID: s.conn.ID, Name: s.conn.Name, Type: s.conn.Type}
	}
	pv.Redacted = redactedColumns(bt.plan)
	return &SessionPreview{Preview: pv, Conflicts: conflicts}, nil
}

// PlanSession rebuilds a session's plan as the executor needs it.
func (p *Planner) PlanSession(ctx context.Context, org ids.OrgID, subj SessionSubject) (*undo.Plan, connector.Bound, Runner, error) {
	bt, err := p.buildSession(ctx, org, subj)
	if err != nil {
		return nil, nil, nil, err
	}
	if bt.catErr != nil {
		return nil, bt.b, bt.run, bt.catErr
	}
	return bt.plan, bt.b, bt.run, nil
}

func sessionSummary(p *undo.Plan, session string) string {
	if p == nil {
		return "Nothing in this session can be put back from the record."
	}
	return undo.SummaryOf(p.Rows, p.TableNames(), "this session")
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return undo.Thousands(n) + " " + w + "s"
}

// SessionArgs are "<connector>.undo_session"'s arguments: ids, a digest, a flag and a reason.
type SessionArgs struct {
	Session       string `json:"session"`
	PlanDigest    string `json:"plan_digest"`
	AllowTriggers bool   `json:"allow_triggers"`
	Reason        string `json:"reason"`
	// Excluded are the transactions a person left out (sorted dtx ids): in the undo's own record,
	// and checked again at run time (a different set is undo_plan_stale).
	Excluded []string `json:"excluded"`
}

// SessionExecutor runs "<connector>.undo_session" for the Gate: it rebuilds the session's plan from
// the stored frames and the catalog now, refuses a digest that differs from the one approved
// (undo_plan_stale: nothing was sent), and hands the plan to the Pro engine. Without the engine
// (no Pro) it refuses with pro_required; nothing runs.
type SessionExecutor struct {
	b       connector.Bound
	run     Runner
	planner *Planner
	db      *store.DB
	source  SessionSource
	engine  SessionUndo
}

var (
	_ connector.Executor     = (*SessionExecutor)(nil)
	_ connector.L2Witnessing = (*SessionExecutor)(nil)
)

// CodeProRequired is the executor's failure code without Pro.
const CodeProRequired = "pro_required"

// Execute implements connector.Executor.
func (e *SessionExecutor) Execute(ctx context.Context, call connector.Call) (connector.Result, error) {
	if err := connector.ValidateArgs(call.Tool.InputSchema, call.Args); err != nil {
		return connector.Result{}, err
	}
	var a SessionArgs
	if err := json.Unmarshal(call.Args, &a); err != nil || a.Session == "" || a.PlanDigest == "" {
		return connector.Result{}, connector.ErrValidation("args", "need session and plan_digest")
	}
	if e.engine == nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeProRequired,
			AgentMessage: "Undoing a whole session in one step is part of Pro, so nothing ran. The undo SQL stays free: deedglass observe show " + a.Session + " --undo-sql."}
	}
	if e.source == nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_unavailable",
			AgentMessage: "Deedglass couldn't read the session, so nothing ran. Try again in a moment."}
	}
	subj, err := e.source(ctx, call.OrgID, a.Session)
	if err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeUndoPlanStale, AgentMessage: undo.MsgStale, Err: err}
	}
	if !sameSet(subj.Excluded, a.Excluded) {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeUndoPlanStale, AgentMessage: undo.MsgStale}
	}
	plan, _, _, err := e.planner.PlanSession(ctx, call.OrgID, subj)
	if err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_unavailable",
			AgentMessage: "Deedglass couldn't read the database's catalog to check the plan, so nothing ran. Try again in a moment.", Err: err}
	}
	if plan == nil || plan.Digest != a.PlanDigest {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeUndoPlanStale, AgentMessage: undo.MsgStale}
	}
	cfg := e.planner.Config()
	return e.engine.Run(ctx, SessionRun{Call: call, Plan: plan, Digest: a.PlanDigest, Instance: e.b.Instance(), Runner: e.run,
		Options: undo.RunOptions{AllowTriggers: a.AllowTriggers, LockTimeout: cfg.LockTimeout, StatementTimeout: cfg.StatementTimeout},
		Record: func(ctx context.Context, fp []byte) error {
			return e.db.OrgTx(ctx, call.OrgID, func(ctx context.Context, tx pgx.Tx) error {
				return store.SetWitnessExpect(ctx, tx, call.OrgID, call.ActionID, fp)
			})
		}})
}

// L2Target implements connector.L2Witnessing through the bound connector.
func (e *SessionExecutor) L2Target(call connector.Call) (*connector.L2Target, error) {
	w, ok := e.b.(connector.L2Witnessing)
	if !ok {
		return nil, nil
	}
	return w.L2Target(call)
}

// L2Triggers implements connector.L2Witnessing through the bound connector.
func (e *SessionExecutor) L2Triggers(ctx context.Context, tables []string, fresh bool) (map[string][]string, error) {
	w, ok := e.b.(connector.L2Witnessing)
	if !ok {
		return nil, nil
	}
	return w.L2Triggers(ctx, tables, fresh)
}

func sameSet(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(slices.Compact(x), slices.Compact(y))
}

// isSessionTool reports whether a qualified tool is a session undo.
func isSessionTool(qualified string) bool { return strings.HasSuffix(qualified, "."+SessionTool) }

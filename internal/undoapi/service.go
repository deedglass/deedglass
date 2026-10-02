// Package undoapi is the impure half of S23's undo: it loads the original action's evidence from
// the Ledger, reads the customer database's catalog and rows through the Postgres connector, plans
// and checks with internal/witness/undo (pure), and runs the undo as a Gate action: the HTTP
// routes (preview, run), the executor the Gate calls for <connector>.undo, the internal resolver
// that only the console principal reaches, and the approval context section "undo".
//
// It lives beside witness/undo (as policyapi lives beside policy) because it needs the store, the
// connector registry and the Gate, which the pure package must never import.
package undoapi

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness/l2"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// Config is undo.* (deedglass.yaml).
type Config struct {
	MaxRows          int
	MaxAge           time.Duration
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	PreviewSteps     int
}

func (c Config) withDefaults() Config {
	if c.MaxRows <= 0 {
		c.MaxRows = 1000
	}
	if c.MaxAge <= 0 {
		c.MaxAge = 168 * time.Hour
	}
	if c.LockTimeout <= 0 {
		c.LockTimeout = 2 * time.Second
	}
	if c.StatementTimeout <= 0 {
		c.StatementTimeout = 10 * time.Second
	}
	if c.PreviewSteps <= 0 {
		c.PreviewSteps = 20
	}
	return c
}

// Runner is the connector type that plans and runs undos: internal/connector/postgres implements
// it. undo never imports postgres; undoapi meets it through this interface.
type Runner interface {
	UndoCatalog(ctx context.Context, inst *connector.Instance, relids []uint32) (*undo.Catalog, error)
	UndoRows(ctx context.Context, inst *connector.Instance, p *undo.Plan) (undo.Current, error)
	RunUndo(ctx context.Context, inst *connector.Instance, call connector.Call, p *undo.Plan, o undo.RunOptions) (connector.Result, error)
}

// InternalResolver finds a connector's internal tool (connector.Registry.ResolveInternal).
type InternalResolver interface {
	ResolveInternal(ctx context.Context, org ids.OrgID, qualified string) (connector.Executor, connector.ToolSpec, string, error)
}

// Planner loads evidence, reads the catalog and builds plans and previews.
type Planner struct {
	db  *store.DB
	reg InternalResolver
	cfg Config
	now func() time.Time
	log *slog.Logger
}

// NewPlanner returns a Planner.
func NewPlanner(db *store.DB, reg InternalResolver, cfg Config, log *slog.Logger) *Planner {
	if log == nil {
		log = slog.Default()
	}
	return &Planner{db: db, reg: reg, cfg: cfg.withDefaults(), now: time.Now, log: log}
}

// Config is the effective configuration.
func (p *Planner) Config() Config { return p.cfg }

// subject is everything Deedglass knows about the action to undo, read in one transaction.
type subject struct {
	action    store.UndoSubject
	frames    []l2.Frame
	live      *store.LiveUndo
	conn      *store.Connector // nil: deleted, or the action named none
	hasCreds  bool
	connName  string
	witnessOn bool
}

// ErrNotFound: the id isn't an action of the org (another org's action looks exactly like this).
var ErrNotFound = errors.New("undoapi: no such action")

// load reads the action, its frames, its live undo and its connector.
func (p *Planner) load(ctx context.Context, org ids.OrgID, action string) (*subject, error) {
	if ids.Check(ids.Action, action) != nil {
		return nil, ErrNotFound
	}
	s := &subject{}
	err := p.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if s.action, err = store.GetUndoSubject(ctx, tx, org, action); err != nil {
			return err
		}
		fs, err := store.FramesForAction(ctx, tx, action)
		if err != nil {
			return err
		}
		if s.frames, err = framesOf(fs); err != nil {
			return err
		}
		if lu, err := store.GetLiveUndo(ctx, tx, org, action); err == nil {
			s.live = &lu
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		// The connector: the one whose decoder bound the frame, else the action's slug.
		var c store.Connector
		switch {
		case len(fs) > 0:
			c, err = store.GetConnector(ctx, tx, fs[0].ConnectorID, false)
		case s.action.Connector != nil:
			c, err = store.ConnectorByName(ctx, tx, *s.action.Connector, false)
		default:
			err = store.ErrNotFound
		}
		if s.action.Connector != nil {
			s.connName = *s.action.Connector
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if c.DeletedAt == nil {
			s.conn = &c
			s.connName = c.Name
			s.witnessOn = witnessEnabled(c.Config)
			if s.hasCreds, err = store.HasCredential(ctx, tx, c.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return s, err
}

// framesOf converts stored frames into l2's plain frames (the classes that name an action).
func framesOf(fs []store.DecodedFrame) ([]l2.Frame, error) {
	var out []l2.Frame
	for _, f := range fs {
		x := l2.Frame{ID: f.ID, Class: f.Class, SystemID: f.SystemID, Timeline: f.TimelineID, Markers: f.Markers, Total: f.ChangeCount, Flags: f.Flags}
		if f.CommitLSN != nil {
			x.CommitLSN = *f.CommitLSN
		}
		if f.Xid != nil {
			x.Xid = *f.Xid
		}
		if f.CommitTime != nil {
			x.CommitTime = *f.CommitTime
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

// bound is the connector's internal undo tool, with its runner, or nil when it can't run one.
func (p *Planner) bound(ctx context.Context, org ids.OrgID, s *subject) (connector.Bound, Runner) {
	if s.conn == nil || s.conn.Type != "postgres" || s.conn.Status != "active" || p.reg == nil {
		return nil, nil
	}
	ex, _, _, err := p.reg.ResolveInternal(ctx, org, s.conn.Name+"."+undoTool)
	if err != nil {
		return nil, nil
	}
	b, ok := ex.(connector.Bound)
	if !ok {
		return nil, nil
	}
	r, ok := b.Impl().(Runner)
	if !ok {
		return nil, nil
	}
	return b, r
}

// undoTool is the internal tool's unqualified name.
const undoTool = "undo"

// built is a plan and everything the checks read.
type built struct {
	sub      *subject
	ev       *undo.Evidence
	plan     *undo.Plan
	findings []undo.Finding
	cat      *undo.Catalog
	b        connector.Bound
	run      Runner
	catErr   error
}

// build loads the subject, reads the catalog through the connector and builds the plan.
func (p *Planner) build(ctx context.Context, org ids.OrgID, action string) (*built, error) {
	s, err := p.load(ctx, org, action)
	if err != nil {
		return nil, err
	}
	out := &built{sub: s}
	connID := ""
	if s.conn != nil {
		connID = s.conn.ID
	}
	out.ev = undo.FromFrames(undo.FrameInput{Action: action, ConnectorID: connID, Frames: s.frames})
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
		if out.catErr != nil {
			p.log.Warn("undo: the catalog couldn't be read", "org_id", org.String(), "action_id", action, "err", out.catErr)
		}
	}
	if out.cat != nil {
		out.plan, out.findings = undo.Build(undo.BuildInput{Undoes: action, ConnectorID: connID, Evidence: out.ev, Catalog: out.cat})
	} else {
		out.findings = append([]undo.Finding{}, out.ev.Problems...)
	}
	return out, nil
}

// Preview is the undo_preview answer before it is rendered.
type Preview struct {
	Action    string
	Tool      string
	Plan      *undo.Plan
	Evidence  *undo.Evidence
	Checks    []undo.Check
	Available bool
	Summary   string
	Connector *ConnectorRef
	Level     string
	Verdict   string
	Redacted  []string
	Live      *store.LiveUndo
}

// ConnectorRef names the connector an undo runs through.
type ConnectorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Preview plans the undo and runs every check without locks (S23 §3.7). It writes nothing.
func (p *Planner) Preview(ctx context.Context, org ids.OrgID, action string, allowTriggers bool, loc *time.Location) (*Preview, error) {
	bt, err := p.build(ctx, org, action)
	if err != nil {
		return nil, err
	}
	s := bt.sub
	var cur undo.Current
	if bt.plan != nil && bt.run != nil {
		cctx, cancel := context.WithTimeout(ctx, p.cfg.StatementTimeout)
		cur, err = bt.run.UndoRows(cctx, bt.b.Instance(), bt.plan)
		cancel()
		if err != nil {
			p.log.Warn("undo: the rows couldn't be read", "org_id", org.String(), "action_id", action, "err", err)
			cur = nil
			bt.findings = append(bt.findings, undo.Finding{Check: undo.CheckPrivileges,
				Text: "Deedglass couldn't read the rows the undo would change as deedglass_agent (" + shortErr(err) + ")."})
		}
	}
	if bt.catErr != nil {
		bt.findings = append(bt.findings, undo.Finding{Check: undo.CheckPrivileges,
			Text: "Deedglass couldn't read the catalog as deedglass_agent (" + shortErr(bt.catErr) + ")."})
	}
	in := undo.CheckInput{Action: actionFacts(s), Connector: connectorFacts(s), Now: p.now(), MaxAge: p.cfg.MaxAge, MaxRows: p.cfg.MaxRows,
		Other: otherUndo(s.live, loc), Evidence: bt.ev, Plan: bt.plan, Findings: bt.findings, Catalog: bt.cat, Current: cur,
		AllowTriggers: allowTriggers}
	pv := &Preview{Action: action, Tool: s.action.Tool, Plan: bt.plan, Evidence: bt.ev, Checks: undo.Checks(in), Live: s.live}
	pv.Available = undo.Available(pv.Checks)
	pv.Summary = undo.Summary(bt.plan, action)
	if s.conn != nil {
		pv.Connector = &ConnectorRef{ID: s.conn.ID, Name: s.conn.Name, Type: s.conn.Type}
	}
	if s.action.WitnessLevel != nil {
		pv.Level = *s.action.WitnessLevel
	}
	if s.action.WitnessVerdict != nil {
		pv.Verdict = *s.action.WitnessVerdict
	}
	pv.Redacted = redactedColumns(bt.plan)
	return pv, nil
}

// Plan rebuilds the plan of action as the executor needs it: the evidence and the catalog now. It
// returns nil when the evidence can't be planned from any more.
func (p *Planner) Plan(ctx context.Context, org ids.OrgID, action string) (*undo.Plan, connector.Bound, Runner, error) {
	bt, err := p.build(ctx, org, action)
	if err != nil {
		return nil, nil, nil, err
	}
	if bt.catErr != nil {
		return nil, bt.b, bt.run, bt.catErr
	}
	return bt.plan, bt.b, bt.run, nil
}

func actionFacts(s *subject) undo.ActionFacts {
	a := s.action
	f := undo.ActionFacts{ID: a.ID, Sealed: a.State == "sealed", Mode: a.Mode, Tool: a.Tool, PayloadPruned: a.PayloadPrunedAt}
	if a.WitnessLevel != nil {
		f.WitnessLevel = *a.WitnessLevel
	}
	if a.WitnessVerdict != nil {
		f.Verdict = *a.WitnessVerdict
	}
	if a.SealedAt != nil {
		f.SealedAt = *a.SealedAt
	}
	f.Executed = a.ExecutionStatus != nil && *a.ExecutionStatus != "failed"
	return f
}

func connectorFacts(s *subject) undo.ConnectorFacts {
	if s.conn == nil {
		return undo.ConnectorFacts{Name: nonEmpty(s.connName, "postgres")}
	}
	return undo.ConnectorFacts{Exists: true, Name: s.conn.Name, Type: s.conn.Type, Status: s.conn.Status, HasCredentials: s.hasCreds,
		WitnessEnabled: s.witnessOn}
}

func otherUndo(u *store.LiveUndo, loc *time.Location) *undo.OtherUndo {
	if u == nil {
		return nil
	}
	o := &undo.OtherUndo{ID: u.ID}
	switch {
	case u.State == "pending_approval":
		o.State = "pending"
	case u.State == "sealed" && u.ExecutionStatus != nil && *u.ExecutionStatus != "failed":
		o.State = "applied"
		if u.SealedAt != nil {
			o.At = clock(*u.SealedAt, loc)
		}
	default:
		o.State = "running"
	}
	return o
}

// clock is "10:14 IST" in loc (UTC when nil).
func clock(t time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	return t.Format("15:04 ") + t.Format("MST")
}

func witnessEnabled(cfg []byte) bool {
	var c struct {
		Witness struct {
			Enabled bool `json:"enabled"`
		} `json:"witness"`
	}
	return jsonUnmarshal(cfg, &c) == nil && c.Witness.Enabled
}

func redactedColumns(p *undo.Plan) []string {
	if p == nil {
		return []string{}
	}
	set := map[string]bool{}
	for _, g := range p.Guards {
		t, _ := p.Table(g.Table)
		for c, v := range g.Expect {
			if v.D != "" {
				set[t.String()+"."+c] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// shortErr is an error as a short phrase for a check's detail: a SQLSTATE when there is one, never
// a value.
func shortErr(err error) string {
	if ce := connector.AsError(err); ce != nil && ce.AgentMessage != "" {
		return strings.TrimSuffix(ce.AgentMessage, ".")
	}
	if st := store.SQLState(err); st != "" {
		return "SQLSTATE " + st
	}
	return "a connection problem"
}

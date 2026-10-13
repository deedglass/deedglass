package observe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Admin actions (sealed). Data: SealedData for the seal; {session, frame, excluded} for an exclude.
const (
	AdminSessionSealed = ledger.AdminObserveSessionSealed
	AdminFrameExcluded = ledger.AdminObserveFrameExcluded
)

// Errors the API maps to its catalog.
var (
	ErrNotFound          = errors.New("observe: no such session")
	ErrConnectorNotFound = errors.New("observe: no such connector")
	ErrNotPostgres       = errors.New("observe: observe mode reads Postgres connectors only")
	ErrAgentNotFound     = errors.New("observe: no such agent")
	ErrNotSealed         = errors.New("observe: the session isn't sealed")
	ErrFrameNotAttached  = errors.New("observe: the transaction isn't in this session")
	ErrLabel             = errors.New("observe: the label is 1 to 80 characters on one line")
)

// NotEnabledError: the connector's witness.observe is off (or the Witness is).
type NotEnabledError struct{ Connector string }

func (e *NotEnabledError) Error() string { return "observe: observe mode is off for " + e.Connector }

// Actor is who opened, closed or changed a session: {user, usr_…}, {api_key, key_…} or {system, deedglass}.
type Actor struct {
	Type string
	ID   string
}

// System is the serve job's actor (max_session, the seal of a session nobody closed).
var System = Actor{Type: "system", ID: "deedglass"}

func (a Actor) ref() deed.Ref {
	if a.Type == "system" {
		return deed.Ref{Type: "system", ID: "deedglass"}
	}
	return deed.Ref{Type: a.Type, ID: a.ID}
}

func (a Actor) idPtr() *string {
	if a.Type == "system" || a.ID == "" {
		return nil
	}
	s := a.ID
	return &s
}

// Service is observe mode: the routes' and the CLI's operations, and the serve job.
type Service struct {
	DB        *store.DB
	Ledger    *ledger.Ledger
	Log       *slog.Logger
	Heartbeat time.Duration // decoder.heartbeat_interval (CaughtUp)
	now       func() time.Time
}

// New returns a Service.
func New(db *store.DB, led *ledger.Ledger, heartbeat time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{DB: db, Ledger: led, Log: log, Heartbeat: heartbeat, now: time.Now}
}

// ConnectorFacts is what observe reads from a connector's row.
type ConnectorFacts struct {
	ID         string
	Name       string
	Type       string
	Status     string
	Deleted    bool
	Enabled    bool
	Watched    []string
	MaxSession time.Duration
	CaughtRows int
	Sensitive  []string
}

func connectorFacts(c store.Connector) ConnectorFacts {
	f := ConnectorFacts{ID: c.ID, Name: c.Name, Type: c.Type, Status: c.Status, Deleted: c.DeletedAt != nil,
		MaxSession: postgres.DefaultObserveMaxSession, CaughtRows: postgres.DefaultObserveCaughtRows}
	if c.Type != "postgres" {
		return f
	}
	var cfg postgres.Config
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return f
	}
	f.Enabled = cfg.Witness.ObserveEnabled()
	f.Watched = append([]string{}, cfg.Witness.WatchedTables...)
	f.MaxSession, f.CaughtRows = cfg.Witness.ObserveMaxSession(), cfg.Witness.ObserveCaughtRows()
	f.Sensitive = append([]string{}, cfg.Witness.SensitiveColumns...)
	return f
}

// OpenRequest opens a session.
type OpenRequest struct {
	Connector string // con_… or the connector's name
	Label     string
	Agent     string // agt_… or an agent's name; display only, it attributes nothing
}

// Open opens a watched session on a connector whose observe mode is on.
func (s *Service) Open(ctx context.Context, org ids.OrgID, actor Actor, req OpenRequest) (*Session, error) {
	if req.Label != "" && !ValidLabel(req.Label) {
		return nil, ErrLabel
	}
	var out *Session
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		c, err := resolveConnector(ctx, tx, req.Connector)
		if err != nil {
			return err
		}
		cf := connectorFacts(c)
		if cf.Type != "postgres" {
			return ErrNotPostgres
		}
		if !cf.Enabled {
			return &NotEnabledError{Connector: c.Name}
		}
		row := store.ObserveSession{ID: ids.New[ids.ObserveSessionKind]().String(), OrgID: org.String(), ConnectorID: c.ID,
			OpenedByType: actor.Type, OpenedByID: actor.idPtr()}
		if req.Label != "" {
			l := req.Label
			row.Label = &l
		}
		if req.Agent != "" {
			a, err := resolveAgent(ctx, tx, org, req.Agent)
			if err != nil {
				return err
			}
			row.AgentID = &a
		}
		got, err := store.InsertObserveSession(ctx, tx, row)
		if err != nil {
			return err
		}
		out = sessionOf(got, cf, nil)
		return nil
	})
	return out, err
}

func resolveConnector(ctx context.Context, tx pgx.Tx, ref string) (store.Connector, error) {
	var c store.Connector
	var err error
	if strings.HasPrefix(ref, "con_") {
		c, err = store.GetConnector(ctx, tx, ref, false)
	} else {
		c, err = store.ConnectorByName(ctx, tx, ref, false)
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && c.DeletedAt != nil) {
		return c, ErrConnectorNotFound
	}
	return c, err
}

func resolveAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, ref string) (string, error) {
	if strings.HasPrefix(ref, "agt_") {
		id, err := ids.Parse[ids.AgentKind](ref)
		if err != nil {
			return "", ErrAgentNotFound
		}
		a, err := store.GetAgent(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrAgentNotFound
		}
		if err != nil {
			return "", err
		}
		return a.ID.String(), nil
	}
	a, err := store.GetAgentByName(ctx, tx, org, ref)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrAgentNotFound
	}
	if err != nil {
		return "", err
	}
	return a.ID.String(), nil
}

// Close moves an open session to closing (the serve job seals it once the decoder has caught up).
// Closing a session that is already closing or sealed answers its current state.
func (s *Service) Close(ctx context.Context, org ids.OrgID, actor Actor, id string) (*Session, error) {
	if ids.Check(ids.ObserveSession, id) != nil {
		return nil, ErrNotFound
	}
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		row, err := store.GetObserveSession(ctx, tx, id, true)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.State != StateOpen {
			return nil
		}
		_, err = store.CloseObserveSession(ctx, tx, id, nil, actor.Type, actor.idPtr(), nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, org, id)
}

// Session is a watched session as the API, the CLI and the Console read it.
type Session struct {
	Row       store.ObserveSession
	Connector ConnectorFacts
	Summary   Summary
	Frames    []FrameFacts // nil unless loaded
}

func sessionOf(row store.ObserveSession, cf ConnectorFacts, frames []FrameFacts) *Session {
	out := &Session{Row: row, Connector: cf, Frames: frames}
	if len(row.Summary) > 2 {
		_ = json.Unmarshal(row.Summary, &out.Summary)
	}
	if out.Summary.Tables == nil {
		out.Summary.Tables = map[string]Counts{}
	}
	if out.Summary.Caught == nil {
		out.Summary.Caught = []caught.Item{}
	}
	return out
}

// Undoable reports whether a session's undo could still be planned from the record.
func (x *Session) Undoable() bool {
	if x.Row.State != StateSealed {
		return false
	}
	for _, f := range x.Frames {
		if f.Expired && !f.Excluded {
			return false
		}
	}
	return x.Summary.Transactions > x.Summary.Excluded
}

// Get loads one session with its frames' facts (no row values).
func (s *Service) Get(ctx context.Context, org ids.OrgID, id string) (*Session, error) {
	if ids.Check(ids.ObserveSession, id) != nil {
		return nil, ErrNotFound
	}
	var out *Session
	err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		row, err := store.GetObserveSession(ctx, tx, id, false)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		c, err := store.ConnectorAny(ctx, tx, row.ConnectorID)
		if err != nil {
			return err
		}
		fr, err := store.ObserveFrames(ctx, tx, id, false)
		if err != nil {
			return err
		}
		out = sessionOf(row, connectorFacts(c), factsOf(fr))
		live := liveSummary(out)
		out.Summary = live
		return nil
	})
	return out, err
}

// liveSummary recomputes the counts from the frames as they are now: exclusions and expired frames
// change it after the seal; the sealed numbers stay in the deed.
func liveSummary(x *Session) Summary {
	und := x.Row.State == StateSealed
	sum := Summarize(x.Row.ID, x.Frames, x.Connector.CaughtRows, und)
	if und {
		for i := range sum.Caught {
			if sum.Caught[i].Kind != caught.KindTruncate {
				sum.Caught[i].UndoPossible = x.undoableWith(sum)
			}
		}
	}
	return sum
}

func (x *Session) undoableWith(sum Summary) bool {
	for _, f := range x.Frames {
		if f.Expired && !f.Excluded {
			return false
		}
	}
	return sum.Transactions > sum.Excluded
}

func factsOf(rows []store.ObserveFrameRow) []FrameFacts {
	out := make([]FrameFacts, 0, len(rows))
	for _, r := range rows {
		f := FrameFacts{ID: r.FrameID, Excluded: r.Excluded, Expired: r.Expired, Total: r.ChangeCount, Flags: r.Flags, CommitTime: r.CommitTime,
			Counts: map[string]Counts{}}
		if r.CommitLSN != nil {
			f.CommitLSN = *r.CommitLSN
		}
		if r.Xid != nil {
			f.Xid = *r.Xid
		}
		if r.ChangesDigest != nil {
			f.Digest = *r.ChangesDigest
		}
		if len(r.Counts) > 0 {
			_ = json.Unmarshal(r.Counts, &f.Counts)
		}
		delete(f.Counts, "deedglass.ledger")
		out = append(out, f)
	}
	return out
}

// ListFilter filters List.
type ListFilter struct {
	Connector string // con_… or name
	State     string
	Caught    *bool
	Limit     int
	Cursor    string // "<unix_nano>.<obs_…>" from the previous page
}

// List returns this org's sessions, newest first, and whether there are more.
func (s *Service) List(ctx context.Context, org ids.OrgID, f ListFilter) ([]*Session, bool, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	sf := store.ObserveFilter{State: f.State, Caught: f.Caught, Limit: limit + 1}
	if f.Cursor != "" {
		at, id, ok := parseCursor(f.Cursor)
		if !ok {
			return nil, false, "", ErrBadCursor
		}
		sf.AfterAt, sf.AfterID = &at, id
	}
	var out []*Session
	more := false
	err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if f.Connector != "" {
			c, err := resolveConnector(ctx, tx, f.Connector)
			if errors.Is(err, ErrConnectorNotFound) {
				return nil // an unknown connector has no sessions
			}
			if err != nil {
				return err
			}
			sf.ConnectorID = c.ID
		}
		rows, err := store.ListObserveSessions(ctx, tx, sf)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			rows, more = rows[:limit], true
		}
		conns := map[string]ConnectorFacts{}
		for _, r := range rows {
			cf, ok := conns[r.ConnectorID]
			if !ok {
				c, err := store.ConnectorAny(ctx, tx, r.ConnectorID)
				if err != nil {
					return err
				}
				cf = connectorFacts(c)
				conns[r.ConnectorID] = cf
			}
			out = append(out, sessionOf(r, cf, nil))
		}
		return nil
	})
	next := ""
	if more && len(out) > 0 {
		last := out[len(out)-1].Row
		next = fmt.Sprintf("%d.%s", last.OpenedAt.UnixNano(), last.ID)
	}
	return out, more, next, err
}

// ErrBadCursor: the cursor isn't one List returned.
var ErrBadCursor = errors.New("observe: the cursor isn't valid")

func parseCursor(c string) (time.Time, string, bool) {
	ns, id, ok := strings.Cut(c, ".")
	if !ok || ids.Check(ids.ObserveSession, id) != nil {
		return time.Time{}, "", false
	}
	var n int64
	if _, err := fmt.Sscanf(ns, "%d", &n); err != nil || n <= 0 || fmt.Sprint(n) != ns {
		return time.Time{}, "", false
	}
	return time.Unix(0, n).UTC(), id, true
}

// Exclude leaves a transaction out of (or puts it back into) a sealed session's undo, as a sealed
// admin action. Allowed only while the session is sealed (never once an undo has started).
func (s *Service) Exclude(ctx context.Context, org ids.OrgID, actor Actor, id, frame string, excluded bool, requestID string) (*Session, error) {
	if ids.Check(ids.ObserveSession, id) != nil {
		return nil, ErrNotFound
	}
	if ids.Check(ids.DecodedTx, frame) != nil {
		return nil, ErrFrameNotAttached
	}
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		row, err := store.GetObserveSession(ctx, tx, id, true)
		if errors.Is(err, store.ErrNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.State != StateSealed {
			return ErrNotSealed
		}
		ok, err := store.SetObserveFrameExcluded(ctx, tx, id, frame, excluded, actor.Type+":"+actor.ID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrFrameNotAttached
		}
		_, err = s.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: AdminFrameExcluded, Actor: actor.ref(),
			Target: &deed.Ref{Type: "observe_session", ID: id}, RequestID: requestID,
			Data: map[string]any{"session": id, "frame": frame, "excluded": excluded}})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, org, id)
}

// Evidence returns a session and the full frames an undo plans from (excluded ones left out).
func (s *Service) Evidence(ctx context.Context, org ids.OrgID, id string) (*Session, []store.DecodedFrame, error) {
	x, err := s.Get(ctx, org, id)
	if err != nil {
		return nil, nil, err
	}
	var frames []store.DecodedFrame
	err = s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		frames, err = store.ObserveDecodedFrames(ctx, tx, id, false)
		return err
	})
	return x, frames, err
}

// DiffFrames returns the full stored frames for up to n of a session's transactions, newest first.
func (s *Service) DiffFrames(ctx context.Context, org ids.OrgID, x *Session, n int) ([]store.DecodedFrame, error) {
	var want []string
	for i := len(x.Frames) - 1; i >= 0 && len(want) < n; i-- {
		if !x.Frames[i].Expired {
			want = append(want, x.Frames[i].ID)
		}
	}
	var out []store.DecodedFrame
	err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = store.DecodedFramesByID(ctx, tx, want)
		return err
	})
	slices.Reverse(out)
	return out, err
}

// StartUndo moves a sealed session to undoing with its undo action, in tx. It reports false when
// the session is no longer sealed.
func StartUndo(ctx context.Context, tx pgx.Tx, id, actionID string) (bool, error) {
	return store.StartObserveUndo(ctx, tx, id, actionID)
}

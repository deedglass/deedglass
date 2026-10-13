// Package observeapi is observe mode's HTTP routes (S50 §3.4): open, close, list and read watched
// sessions, leave a transaction out of an undo, preview the undo (free, with the SQL a person runs
// themselves) and run it in one step (Pro). Agent keys get 403 on every route: an agent must not
// open, close, edit or undo its own recording.
package observeapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/observe"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/undoapi"
	"github.com/deedglass/deedglass/internal/witness/l2"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// MsgAgentKey is insufficient_scope's message for an agent key on these routes.
const MsgAgentKey = "Agents can't open, read, change or undo watched sessions; a person or an org admin key does. Use the Console or an org admin key."

// SystemAgents creates the console system agent on first use (identity.Service).
type SystemAgents interface {
	EnsureSystemAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, name string) (ids.AgentID, error)
}

// Submitter is the Gate.
type Submitter interface {
	Submit(ctx context.Context, pr identity.Principal, req gate.ActionRequest) (*gate.Action, error)
}

// API is the routes' dependencies. Pro is true when serve's licence check allowed undo_session
// (cmd/deedglass/licence.go); without it the run answers pro_required and everything else works.
type API struct {
	Service *observe.Service
	Planner *undoapi.Planner
	DB      *store.DB
	Agents  SystemAgents
	Gate    Submitter
	Pro     func() bool
}

// Routes are the seven observe routes. With a nil API (route-table tests) they answer service_unavailable.
func Routes(a *API) []httpx.Route {
	admin := []string{httpx.ScopeAdmin}
	return []httpx.Route{
		{Method: "POST", Pattern: "/v1/observe/sessions", OperationID: "openObserveSession", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "opening a watched session", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Idempotency: httpx.IdemOptional, MaxBody: 2 << 10, Handler: a.open},
		{Method: "POST", Pattern: "/v1/observe/sessions/{id}/close", OperationID: "closeObserveSession", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "closing a watched session", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Idempotency: httpx.IdemOptional, MaxBody: 1 << 10, Handler: a.close},
		{Method: "GET", Pattern: "/v1/observe/sessions", OperationID: "listObserveSessions", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleViewer, Purpose: "reading watched sessions", Scopes: admin, AgentKeyMessage: MsgAgentKey, Handler: a.list},
		{Method: "GET", Pattern: "/v1/observe/sessions/{id}", OperationID: "getObserveSession", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleViewer, Purpose: "reading a watched session", Scopes: admin, AgentKeyMessage: MsgAgentKey, Handler: a.get},
		{Method: "POST", Pattern: "/v1/observe/sessions/{id}/frames/{dtx}/exclude", OperationID: "excludeObserveFrame", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "leaving a transaction out of an undo", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Idempotency: httpx.IdemOptional, MaxBody: 1 << 10, Handler: a.exclude},
		{Method: "GET", Pattern: "/v1/observe/sessions/{id}/undo_preview", OperationID: "previewObserveUndo", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleViewer, Purpose: "previewing a session's undo", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Timeout: 30 * time.Second, Handler: a.preview},
		{Method: "POST", Pattern: "/v1/observe/sessions/{id}/undo", OperationID: "createObserveUndo", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "undoing a watched session", Scopes: admin, AgentKeyMessage: MsgAgentKey,
			Idempotency: httpx.IdemOptional, MaxBody: 4 << 10, Timeout: gate.MaxWait + 15*time.Second, Handler: a.undo},
	}
}

func (a *API) principal(r *http.Request) (httpx.Principal, observe.Actor, error) {
	if a == nil || a.Service == nil {
		return httpx.Principal{}, observe.Actor{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return pr, observe.Actor{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	switch {
	case pr.Kind == httpx.KindUser && !pr.UserID.IsZero():
		return pr, observe.Actor{Type: "user", ID: pr.UserID.String()}, nil
	case pr.Kind == httpx.KindAdminKey && !pr.KeyID.IsZero():
		return pr, observe.Actor{Type: "api_key", ID: pr.KeyID.String()}, nil
	}
	e := httpx.ErrInsufficientScope(httpx.ScopeAdmin)
	e.Message = MsgAgentKey
	return pr, observe.Actor{}, e
}

// openBody is POST /v1/observe/sessions.
type openBody struct {
	Connector *string `json:"connector"`
	Label     *string `json:"label"`
	Agent     *string `json:"agent"`
}

func (a *API) open(w http.ResponseWriter, r *http.Request) error {
	pr, actor, err := a.principal(r)
	if err != nil {
		return err
	}
	var b openBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.Connector == nil || *b.Connector == "" || len(*b.Connector) > 64 {
		return httpx.ErrValidation("connector", "connector is the connector's name or con_ id.")
	}
	req := observe.OpenRequest{Connector: *b.Connector}
	if b.Label != nil {
		req.Label = *b.Label
	}
	if b.Agent != nil {
		req.Agent = *b.Agent
	}
	x, err := a.Service.Open(r.Context(), pr.OrgID, actor, req)
	if err != nil {
		return mapErr(err, *b.Connector)
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), "", "")
	return httpx.WriteJSON(w, http.StatusCreated, Render(x, nil, nil))
}

func (a *API) close(w http.ResponseWriter, r *http.Request) error {
	pr, actor, err := a.principal(r)
	if err != nil {
		return err
	}
	if err := httpx.DecodeJSONOptional(r, &struct{}{}); err != nil {
		return err
	}
	x, err := a.Service.Close(r.Context(), pr.OrgID, actor, r.PathValue("id"))
	if err != nil {
		return mapErr(err, r.PathValue("id"))
	}
	return httpx.WriteJSON(w, http.StatusOK, Render(x, nil, nil))
}

func (a *API) list(w http.ResponseWriter, r *http.Request) error {
	pr, _, err := a.principal(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := observe.ListFilter{Connector: q.Get("connector"), State: q.Get("state"), Cursor: q.Get("cursor")}
	for k := range q {
		switch k {
		case "connector", "state", "caught", "limit", "cursor":
		default:
			return httpx.ErrValidation(k, "Filter with connector, state or caught; page with limit and cursor.")
		}
	}
	switch f.State {
	case "", observe.StateOpen, observe.StateClosing, observe.StateSealed, observe.StateUndoing, observe.StateUndone:
	default:
		return httpx.ErrValidation("state", "state is open, closing, sealed, undoing or undone.")
	}
	if v := q.Get("caught"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil || (v != "true" && v != "false") {
			return httpx.ErrValidation("caught", "caught is true or false.")
		}
		f.Caught = &b
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return httpx.ErrValidation("limit", "limit is a number from 1 to 100.")
		}
		f.Limit = n
	}
	xs, more, next, err := a.Service.List(r.Context(), pr.OrgID, f)
	if errors.Is(err, observe.ErrBadCursor) {
		return httpx.ErrValidation("cursor", "cursor is the next_cursor of the previous page.")
	}
	if err != nil {
		return err
	}
	out := List{Object: "list", Data: []SessionJSON{}, HasMore: more}
	if next != "" {
		out.NextCursor = &next
	}
	for _, x := range xs {
		out.Data = append(out.Data, Render(x, nil, nil))
	}
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) error {
	pr, _, err := a.principal(r)
	if err != nil {
		return err
	}
	n := 20
	for k, v := range r.URL.Query() {
		if k != "diff_frames" {
			return httpx.ErrValidation(k, "The session takes only diff_frames, from 0 to 100.")
		}
		x, err := strconv.Atoi(v[0])
		if err != nil || x < 0 || x > 100 {
			return httpx.ErrValidation("diff_frames", "diff_frames is a number from 0 to 100.")
		}
		n = x
	}
	x, err := a.Service.Get(r.Context(), pr.OrgID, r.PathValue("id"))
	if err != nil {
		return mapErr(err, r.PathValue("id"))
	}
	diffs := map[string]observe.Diff{}
	if n > 0 {
		frames, err := a.Service.DiffFrames(r.Context(), pr.OrgID, x, n)
		if err != nil {
			return err
		}
		for _, f := range frames {
			lf, err := l2Frame(f)
			if err != nil {
				return err
			}
			diffs[f.ID] = observe.BuildDiff(lf, observe.DiffRows)
		}
	}
	out := Render(x, x.Frames, diffs)
	return httpx.WriteJSON(w, http.StatusOK, out)
}

// excludeBody is POST …/frames/{dtx}/exclude.
type excludeBody struct {
	Excluded *bool `json:"excluded"`
}

func (a *API) exclude(w http.ResponseWriter, r *http.Request) error {
	pr, actor, err := a.principal(r)
	if err != nil {
		return err
	}
	var b excludeBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.Excluded == nil {
		return httpx.ErrValidation("excluded", "excluded is true (leave the transaction out of the undo) or false (put it back).")
	}
	x, err := a.Service.Exclude(r.Context(), pr.OrgID, actor, r.PathValue("id"), r.PathValue("dtx"), *b.Excluded, httpx.RequestID(r.Context()))
	if err != nil {
		return mapErr(err, r.PathValue("id"))
	}
	return httpx.WriteJSON(w, http.StatusOK, Render(x, x.Frames, nil))
}

// subject loads what a session undo plans from.
func (a *API) subject(ctx context.Context, org ids.OrgID, id string) (*observe.Session, undoapi.SessionSubject, error) {
	x, frames, err := a.Service.Evidence(ctx, org, id)
	if err != nil {
		return nil, undoapi.SessionSubject{}, err
	}
	return x, SubjectOf(x, frames), nil
}

// SubjectOf is a session as the undo planner reads it.
func SubjectOf(x *observe.Session, frames []store.DecodedFrame) undoapi.SessionSubject {
	s := undoapi.SessionSubject{ID: x.Row.ID, ConnectorID: x.Row.ConnectorID, State: x.Row.State, Frames: frames}
	if x.Row.ClosedAt != nil {
		s.ClosedAt = *x.Row.ClosedAt
	}
	if x.Row.UndoActionID != nil {
		s.UndoAction = *x.Row.UndoActionID
	}
	for _, f := range x.Frames {
		switch {
		case f.Excluded:
			s.Excluded = append(s.Excluded, f.ID)
		case f.Expired:
			s.Expired = append(s.Expired, f.ID)
		}
	}
	return s
}

func (a *API) preview(w http.ResponseWriter, r *http.Request) error {
	pr, _, err := a.principal(r)
	if err != nil {
		return err
	}
	if a.Planner == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	allow := false
	limit := a.Planner.Config().PreviewSteps
	for k, v := range r.URL.Query() {
		switch k {
		case "allow_triggers":
			if v[0] != "true" && v[0] != "false" {
				return httpx.ErrValidation(k, "allow_triggers is true or false.")
			}
			allow = v[0] == "true"
		case "steps_limit":
			n, err := strconv.Atoi(v[0])
			if err != nil || n < 1 || n > undoapi.MaxStepsLimit {
				return httpx.ErrValidation("steps_limit", "steps_limit is a number from 1 to 1000.")
			}
			limit = n
		default:
			return httpx.ErrValidation(k, "The preview takes allow_triggers and steps_limit.")
		}
	}
	ctx := r.Context()
	x, subj, err := a.subject(ctx, pr.OrgID, r.PathValue("id"))
	if err != nil {
		return mapErr(err, r.PathValue("id"))
	}
	if x.Row.State == observe.StateOpen || x.Row.State == observe.StateClosing {
		return httpx.E(httpx.CodeConflict, httpx.Msg("This session isn't sealed yet. Close it and wait for the seal, then preview its undo."))
	}
	pv, err := a.Planner.PreviewSession(ctx, pr.OrgID, subj, allow)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, a.renderPreview(x, subj, pv, limit))
}

// PreviewJSON is the observe_undo_preview object.
type PreviewJSON struct {
	Object     string                `json:"object"` // "observe_undo_preview"
	Session    string                `json:"session"`
	Available  bool                  `json:"available"`
	Summary    string                `json:"summary"`
	Headline   string                `json:"headline"`
	PlanDigest *string               `json:"plan_digest"`
	Connector  *undoapi.ConnectorRef `json:"connector"`
	Counts     undo.Counts           `json:"counts"`
	Rows       int                   `json:"rows"`
	Tables     []string              `json:"tables"`
	Steps      undoapi.StepList      `json:"steps"`
	Checks     []undo.Check          `json:"checks"`
	Conflicts  int                   `json:"conflicts"`
	Excluded   []string              `json:"excluded"`
	Notes      []string              `json:"notes"`
	Redacted   []string              `json:"redacted"`
	SQL        *string               `json:"sql"`
	Refusals   []string              `json:"refusals"`
	Pro        bool                  `json:"pro"`
	FreePath   string                `json:"free_path"`
	Observed   string                `json:"observed"`
}

func (a *API) renderPreview(x *observe.Session, subj undoapi.SessionSubject, pv *undoapi.SessionPreview, limit int) PreviewJSON {
	out := PreviewJSON{Object: "observe_undo_preview", Session: x.Row.ID, Available: pv.Available, Summary: pv.Summary, Connector: pv.Connector,
		Tables: []string{}, Checks: pv.Checks, Conflicts: pv.Conflicts, Excluded: append([]string{}, subj.Excluded...), Notes: []string{},
		Redacted: pv.Redacted, Refusals: []string{}, Steps: undoapi.StepList{Object: "list", Data: []undoapi.PreviewStep{}},
		Pro: a.Pro != nil && a.Pro(), FreePath: caught.FreeSessionPath(x.Row.ID), Observed: observe.NoteObserved}
	if out.Excluded == nil {
		out.Excluded = []string{}
	}
	if p := pv.Plan; p != nil {
		d := p.Digest
		out.PlanDigest, out.Counts, out.Rows, out.Tables, out.Headline = &d, p.Counts, p.Rows, p.TableNames(), undo.Headline(p)
		steps, more := undoapi.PreviewStepsOf(p, limit)
		out.Steps = undoapi.StepList{Object: "list", Total: len(p.Steps), HasMore: more > 0, Data: steps}
		out.Notes = append(out.Notes, p.Notes...)
		var warns []string
		for _, c := range pv.Checks {
			if c.Status == undo.StatusFail || c.Status == undo.StatusWarn {
				warns = append(warns, c.Title+": "+c.Detail)
			}
		}
		connName := ""
		if pv.Connector != nil {
			connName = pv.Connector.Name
		}
		sql, err := undo.Script(p, undo.ScriptOptions{Subject: "watched session " + x.Row.ID, Connector: connName,
			Generated: time.Now().UTC().Format("2006-01-02 15:04 UTC"), Excluded: subj.Excluded, Warnings: warns})
		var ref *undo.ScriptRefusal
		switch {
		case err == nil:
			out.SQL = &sql
		case errors.As(err, &ref):
			out.Refusals = append(out.Refusals, ref.Reasons...)
		default:
			out.Refusals = append(out.Refusals, "The undo SQL couldn't be written: "+err.Error())
		}
	} else {
		for _, c := range pv.Checks {
			if c.Status == undo.StatusFail && c.Detail != "" {
				out.Refusals = append(out.Refusals, c.Detail)
			}
		}
	}
	return out
}

// undoBody is POST /v1/observe/sessions/{id}/undo.
type undoBody struct {
	PlanDigest    *string  `json:"plan_digest"`
	Reason        *string  `json:"reason"`
	AllowTriggers bool     `json:"allow_triggers"`
	Wait          *float64 `json:"wait"`
}

func (a *API) undo(w http.ResponseWriter, r *http.Request) error {
	pr, _, err := a.principal(r)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	var b undoBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	// Another org's session answers exactly like an unknown one, before anything else is said.
	if _, err := a.Service.Get(r.Context(), pr.OrgID, id); err != nil {
		return mapErr(err, id)
	}
	if a.Pro == nil || !a.Pro() {
		return httpx.E(httpx.CodeProRequired, httpx.Msgf("Undoing a whole session in one step is part of Pro. Per-action undo and the undo SQL stay free: deedglass observe show %s --undo-sql.", clip(id)))
	}
	if b.PlanDigest == nil || !reDigest(*b.PlanDigest) {
		return httpx.ErrValidation("plan_digest", "plan_digest is the preview's plan_digest, sha256: and 64 hex digits. Preview the undo first.")
	}
	if b.Reason == nil || len(*b.Reason) == 0 || !utf8.ValidString(*b.Reason) {
		return httpx.ErrValidation("reason", undo.MsgReason)
	}
	if utf8.RuneCountInString(*b.Reason) > undoapi.MaxReason {
		return httpx.ErrValidation("reason", undo.MsgReasonLong)
	}
	if b.Wait != nil && (*b.Wait < 0 || *b.Wait > gate.MaxWait.Seconds()) {
		return httpx.ErrValidation("wait", "wait is seconds from 0 to 25.")
	}
	ctx := r.Context()
	// The console system agent acts (committed first: the Gate reads it on its own connection).
	var console ids.AgentID
	if err := a.DB.OrgTx(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		console, err = a.Agents.EnsureSystemAgent(ctx, tx, pr.OrgID, identity.ConsoleAgent)
		return err
	}); err != nil {
		return err
	}
	var act *gate.Action
	var submitErr error
	err = a.DB.OrgTx(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		// One undo per session at a time: the lock serializes concurrent requests for it.
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtext('deedglass.observe_undo:' || $1))`, id); err != nil {
			return err
		}
		x, subj, err := a.subject(ctx, pr.OrgID, id)
		if err != nil {
			return mapErr(err, id)
		}
		switch x.Row.State {
		case observe.StateUndoing, observe.StateUndone:
			by := ""
			if x.Row.UndoActionID != nil {
				by = *x.Row.UndoActionID
			}
			return httpx.E(httpx.CodeAlreadyUndone, httpx.Msgf("This session was already undone, or its undo is waiting: %s.", by))
		case observe.StateOpen, observe.StateClosing:
			return httpx.E(httpx.CodeConflict, httpx.Msg("This session isn't sealed yet. Close it and wait for the seal, then undo it."))
		}
		pv, err := a.Planner.PreviewSession(ctx, pr.OrgID, subj, b.AllowTriggers)
		if err != nil {
			return err
		}
		if !pv.Available {
			if pv.Conflicts > 0 {
				return httpx.E(httpx.CodeUndoConflict, httpx.Msgf("%s changed again after the session ended, so the undo would overwrite newer data. Nothing ran. Leave out the transaction that touched them, or undo those rows by hand.", rowsWord(pv.Conflicts)), httpx.WithChecks(pv.Checks))
			}
			return httpx.E(httpx.CodeUndoUnavailable, httpx.Msg(undo.MsgUnavailable(id, undo.Failed(pv.Checks))), httpx.WithChecks(pv.Checks))
		}
		if pv.Plan == nil || pv.Plan.Digest != *b.PlanDigest {
			return httpx.E(httpx.CodeUndoPlanStale, httpx.Param("plan_digest"))
		}
		user, obo := a.onBehalf(ctx, pr)
		undoapi.RememberSession(pv.Plan, id, "")
		excluded := append([]string{}, subj.Excluded...)
		slices.Sort(excluded)
		args, _ := json.Marshal(undoapi.SessionArgs{Session: id, PlanDigest: pv.Plan.Digest, AllowTriggers: b.AllowTriggers, Reason: *b.Reason, Excluded: excluded})
		req := gate.ActionRequest{Tool: pv.Connector.Name + "." + undoapi.SessionTool, Args: args, Mode: gate.ModeGateway,
			OnBehalfOf: obo, RequestID: httpx.RequestID(ctx), Context: &gate.RequestContext{Transport: gate.TransportREST}}
		if b.Wait != nil {
			req.Wait, req.WaitSet = time.Duration(*b.Wait*float64(time.Second)), true
		}
		cp := identity.Principal{Kind: httpx.KindConsole, OrgID: pr.OrgID, AgentID: console, UserID: user, Role: pr.Role,
			AgentName: identity.ConsoleAgent, AgentStatus: "active", APIVersion: pr.APIVersion, OrgName: pr.OrgName}
		act, submitErr = a.Gate.Submit(ctx, cp, req)
		if act == nil {
			return nil
		}
		ok, err := observe.StartUndo(ctx, tx, id, act.ID)
		if err != nil {
			return err
		}
		if !ok {
			return httpx.E(httpx.CodeAlreadyUndone)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if submitErr != nil {
		var he *httpx.Error
		if errors.As(submitErr, &he) && act != nil {
			he.Action = &act.Action
		}
		return submitErr
	}
	httpx.Annotate(ctx, pr.OrgID.String(), "", act.ID)
	return httpx.WriteJSON(w, act.HTTPStatus(), act)
}

// onBehalf is the person the undo runs for: the signed-in person, or for an org admin key the
// person who minted it (S22's self rule then keeps them from countersigning their own undo). A key
// the operator minted stands for itself (as undoapi does).
func (a *API) onBehalf(ctx context.Context, pr httpx.Principal) (ids.UserID, string) {
	if pr.Kind != httpx.KindAdminKey {
		return pr.UserID, pr.UserID.String()
	}
	var by *string
	_ = a.DB.OrgReadTx(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT created_by FROM deedglass.api_keys WHERE id = $1`, pr.KeyID).Scan(&by)
	})
	if by != nil {
		if u, err := ids.Parse[ids.UserKind](*by); err == nil {
			return u, u.String()
		}
	}
	return ids.UserID{}, pr.KeyID.String()
}

func rowsWord(n int) string {
	if n == 1 {
		return "1 row"
	}
	return undo.Thousands(n) + " rows"
}

func reDigest(s string) bool {
	if len(s) != 71 || s[:7] != "sha256:" {
		return false
	}
	for _, c := range s[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func clip(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		return string([]rune(s)[:40]) + "…"
	}
	return s
}

// mapErr turns the service's errors into the catalog's.
func mapErr(err error, ref string) error {
	var ne *observe.NotEnabledError
	switch {
	case errors.As(err, &ne):
		return httpx.E(httpx.CodeObserveNotEnabled, httpx.Param("connector"),
			httpx.Msgf("Observe mode is off for %s. Turn it on in Console › Connectors › %s, or set witness.observe.enabled: true in its config.", ne.Connector, ne.Connector))
	case errors.Is(err, observe.ErrNotFound):
		return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no watched session with id %s in this org. Check the id.", clip(ref)))
	case errors.Is(err, observe.ErrConnectorNotFound):
		return httpx.E(httpx.CodeNotFound, httpx.Param("connector"), httpx.Msgf("There's no connector named %s in this org. Check Console › Connectors.", clip(ref)))
	case errors.Is(err, observe.ErrNotPostgres):
		return httpx.ErrValidation("connector", "Observe mode reads Postgres connectors only.")
	case errors.Is(err, observe.ErrAgentNotFound):
		return httpx.ErrValidation("agent", "There's no agent with that name or id in this org.")
	case errors.Is(err, observe.ErrLabel):
		return httpx.ErrValidation("label", "label is 1 to 80 characters on one line.")
	case errors.Is(err, observe.ErrNotSealed):
		return httpx.E(httpx.CodeConflict, httpx.Msg("A transaction can be left out only while the session is sealed and before its undo starts."))
	case errors.Is(err, observe.ErrFrameNotAttached):
		return httpx.E(httpx.CodeNotFound, httpx.Param("dtx"), httpx.Msg("That transaction isn't in this session. Check the dtx_ id in the session's transactions."))
	}
	return err
}

func l2Frame(f store.DecodedFrame) (l2.Frame, error) {
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
	err := x.ParseParts(f.Ledger, f.Tables, f.Counts, f.Changes)
	return x, err
}

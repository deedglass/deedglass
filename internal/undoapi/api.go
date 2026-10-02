package undoapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// The undo routes (S23 §3.7): POST /v1/actions/{id}/undo/preview and POST /v1/actions/{id}/undo.
// A signed-in admin or owner (with CSRF), or an org admin key. Agent keys get insufficient_scope:
// an undo is something a person starts.

// PolicyEvaluator is the dry run (policy.Service.Evaluate).
type PolicyEvaluator interface {
	Evaluate(ctx context.Context, caller policy.Caller, req policy.DryRunRequest) (*policy.DryRunResult, error)
}

// SystemAgents creates the console system agent on first use (identity.Service).
type SystemAgents interface {
	EnsureSystemAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, name string) (ids.AgentID, error)
}

// Submitter is the Gate (gate.Pipeline.Submit).
type Submitter interface {
	Submit(ctx context.Context, pr identity.Principal, req gate.ActionRequest) (*gate.Action, error)
}

// API is the undo routes' dependencies.
type API struct {
	Planner  *Planner
	DB       *store.DB
	Policies PolicyEvaluator
	Agents   SystemAgents
	Gate     Submitter
}

// MaxStepsLimit is the most steps ?steps_limit= returns.
const MaxStepsLimit = 1000

// MaxReason is the most characters a reason may have.
const MaxReason = 280

// Routes are the two undo routes. With a nil API (route-table tests) they answer service_unavailable.
func Routes(a *API) []httpx.Route {
	admin := []string{httpx.ScopeAdmin}
	return []httpx.Route{
		{Method: "POST", Pattern: "/v1/actions/{id}/undo/preview", OperationID: "previewUndo", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "previewing an undo", Scopes: admin, AgentKeyMessage: undo.MsgAgentKey,
			Idempotency: httpx.IdemNever, MaxBody: 1 << 10, Handler: a.preview},
		{Method: "POST", Pattern: "/v1/actions/{id}/undo", OperationID: "createUndo", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "undoing an action", Scopes: admin, AgentKeyMessage: undo.MsgAgentKey,
			Idempotency: httpx.IdemOptional, MaxBody: 4 << 10, Timeout: gate.MaxWait + 15*time.Second, Handler: a.run},
	}
}

func (a *API) principal(r *http.Request) (httpx.Principal, error) {
	if a == nil || a.Planner == nil {
		return httpx.Principal{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	pr, ok := httpx.PrincipalFrom(r.Context())
	if !ok || pr.OrgID.IsZero() {
		return pr, httpx.E(httpx.CodeAuthenticationRequired)
	}
	if pr.Kind == httpx.KindAgentKey {
		e := httpx.ErrInsufficientScope(httpx.ScopeActionsUndo)
		e.Message = undo.MsgAgentKey
		return pr, e
	}
	return pr, nil
}

// previewBody is the preview's request.
type previewBody struct {
	AllowTriggers bool `json:"allow_triggers"`
}

// PreviewJSON is the undo_preview object.
type PreviewJSON struct {
	Object     string        `json:"object"` // "undo_preview"
	Action     string        `json:"action"`
	Tool       string        `json:"tool"`
	Available  bool          `json:"available"`
	Summary    string        `json:"summary"`
	Headline   string        `json:"headline"`
	PlanDigest *string       `json:"plan_digest"`
	Connector  *ConnectorRef `json:"connector"`
	Evidence   *EvidenceJSON `json:"evidence"`
	Counts     undo.Counts   `json:"counts"`
	Rows       int           `json:"rows"`
	Tables     []string      `json:"tables"`
	Steps      StepList      `json:"steps"`
	Checks     []undo.Check  `json:"checks"`
	Notes      []string      `json:"notes"`
	Redacted   []string      `json:"redacted"`
	Policy     *PolicyJSON   `json:"policy"`
	UndoneBy   *string       `json:"undone_by"`
}

// EvidenceJSON is the preview's evidence summary.
type EvidenceJSON struct {
	Level       string `json:"level"`
	Verdict     string `json:"verdict"`
	Xid         string `json:"xid"`
	CommitLSN   string `json:"commit_lsn"`
	CommittedAt string `json:"committed_at"`
	Changes     int    `json:"changes"`
}

// StepList is the first steps of the plan.
type StepList struct {
	Object  string        `json:"object"` // "list"
	Total   int           `json:"total"`
	HasMore bool          `json:"has_more"`
	Data    []PreviewStep `json:"data"`
}

// PreviewStep is one step as a person reads it: the values before ("expect", now) and after
// ("set", what the undo writes). A sensitive value is •••; NULL is null.
type PreviewStep struct {
	Op     string             `json:"op"`
	Table  string             `json:"table"`
	Key    map[string]string  `json:"key"`
	Set    map[string]*string `json:"set,omitempty"`
	Expect map[string]*string `json:"expect,omitempty"`
}

// PolicyJSON is the dry run of the policy for the console agent.
type PolicyJSON struct {
	Decision string  `json:"decision"`
	RuleID   *string `json:"rule_id"`
	Reason   *string `json:"reason"`
}

func (a *API) preview(w http.ResponseWriter, r *http.Request) error {
	pr, err := a.principal(r)
	if err != nil {
		return err
	}
	var b previewBody
	if err := httpx.DecodeJSONOptional(r, &b); err != nil {
		return err
	}
	limit := a.Planner.Config().PreviewSteps
	q := r.URL.Query()
	for k := range q {
		if k != "steps_limit" {
			return httpx.ErrValidation(k, "The preview takes only steps_limit, from 1 to 1000.")
		}
	}
	if v := q.Get("steps_limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxStepsLimit {
			return httpx.ErrValidation("steps_limit", "steps_limit is a number from 1 to 1000.")
		}
		limit = n
	}
	id := r.PathValue("id")
	loc := a.orgLocation(r.Context(), pr.OrgID)
	pv, err := a.Planner.Preview(r.Context(), pr.OrgID, id, b.AllowTriggers, loc)
	if errors.Is(err, ErrNotFound) {
		return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no action with id %s in this org. Check the id.", clipID(id)))
	}
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), pr.OrgID.String(), "", id)
	return httpx.WriteJSON(w, http.StatusOK, a.render(r.Context(), pr.OrgID, pv, limit))
}

// render is the undo_preview object.
func (a *API) render(ctx context.Context, org ids.OrgID, pv *Preview, limit int) PreviewJSON {
	out := PreviewJSON{Object: "undo_preview", Action: pv.Action, Tool: pv.Tool, Available: pv.Available, Summary: pv.Summary, Connector: pv.Connector,
		Tables: []string{}, Checks: pv.Checks, Notes: []string{}, Redacted: pv.Redacted,
		Steps: StepList{Object: "list", Data: []PreviewStep{}}}
	if p := pv.Plan; p != nil {
		d := p.Digest
		out.PlanDigest, out.Counts, out.Rows, out.Tables, out.Headline = &d, p.Counts, p.Rows, p.TableNames(), undo.Headline(p)
		steps, more := previewSteps(p, limit)
		out.Steps = StepList{Object: "list", Total: len(p.Steps), HasMore: more > 0, Data: steps}
		out.Notes = append(out.Notes, p.Notes...)
	}
	if ev := pv.Evidence; ev != nil && ev.Ref.DecodedTx != "" {
		out.Evidence = &EvidenceJSON{Level: nonEmpty(pv.Level, "L2"), Verdict: pv.Verdict, Xid: strconv.FormatInt(ev.Ref.Xid, 10),
			CommitLSN: ev.Ref.CommitLSN, CommittedAt: ev.Ref.CommittedAt, Changes: ev.Total}
	}
	if pv.Live != nil && pv.Live.State == "sealed" && pv.Live.ExecutionStatus != nil && *pv.Live.ExecutionStatus != "failed" {
		u := pv.Live.ID
		out.UndoneBy = &u
	}
	out.Policy = a.dryRun(ctx, org, pv)
	return out
}

// dryRun is the policy's decision for the console agent running <connector>.undo (S11's
// evaluation, nothing reserved, nothing written). nil when it can't run.
func (a *API) dryRun(ctx context.Context, org ids.OrgID, pv *Preview) *PolicyJSON {
	if a.Policies == nil || pv.Connector == nil {
		return nil
	}
	in := map[string]any{
		"tool":  map[string]any{"name": pv.Connector.Name + "." + undoTool, "connector_type": pv.Connector.Type, "side_effect": "write"},
		"agent": map[string]any{"name": identity.ConsoleAgent},
		"args":  map[string]any{"undoes": pv.Action, "plan_digest": "", "allow_triggers": false, "reason": "preview"},
	}
	if pv.Plan != nil {
		in["args"].(map[string]any)["plan_digest"] = pv.Plan.Digest
	}
	res, err := a.Policies.Evaluate(ctx, policy.Caller{Org: org.String(), Actor: policy.SystemActor}, policy.DryRunRequest{Input: in})
	if err != nil || res == nil {
		return nil
	}
	return &PolicyJSON{Decision: string(res.Decision), RuleID: res.RuleID, Reason: res.Reason}
}

// runBody is POST /v1/actions/{id}/undo.
type runBody struct {
	PlanDigest    *string  `json:"plan_digest"`
	Reason        *string  `json:"reason"`
	AllowTriggers bool     `json:"allow_triggers"`
	Wait          *float64 `json:"wait"`
}

var reDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (a *API) run(w http.ResponseWriter, r *http.Request) error {
	pr, err := a.principal(r)
	if err != nil {
		return err
	}
	var b runBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	if b.PlanDigest == nil || !reDigest.MatchString(*b.PlanDigest) {
		return httpx.ErrValidation("plan_digest", "plan_digest is the preview's plan_digest, sha256: and 64 hex digits. Run deedglass undo preview first.")
	}
	if b.Reason == nil || len(*b.Reason) == 0 || !utf8.ValidString(*b.Reason) {
		return httpx.ErrValidation("reason", undo.MsgReason)
	}
	if utf8.RuneCountInString(*b.Reason) > MaxReason {
		return httpx.ErrValidation("reason", undo.MsgReasonLong)
	}
	if b.Wait != nil && (*b.Wait < 0 || *b.Wait > gate.MaxWait.Seconds()) {
		return httpx.ErrValidation("wait", "wait is seconds from 0 to 25.")
	}
	id := r.PathValue("id")
	ctx := r.Context()
	loc := a.orgLocation(ctx, pr.OrgID)
	pv, err := a.Planner.Preview(ctx, pr.OrgID, id, b.AllowTriggers, loc)
	if errors.Is(err, ErrNotFound) {
		return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no action with id %s in this org. Check the id.", clipID(id)))
	}
	if err != nil {
		return err
	}
	if lu := pv.Live; lu != nil {
		return alreadyUndone(id, lu, loc)
	}
	if !pv.Available {
		return httpx.E(httpx.CodeUndoUnavailable, httpx.Msg(undo.MsgUnavailable(id, undo.Failed(pv.Checks))), httpx.WithChecks(pv.Checks))
	}
	if pv.Plan == nil || pv.Plan.Digest != *b.PlanDigest {
		return httpx.E(httpx.CodeUndoPlanStale, httpx.Param("plan_digest"))
	}
	// The console system agent acts; the person is on_behalf_of (so S22's self rule applies).
	var console ids.AgentID
	if err := a.DB.OrgTx(ctx, pr.OrgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		console, err = a.Agents.EnsureSystemAgent(ctx, tx, pr.OrgID, identity.ConsoleAgent)
		return err
	}); err != nil {
		return err
	}
	user, obo := a.onBehalf(ctx, pr)
	remember(pv.Plan.Digest, sectionOf(pv.Plan, id, a.personName(ctx, pr.OrgID, user)))
	args, _ := json.Marshal(Args{Undoes: id, PlanDigest: pv.Plan.Digest, AllowTriggers: b.AllowTriggers, Reason: *b.Reason})
	req := gate.ActionRequest{Tool: pv.Connector.Name + "." + undoTool, Args: args, Mode: gate.ModeGateway, UndoesActionID: id,
		OnBehalfOf: obo, RequestID: httpx.RequestID(ctx), Context: &gate.RequestContext{Transport: gate.TransportREST}}
	if b.Wait != nil {
		req.Wait, req.WaitSet = time.Duration(*b.Wait*float64(time.Second)), true
	}
	cp := identity.Principal{Kind: httpx.KindConsole, OrgID: pr.OrgID, AgentID: console, UserID: user, Role: pr.Role,
		AgentName: identity.ConsoleAgent, AgentStatus: "active", Livemode: false, APIVersion: pr.APIVersion, OrgName: pr.OrgName}
	act, err := a.Gate.Submit(ctx, cp, req)
	if errors.Is(err, gate.ErrUndoTaken) {
		if lu, lerr := a.live(ctx, pr.OrgID, id); lerr == nil {
			return alreadyUndone(id, lu, loc)
		}
		return httpx.E(httpx.CodeAlreadyUndone)
	}
	if act != nil {
		act.Undo = &ledger.ActionUndo{Undoes: &id}
		httpx.Annotate(ctx, pr.OrgID.String(), console.String(), act.ID)
	}
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && act != nil {
			he.Action = &act.Action
		}
		return err
	}
	return httpx.WriteJSON(w, act.HTTPStatus(), act)
}

func alreadyUndone(id string, lu *store.LiveUndo, loc *time.Location) error {
	if lu.State == "sealed" && lu.ExecutionStatus != nil && *lu.ExecutionStatus != "failed" {
		at := ""
		if lu.SealedAt != nil {
			at = clock(*lu.SealedAt, loc)
		}
		return httpx.E(httpx.CodeAlreadyUndone, httpx.Msg(undo.MsgAlreadyUndone(id, lu.ID, at)))
	}
	return httpx.E(httpx.CodeAlreadyUndone, httpx.Msg(undo.MsgAlreadyWaiting(id, lu.ID)))
}

func (a *API) live(ctx context.Context, org ids.OrgID, id string) (*store.LiveUndo, error) {
	var lu store.LiveUndo
	err := a.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		lu, err = store.GetLiveUndo(ctx, tx, org, id)
		return err
	})
	return &lu, err
}

// orgLocation is the org's time zone (UTC when unknown), for the times messages name.
func (a *API) orgLocation(ctx context.Context, org ids.OrgID) *time.Location {
	var tz string
	if a.DB == nil {
		return time.UTC
	}
	err := a.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT timezone FROM deedglass.orgs WHERE id = $1`, org).Scan(&tz)
	})
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// onBehalf is the person an undo runs for: the signed-in person, or for an org admin key the person
// who minted it (S22's self rule then keeps them from countersigning their own undo). A key the
// operator minted stands for itself.
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

// personName is how the approval names the person: their display name, else the part of their
// email before the @, else nothing.
func (a *API) personName(ctx context.Context, org ids.OrgID, user ids.UserID) string {
	if user.IsZero() || a.DB == nil {
		return ""
	}
	var name string
	_ = a.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(u.display_name, pg_catalog.split_part(u.email, '@', 1)) FROM deedglass.users AS u WHERE u.id = $1`,
			user).Scan(&name)
	})
	return name
}

func clipID(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		return string([]rune(s)[:40]) + "…"
	}
	return s
}

// previewSteps are the first limit steps as a person reads them, and how many more there are.
func previewSteps(p *undo.Plan, limit int) ([]PreviewStep, int) {
	out := []PreviewStep{}
	guards := map[uint32]map[string]undo.Guard{}
	for _, g := range p.Guards {
		t, _ := p.Table(g.Table)
		if guards[g.Table] == nil {
			guards[g.Table] = map[string]undo.Guard{}
		}
		guards[g.Table][g.Key.ID(t.PK)] = g
	}
	for i, s := range p.Steps {
		if i >= limit {
			return out, len(p.Steps) - limit
		}
		t, _ := p.Table(s.Table)
		ps := PreviewStep{Op: string(s.Op), Table: t.String(), Key: map[string]string(s.Key)}
		g := guards[s.Table][s.Key.ID(t.PK)]
		switch s.Op {
		case undo.OpUpdate:
			ps.Set, ps.Expect = map[string]*string{}, map[string]*string{}
			for c, v := range s.Set {
				ps.Set[c] = shown(v)
				if e, ok := g.Expect[c]; ok {
					ps.Expect[c] = shown(e)
				}
			}
		case undo.OpInsert:
			ps.Set = map[string]*string{}
			for c, v := range s.Set {
				ps.Set[c] = shown(v)
			}
		case undo.OpDelete:
			ps.Expect = map[string]*string{}
			for c, v := range g.Expect {
				ps.Expect[c] = shown(v)
			}
		}
		out = append(out, ps)
	}
	return out, 0
}

// shown is a value as the preview shows it: its text, null for NULL, ••• for a sensitive column.
func shown(v undo.Value) *string {
	switch {
	case v.N:
		return nil
	case v.D != "":
		s := undo.Hidden
		return &s
	case v.X != "":
		s := `\x` + v.X
		return &s
	case v.T != nil:
		s := *v.T
		return &s
	}
	return nil
}

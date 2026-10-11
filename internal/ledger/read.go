package ledger

//deedglass:allow-sql-build list filters join fixed WHERE fragments and $n placeholder numbers; every filter value is a bound parameter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	gate "github.com/deedglass/deedglass/internal/gate/lifecycle"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/witness"
)

// List is every list result: {"object":"list","data":[…],"has_more":…,"next_cursor":…}.
type List[T any] struct {
	Object     string  `json:"object"`
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

func newList[T any](data []T, next string) *List[T] {
	if data == nil {
		data = []T{}
	}
	l := &List[T]{Object: "list", Data: data, HasMore: next != ""}
	if next != "" {
		l.NextCursor = &next
	}
	return l
}

// Time is a timestamp as the API shows it: RFC 3339 UTC with milliseconds.
type Time = httpx.Time

func tptr(t *time.Time) *Time {
	if t == nil {
		return nil
	}
	v := Time(*t)
	return &v
}

// AgentRef names the agent on an action.
type AgentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ActionPolicy is the decision and the policy version that made it (the deed's policy section).
type ActionPolicy struct {
	ID        *string `json:"id"`
	VersionID *string `json:"version_id"`
	Mode      string  `json:"mode"`
	Decision  *string `json:"decision"`
	RuleID    *string `json:"rule_id"`
}

// ActionExecution is what happened when the tool ran; null when it never ran. Result is set only
// on the Gate's own responses (POST /v1/actions and the proxy result), never on lists.
type ActionExecution struct {
	Status       *string         `json:"status"`
	StartedAt    *Time           `json:"started_at"`
	FinishedAt   *Time           `json:"finished_at"`
	LatencyMS    *int64          `json:"latency_ms"`
	ErrorCode    *string         `json:"error_code"`
	ErrorMessage *string         `json:"error_message"`
	Result       json.RawMessage `json:"result,omitzero"`
}

// ActionWitness is the witness object (S19 §3.9); null until a witness ran. Lists carry the
// claim (level, verdict, method, required_level, detail_digest); the Gate's own responses and GET
// /v1/actions/{id} add the summary, the reasons that kept it from VERIFIED or contradicted it
// (violations and gaps), and witnessed_at. ?expand[]=witness.detail adds the full witness_detail
// v1, with passes and notes.
type ActionWitness struct {
	Object        string            `json:"object"` // "witness"
	Level         string            `json:"level"`
	Verdict       string            `json:"verdict"`
	Method        *string           `json:"method"`
	RequiredLevel *string           `json:"required_level"`
	Summary       *string           `json:"summary,omitzero"`
	Reasons       *[]witness.Reason `json:"reasons,omitzero"`
	WitnessedAt   *string           `json:"witnessed_at,omitzero"`
	DetailDigest  *string           `json:"detail_digest"`
	Detail        json.RawMessage   `json:"detail,omitzero"`
	// History is every witness section sealed about the action, oldest first (S21): the action
	// deed, then any amendment deeds. GET /v1/actions/{id} only.
	History []WitnessHistory `json:"history,omitzero"`
}

// WitnessHistory is one sealed witness section: the deed that carries it, its kind (action or
// amendment), the claim and when it was sealed. A section a later amendment replaced names the
// reason it stopped where it did (l2_timeout, …).
type WitnessHistory struct {
	Deed    string  `json:"deed"`
	Kind    string  `json:"kind"`
	Level   string  `json:"level"`
	Verdict string  `json:"verdict"`
	At      string  `json:"at"`
	Reason  *string `json:"reason,omitempty"`
}

// fill adds what the stored witness_detail says: the summary, the violations and gaps, and when.
// keepDetail keeps the whole detail (?expand[]=witness.detail).
func (w *ActionWitness) fill(detail []byte, keepDetail bool) {
	rs := []witness.Reason{}
	d, ok := witness.ParseDetail(detail)
	if ok {
		for _, r := range d.Reasons {
			if r.Class == witness.Violation || r.Class == witness.Gap {
				rs = append(rs, r)
			}
		}
		sum := d.Summary
		if sum == "" && len(d.Reasons) > 0 {
			sum = d.Reasons[0].Message
		}
		if sum != "" {
			w.Summary = &sum
		}
		if d.WitnessedAt != "" {
			at := d.WitnessedAt
			w.WitnessedAt = &at
		}
	}
	w.Reasons = &rs
	if keepDetail && detail != nil {
		w.Detail = json.RawMessage(detail)
	}
}

// Codes lists the witness reasons' codes, in order (nil when the reasons weren't read).
func (w *ActionWitness) Codes() []string {
	if w == nil || w.Reasons == nil {
		return nil
	}
	out := make([]string, 0, len(*w.Reasons))
	for _, r := range *w.Reasons {
		out = append(out, string(r.Code))
	}
	return out
}

// Cause is the S13 cause of an unknown outcome the witness recorded (timeout, edge_lost,
// client_cancelled, outcome_unknown), or "".
func (w *ActionWitness) Cause() string {
	if w == nil || w.Reasons == nil {
		return ""
	}
	for _, r := range *w.Reasons {
		if c, ok := r.Evidence["cause"].(string); ok {
			return c
		}
		if r.Code == witness.CodeWitnessGaveUp {
			return string(r.Code)
		}
	}
	return ""
}

// ActionDecision is the policy decision as an agent reads it (S13): S11's Decision, its source and
// its message verbatim, the policy version that made it, and the shadow decision next to it.
type ActionDecision struct {
	Outcome       string          `json:"outcome"`
	Code          *string         `json:"code"`
	Source        *string         `json:"source"`
	RuleID        *string         `json:"rule_id"`
	Reason        *string         `json:"reason"`
	Message       *string         `json:"message"`
	Policy        *string         `json:"policy"`
	PolicyVersion *string         `json:"policy_version"`
	Shadow        *ShadowDecision `json:"shadow"`
	// Anomaly is the decision trace's line about the anomaly facts the policy saw (S27 §3.8,
	// additive): "anomaly facts: volume_spike". Omitted when the facts were inactive.
	Anomaly *string `json:"anomaly,omitempty"`
}

// ShadowDecision is what the shadow policy would have decided (recorded, never enforced).
type ShadowDecision struct {
	Outcome       string  `json:"outcome"`
	RuleID        *string `json:"rule_id"`
	PolicyVersion *string `json:"policy_version"`
}

// ActionApproval is the approval an action waits for, or waited for.
type ActionApproval struct {
	ID        string `json:"id"`
	Status    string `json:"status"` // pending | approved | denied | expired | cancelled
	ExpiresAt Time   `json:"expires_at"`
	// DecisionReason is the reason a person gave for denying (S22, additive): the approval page
	// says "the agent sees your reason". Absent otherwise; an approver's own reasons and names
	// stay on the approval object.
	DecisionReason *string `json:"decision_reason,omitempty"`
}

// NextAction tells the caller what happens next: wait for a person (await_approval) or run the
// tool at the edge and report the result (execute_at_edge, proxy mode). Null otherwise.
type NextAction struct {
	Type      string  `json:"type"`
	Approval  *string `json:"approval,omitempty"`
	ExpiresAt *Time   `json:"expires_at,omitempty"`
	PollURL   *string `json:"poll_url,omitempty"`
	ReportURL *string `json:"report_url,omitempty"`
	Deadline  *Time   `json:"deadline,omitempty"`
	// NotBefore and NotAfter (S40): an approved scheduled action runs inside this window
	// (type scheduled); past not_after it is cancelled and sealed with schedule_missed.
	NotBefore *Time `json:"not_before,omitempty"`
	NotAfter  *Time `json:"not_after,omitempty"`
}

// ActionSchedule is the action's schedule (S40): when an approved action may run. Set once at
// creation, never changed.
type ActionSchedule struct {
	NotBefore Time `json:"not_before"`
	NotAfter  Time `json:"not_after"`
}

// Action statuses (S13): the one field an agent needs. state is the exact lifecycle state.
const (
	StatusProcessing      = "processing"
	StatusAuthorized      = "authorized"
	StatusPendingApproval = "pending_approval"
	StatusDenied          = "denied"
	StatusExecuted        = "executed"
	StatusFailed          = "failed"
	StatusExpired         = "expired"
	StatusCancelled       = "cancelled"
)

// Statuses lists the status enum in documentation order.
var Statuses = []string{StatusProcessing, StatusAuthorized, StatusPendingApproval, StatusDenied, StatusExecuted,
	StatusFailed, StatusExpired, StatusCancelled}

// StatusOf maps a state (and, for sealed actions, the path that led there) to the status. A sealed
// action keeps the status of the state it was sealed from.
func StatusOf(state, mode string, wasDenied, wasExpired, wasCancelled, wasFailed bool) string {
	switch gate.State(state) {
	case gate.PendingApproval:
		return StatusPendingApproval
	case gate.Executing:
		if mode == "proxy" {
			return StatusAuthorized
		}
		return StatusProcessing
	case gate.Denied:
		return StatusDenied
	case gate.Expired:
		return StatusExpired
	case gate.Cancelled:
		return StatusCancelled
	case gate.Failed:
		return StatusFailed
	case gate.Verified, gate.Mismatch, gate.Unverifiable:
		return StatusExecuted
	case gate.Sealed:
		switch {
		case wasDenied:
			return StatusDenied
		case wasExpired:
			return StatusExpired
		case wasCancelled:
			return StatusCancelled
		case wasFailed:
			return StatusFailed
		}
		return StatusExecuted
	}
	return StatusProcessing // received, authenticated, evaluated, executed, witnessing
}

// Money is integer minor units plus a lowercase ISO 4217 currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Action is the action object (GET /v1/actions, and the Gate's responses since S13). Its sections
// mirror the deed's, so the Console reads one shape everywhere. S13 added livemode, status,
// decision, approval and next_action.
type Action struct {
	Object          string           `json:"object"` // "action"
	ID              string           `json:"id"`
	CreatedAt       Time             `json:"created_at"`
	Livemode        bool             `json:"livemode"`
	Agent           AgentRef         `json:"agent"`
	Tool            string           `json:"tool"`
	Connector       *string          `json:"connector"`
	Mode            string           `json:"mode"`
	State           string           `json:"state"`
	Status          string           `json:"status"`
	OnBehalfOf      *string          `json:"on_behalf_of"`
	Session         *string          `json:"session"`
	Decision        *ActionDecision  `json:"decision"`
	Policy          ActionPolicy     `json:"policy"`
	Approval        *ActionApproval  `json:"approval"`
	Execution       *ActionExecution `json:"execution"`
	Witness         *ActionWitness   `json:"witness"`
	Cost            *Money           `json:"cost"`
	RequestDigest   string           `json:"request_digest"`
	ResultDigest    *string          `json:"result_digest"`
	PayloadPrunedAt *Time            `json:"payload_pruned_at"`
	Deed            *string          `json:"deed"`
	NextAction      *NextAction      `json:"next_action"`
	// Undo (S23) is set on a single read and on an undo's own answers: what this action undoes, the
	// undo that put it back, and whether the Console offers Preview undo.
	Undo *ActionUndo `json:"undo,omitempty"`
	// Schedule (S40) is the window an approved action runs in; absent when none was asked for.
	Schedule *ActionSchedule `json:"schedule,omitempty"`
}

// ActionUndo is the action's undo object (S23). Eligible is cheap (L2 evidence from a bound
// transaction, gateway mode, sealed, not undone, within undo.max_age, the payload kept): it only
// decides whether the Console shows Preview undo. The full answer is always the preview.
type ActionUndo struct {
	Undoes   *string `json:"undoes"`
	UndoneBy *string `json:"undone_by"`
	Pending  *string `json:"pending,omitempty"` // an undo waiting for approval or running
	Eligible bool    `json:"eligible"`
}

// ActionEvent is one action_events row.
type ActionEvent struct {
	Object string          `json:"object"` // "action_event"
	ID     string          `json:"id"`
	Seq    int             `json:"seq"`
	From   *string         `json:"from"`
	To     string          `json:"to"`
	At     Time            `json:"at"`
	Actor  string          `json:"actor"`
	Data   json.RawMessage `json:"data"`
}

// DeedRef points at one deed about an action: its deed and any amendments.
type DeedRef struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Seq      int64  `json:"seq"`
	IssuedAt Time   `json:"issued_at"`
}

// ActionDetail is GET /v1/actions/{id}: the action plus its payloads, events and deeds.
type ActionDetail struct {
	Object          string             `json:"object"`
	ID              string             `json:"id"`
	CreatedAt       Time               `json:"created_at"`
	Livemode        bool               `json:"livemode"`
	Agent           AgentRef           `json:"agent"`
	Tool            string             `json:"tool"`
	Connector       *string            `json:"connector"`
	Mode            string             `json:"mode"`
	State           string             `json:"state"`
	Status          string             `json:"status"`
	OnBehalfOf      *string            `json:"on_behalf_of"`
	Session         *string            `json:"session"`
	Decision        *ActionDecision    `json:"decision"`
	Policy          ActionPolicy       `json:"policy"`
	Approval        *ActionApproval    `json:"approval"`
	Execution       *ActionExecution   `json:"execution"`
	Witness         *ActionWitness     `json:"witness"`
	Cost            *Money             `json:"cost"`
	RequestDigest   string             `json:"request_digest"`
	ResultDigest    *string            `json:"result_digest"`
	PayloadPrunedAt *Time              `json:"payload_pruned_at"`
	Deed            *string            `json:"deed"`
	NextAction      *NextAction        `json:"next_action"`
	Request         json.RawMessage    `json:"request"`
	Result          json.RawMessage    `json:"result"`
	Events          *List[ActionEvent] `json:"events"`
	Deeds           []DeedRef          `json:"deeds"`
	Undo            *ActionUndo        `json:"undo"`
	Schedule        *ActionSchedule    `json:"schedule,omitempty"`
}

const actionCols = `a.id, a.created_at, a.agent_id, g.name, a.tool, a.connector_id, a.mode, a.state, a.on_behalf_of,
  a.session_ref, COALESCE(a.policy_id, pv.policy_id), a.policy_version_id, a.decision, a.decision_rule_id, a.execution_status,
  COALESCE(a.started_at, a.executing_at), COALESCE(a.finished_at, a.executed_at, a.failed_at), a.latency_ms, a.error_code,
  a.witness_level, a.witness_verdict, a.witness_method,
  a.cost_amount, a.cost_currency, a.request_digest, a.result_digest, a.payload_pruned_at, a.deed_id,
  a.livemode, a.decision_code, a.decision_source, a.decision_reason, a.decision_message, a.policy_mode,
  a.shadow_decision, a.shadow_rule_id, a.shadow_version_id, a.approval_id, ap.status, ap.expires_at,
  a.executing_deadline, a.error_message,
  a.denied_at IS NOT NULL, a.expired_at IS NOT NULL, a.cancelled_at IS NOT NULL, a.failed_at IS NOT NULL,
  a.witness_detail_digest, a.witness_required,
  CASE WHEN ap.status = 'denied' AND ap.decided_by LIKE 'usr\_%' THEN ap.decision_reason END, a.decision_detail->>'anomaly',
  a.schedule`

const actionFrom = ` FROM deedglass.actions AS a
JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
LEFT JOIN deedglass.policy_versions AS pv ON pv.org_id = a.org_id AND pv.id = a.policy_version_id
LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id`

func scanAction(row pgx.Row, extra ...any) (Action, time.Time, error) {
	var (
		a                                      Action
		created                                time.Time
		started, finished, pruned              *time.Time
		latency                                *int64
		execStatus, errCode, errMsg            *string
		wLevel, wVerdict, wMethod              *string
		costAmount                             *int64
		costCurrency                           *string
		polID, pvID, decision, rID             *string
		dCode, dSource, dReason, dMsg, polMode *string
		sDecision, sRule, sVersion             *string
		apID, apStatus                         *string
		apExpires, deadline                    *time.Time
		wasDenied, wasExpired, wasCancelled    bool
		wasFailed                              bool
		wDigest, wRequired                     *string
		apReason, dAnomaly                     *string
		schedule                               []byte
	)
	dest := []any{&a.ID, &created, &a.Agent.ID, &a.Agent.Name, &a.Tool, &a.Connector, &a.Mode, &a.State, &a.OnBehalfOf,
		&a.Session, &polID, &pvID, &decision, &rID, &execStatus, &started, &finished, &latency, &errCode,
		&wLevel, &wVerdict, &wMethod, &costAmount, &costCurrency, &a.RequestDigest, &a.ResultDigest, &pruned, &a.Deed,
		&a.Livemode, &dCode, &dSource, &dReason, &dMsg, &polMode, &sDecision, &sRule, &sVersion, &apID, &apStatus, &apExpires,
		&deadline, &errMsg, &wasDenied, &wasExpired, &wasCancelled, &wasFailed, &wDigest, &wRequired, &apReason, &dAnomaly, &schedule}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return a, created, err
	}
	a.Object, a.CreatedAt = "action", Time(created)
	mode := "enforce"
	if polMode != nil {
		mode = *polMode
	}
	a.Policy = ActionPolicy{ID: polID, VersionID: pvID, Mode: mode, Decision: decision, RuleID: rID}
	a.Status = StatusOf(a.State, a.Mode, wasDenied, wasExpired, wasCancelled, wasFailed)
	if decision != nil {
		a.Decision = &ActionDecision{Outcome: *decision, Code: dCode, Source: dSource, RuleID: rID, Reason: dReason,
			Message: dMsg, Policy: polID, PolicyVersion: pvID, Anomaly: dAnomaly}
		if sDecision != nil {
			a.Decision.Shadow = &ShadowDecision{Outcome: *sDecision, RuleID: sRule, PolicyVersion: sVersion}
		}
	}
	if apID != nil && apStatus != nil && apExpires != nil {
		a.Approval = &ActionApproval{ID: *apID, Status: *apStatus, ExpiresAt: Time(*apExpires), DecisionReason: apReason}
	}
	if len(schedule) > 0 {
		var sc ActionSchedule
		if json.Unmarshal(schedule, &sc) == nil {
			a.Schedule = &sc
		}
	}
	a.NextAction = nextAction(&a, deadline)
	if started != nil {
		if latency == nil && finished != nil {
			ms := finished.Sub(*started).Milliseconds()
			latency = &ms
		}
		a.Execution = &ActionExecution{Status: execStatus, StartedAt: tptr(started), FinishedAt: tptr(finished), LatencyMS: latency,
			ErrorCode: errCode, ErrorMessage: errMsg}
	}
	if wLevel != nil && wVerdict != nil {
		a.Witness = &ActionWitness{Object: "witness", Level: *wLevel, Verdict: *wVerdict, Method: wMethod, RequiredLevel: wRequired,
			DetailDigest: wDigest}
	}
	if costAmount != nil && costCurrency != nil {
		a.Cost = &Money{Amount: *costAmount, Currency: *costCurrency}
	}
	a.PayloadPrunedAt = tptr(pruned)
	return a, created, nil
}

// PollWait is the longest wait GET /v1/actions/{id}?wait= accepts, in seconds.
const PollWait = 25

// nextAction is what the caller does next: wait for a person, or run the tool at the edge.
func nextAction(a *Action, deadline *time.Time) *NextAction {
	switch {
	case a.State == string(gate.PendingApproval) && a.Approval != nil && a.Approval.Status == "approved" && a.Schedule != nil:
		nb, na := a.Schedule.NotBefore, a.Schedule.NotAfter
		return &NextAction{Type: "scheduled", NotBefore: &nb, NotAfter: &na}
	case a.State == string(gate.PendingApproval) && a.Approval != nil:
		poll := fmt.Sprintf("/v1/actions/%s?wait=%d", a.ID, PollWait)
		exp := a.Approval.ExpiresAt
		id := a.Approval.ID
		return &NextAction{Type: "await_approval", Approval: &id, ExpiresAt: &exp, PollURL: &poll}
	case a.State == string(gate.Executing) && a.Mode == "proxy":
		report := "/v1/actions/" + a.ID + "/result"
		return &NextAction{Type: "execute_at_edge", ReportURL: &report, Deadline: tptr(deadline)}
	}
	return nil
}

// where builds the WHERE clause of an action query from fixed fragments; only values are bound.
func (f ActionFilter) where(org ids.OrgID) (string, []any) {
	conds := []string{"a.org_id = $1"}
	args := []any{org}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	if f.Agent != "" {
		add("a.agent_id = $?", f.Agent)
	}
	if f.Tool != "" {
		if strings.Contains(f.Tool, "*") {
			add(`a.tool LIKE $? ESCAPE '\'`, likePattern(f.Tool))
		} else {
			add("a.tool = $?", f.Tool)
		}
	}
	// One value is an equality, so (org_id, witness_verdict, created_at DESC, id DESC) is read in
	// order with no sort; several values are ANY.
	in := func(col string, vs []string) {
		switch len(vs) {
		case 0:
		case 1:
			add(col+" = $?", vs[0])
		default:
			add(col+" = ANY($?::text[])", vs)
		}
	}
	in("a.state", f.States)
	in("a.decision", f.Decisions)
	in("a.witness_verdict", f.Verdicts)
	in("a.witness_level", f.Levels)
	if f.OnBehalfOf != "" {
		add("a.on_behalf_of = $?", f.OnBehalfOf)
	}
	if f.CreatedGTE != nil {
		add("a.created_at >= $?", *f.CreatedGTE)
	}
	if f.CreatedLT != nil {
		add("a.created_at < $?", *f.CreatedLT)
	}
	if f.Q != "" {
		add(`(a.tool ILIKE $? ESCAPE '\' OR a.error_code ILIKE $? ESCAPE '\')`, containsPattern(f.Q))
	}
	if f.ShadowDiffers {
		// A fixed fragment with no value: the shadow policy decided, and not as the enforced one did.
		conds = append(conds, "(a.shadow_decision IS NOT NULL AND a.shadow_decision IS DISTINCT FROM a.decision)")
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// readTx runs fn in a read-only org transaction with a 5 s statement timeout.
func (l *Ledger) readTx(ctx context.Context, org ids.OrgID, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return l.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', $1, true)`, readTimeout); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
}

// ListActions returns one page of org's actions, newest first. Ordering is (created_at DESC,
// id DESC), so rows inserted while a client pages appear only on a fresh first page.
func (l *Ledger) ListActions(ctx context.Context, org ids.OrgID, f ActionFilter, p Page) (*List[Action], error) {
	q := f.hash()
	c, err := decodeCursor(p.Cursor, q)
	if err != nil {
		return nil, err
	}
	limit := clampLimit(p.Limit)
	where, args := f.where(org)
	if c != nil {
		k, err := time.Parse(time.RFC3339Nano, c.K)
		if err != nil || ids.Check(ids.Action, c.ID) != nil {
			return nil, errBadCursor()
		}
		args = append(args, k, c.ID)
		where += fmt.Sprintf(" AND (a.created_at, a.id) < ($%d::timestamptz, $%d::text)", len(args)-1, len(args))
	}
	args = append(args, limit+1)
	sql := `SELECT ` + actionCols + actionFrom + where + fmt.Sprintf(` ORDER BY a.created_at DESC, a.id DESC LIMIT $%d`, len(args))
	var out []Action
	var lastAt time.Time
	err = l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, at, err := scanAction(rows)
			if err != nil {
				return err
			}
			if len(out) < limit {
				out, lastAt = append(out, a), at
			} else {
				out = append(out, a) // the extra row: has_more
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = cursor{ID: out[limit-1].ID, K: lastAt.UTC().Format(time.RFC3339Nano), Q: q, V: 1}.encode()
	}
	return newList(out, next), nil
}

// MaxActionsByID is the most ids ActionsByID reads at once.
const MaxActionsByID = 200

// ActionsByID returns org's actions with the given ids, in the list shape GET /v1/actions uses,
// read in one read-only org transaction (S24: the live stream hydrates a batch of events with it).
// Unknown ids, and ids of another org, are simply absent.
func (l *Ledger) ActionsByID(ctx context.Context, org ids.OrgID, actionIDs []string) (map[string]Action, error) {
	if len(actionIDs) > MaxActionsByID {
		return nil, fmt.Errorf("ledger: ActionsByID takes at most %d ids, got %d", MaxActionsByID, len(actionIDs))
	}
	want := make([]string, 0, len(actionIDs))
	for _, id := range actionIDs {
		if ids.Check(ids.Action, id) == nil {
			want = append(want, id)
		}
	}
	out := make(map[string]Action, len(want))
	if len(want) == 0 {
		return out, nil
	}
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+actionCols+actionFrom+` WHERE a.org_id = $1 AND a.id = ANY($2::text[])`, org, want)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, _, err := scanAction(rows)
			if err != nil {
				return err
			}
			out[a.ID] = a
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListSQL is the first-page SQL ListActions runs for f (bench and EXPLAIN only).
func ListSQL(org ids.OrgID, f ActionFilter, limit int) (string, []any) {
	where, args := f.where(org)
	args = append(args, limit+1)
	return `SELECT ` + actionCols + actionFrom + where + fmt.Sprintf(` ORDER BY a.created_at DESC, a.id DESC LIMIT $%d`, len(args)), args
}

// CountActions counts the actions that match f (the CLI's "5 of 1,204").
func (l *Ledger) CountActions(ctx context.Context, org ids.OrgID, f ActionFilter) (int64, error) {
	where, args := f.where(org)
	var n int64
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.actions AS a`+where, args...).Scan(&n)
	})
	return n, err
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

// GetAction returns one action with its payloads (null after pruning), events and deeds.
// ErrNotFound when the id doesn't exist in this org, including an id from another org.
func (l *Ledger) GetAction(ctx context.Context, org ids.OrgID, id string) (*ActionDetail, error) {
	if ids.Check(ids.Action, id) != nil {
		return nil, fmt.Errorf("%w: action %s", ErrNotFound, id)
	}
	var d *ActionDetail
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var req, res, detail []byte
		a, _, err := scanAction(tx.QueryRow(ctx, `SELECT `+actionCols+`, a.request, a.result, a.witness_detail`+actionFrom+
			` WHERE a.org_id = $1 AND a.id = $2`, org, id), &req, &res, &detail)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: action %s", ErrNotFound, id)
		}
		if err != nil {
			return err
		}
		d = detailOf(a, req, res, detail)
		rows, err := tx.Query(ctx, `SELECT id, seq, from_state, to_state, at, actor, data FROM deedglass.action_events
		    WHERE org_id = $1 AND action_id = $2 ORDER BY seq LIMIT $3`, org, id, MaxEventsShown+1)
		if err != nil {
			return err
		}
		evs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ActionEvent, error) {
			var e ActionEvent
			var at time.Time
			var data []byte
			err := r.Scan(&e.ID, &e.Seq, &e.From, &e.To, &at, &e.Actor, &data)
			e.Object, e.At, e.Data = "action_event", Time(at), json.RawMessage(data)
			return e, err
		})
		if err != nil {
			return err
		}
		more := len(evs) > MaxEventsShown
		if more {
			evs = evs[:MaxEventsShown]
		}
		d.Events = newList(evs, "")
		d.Events.HasMore = more
		rows, err = tx.Query(ctx, `SELECT id, kind, seq, issued_at, body -> 'witness' ->> 'level', body -> 'witness' ->> 'verdict'
		    FROM deedglass.deeds WHERE org_id = $1 AND action_id = $2 ORDER BY seq`, org, id)
		if err != nil {
			return err
		}
		var hist []WitnessHistory
		d.Deeds, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeedRef, error) {
			var x DeedRef
			var at time.Time
			var level, verdict *string
			err := r.Scan(&x.ID, &x.Kind, &x.Seq, &at, &level, &verdict)
			x.IssuedAt = Time(at)
			if err == nil && level != nil && verdict != nil {
				hist = append(hist, WitnessHistory{Deed: x.ID, Kind: x.Kind, Level: *level, Verdict: *verdict, At: deed.FormatTime(at)})
			}
			return x, err
		})
		if d.Deeds == nil {
			d.Deeds = []DeedRef{}
		}
		if err == nil && d.Witness != nil && len(hist) > 0 {
			if len(hist) > 1 {
				hist[0].Reason = witnessedReason(evs)
			}
			d.Witness.History = hist
		}
		if err != nil {
			return err
		}
		d.Undo, err = UndoOf(ctx, tx, org, id, l.undoMaxAge, l.now())
		return err
	})
	return d, err
}

// UndoOf reads an action's undo object (S23) inside tx.
func UndoOf(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string, maxAge time.Duration, now time.Time) (*ActionUndo, error) {
	u := &ActionUndo{}
	var (
		state, mode                string
		level, execStatus          *string
		sealedAt, pruned           *time.Time
		liveID, liveState, liveExe *string
		bound                      bool
	)
	err := tx.QueryRow(ctx, `SELECT a.undoes_action_id, a.state, a.mode, a.witness_level, a.execution_status, a.sealed_at, a.payload_pruned_at,
	    u.id, u.state, u.execution_status,
	    EXISTS (SELECT 1 FROM deedglass.decoded_txs AS f WHERE f.org_id = a.org_id AND f.action_id = a.id AND f.class = 'bound')
	  FROM deedglass.actions AS a
	  LEFT JOIN LATERAL (SELECT x.id, x.state, x.execution_status FROM deedglass.actions AS x
	      WHERE x.org_id = a.org_id AND x.undoes_action_id = a.id AND x.undo_released_at IS NULL ORDER BY x.created_at LIMIT 1) AS u ON true
	  WHERE a.org_id = $1 AND a.id = $2`, org, id).
		Scan(&u.Undoes, &state, &mode, &level, &execStatus, &sealedAt, &pruned, &liveID, &liveState, &liveExe, &bound)
	if err != nil {
		return nil, err
	}
	if liveID != nil {
		if *liveState == string(gate.Sealed) && liveExe != nil && *liveExe != "failed" {
			u.UndoneBy = liveID
		} else {
			u.Pending = liveID
		}
	}
	u.Eligible = state == string(gate.Sealed) && mode == "gateway" && level != nil && *level == "L2" && bound && pruned == nil &&
		liveID == nil && execStatus != nil && *execStatus != "failed" && sealedAt != nil && (maxAge <= 0 || now.Sub(*sealedAt) <= maxAge)
	return u, nil
}

// witnessedReason is the reason the action's own verdict recorded (its first violation or gap):
// the action.witnessed transition's event data.
func witnessedReason(evs []ActionEvent) *string {
	for _, e := range evs {
		switch e.To {
		case string(gate.Verified), string(gate.Mismatch), string(gate.Unverifiable):
			var x struct {
				Reason *string `json:"reason"`
			}
			if json.Unmarshal(e.Data, &x) == nil && x.Reason != nil {
				return x.Reason
			}
		}
	}
	return nil
}

// ReadAction reads one action object inside tx (an org transaction for org), with its stored
// result as execution.result and its witness reasons: the Gate's own responses (S13). ErrNotFound
// when the id isn't an action of this org.
func ReadAction(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (*Action, error) {
	var res, detail []byte
	a, _, err := scanAction(tx.QueryRow(ctx, `SELECT `+actionCols+`, a.result, a.witness_detail`+actionFrom+
		` WHERE a.org_id = $1 AND a.id = $2`, org, id), &res, &detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: action %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	if a.Execution != nil && res != nil {
		a.Execution.Result = res
	}
	if a.Witness != nil {
		a.Witness.fill(detail, false)
	}
	return &a, nil
}

// ReadActionView is ReadAction in its own read transaction.
func (l *Ledger) ReadActionView(ctx context.Context, org ids.OrgID, id string) (*Action, error) {
	var a *Action
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = ReadAction(ctx, tx, org, id)
		return err
	})
	return a, err
}

func detailOf(a Action, req, res, detail []byte) *ActionDetail {
	d := &ActionDetail{Object: a.Object, ID: a.ID, CreatedAt: a.CreatedAt, Livemode: a.Livemode, Agent: a.Agent, Tool: a.Tool,
		Connector: a.Connector, Mode: a.Mode, State: a.State, Status: a.Status, OnBehalfOf: a.OnBehalfOf, Session: a.Session,
		Decision: a.Decision, Policy: a.Policy, Approval: a.Approval, Execution: a.Execution,
		Cost: a.Cost, RequestDigest: a.RequestDigest, ResultDigest: a.ResultDigest, PayloadPrunedAt: a.PayloadPrunedAt,
		Deed: a.Deed, NextAction: a.NextAction, Request: rawOrNull(req), Result: rawOrNull(res), Schedule: a.Schedule}
	if w := a.Witness; w != nil {
		c := *w
		c.fill(detail, true)
		d.Witness = &c
	}
	return d
}

// WitnessReasons reads the reason codes in a stored witness_detail (v1, or S13's {"reasons": […]}),
// in order. It returns an empty list (never nil) when there are none, or the detail can't be read.
func WitnessReasons(detail []byte) []string {
	out := []string{}
	if d, ok := witness.ParseDetail(detail); ok {
		for _, r := range d.Reasons {
			out = append(out, string(r.Code))
		}
	}
	return out
}

func rawOrNull(b []byte) json.RawMessage {
	if b == nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// ListDeeds returns one page of deed envelopes (the stored JCS bytes), by seq.
func (l *Ledger) ListDeeds(ctx context.Context, org ids.OrgID, f DeedFilter, p Page) (*List[json.RawMessage], error) {
	q := f.hash()
	c, err := decodeCursor(p.Cursor, q)
	if err != nil {
		return nil, err
	}
	limit := clampLimit(p.Limit)
	conds := []string{"org_id = $1"}
	args := []any{org}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	if f.FromSeq > 0 {
		add("seq >= $?", f.FromSeq)
	}
	if f.ToSeq > 0 {
		add("seq <= $?", f.ToSeq)
	}
	if len(f.Kinds) > 0 {
		add("kind = ANY($?::text[])", f.Kinds)
	}
	if f.Action != "" {
		add("action_id = $?", f.Action)
	}
	if f.OwnAgent != "" {
		conds = append(conds, "kind IN ('action', 'amendment')")
		add("body->'agent'->>'id' = $?", f.OwnAgent)
	}
	if c != nil {
		after, err := strconv.ParseInt(c.K, 10, 64)
		if err != nil || after < 1 {
			return nil, errBadCursor()
		}
		if f.Desc {
			add("seq < $?", after)
		} else {
			add("seq > $?", after)
		}
	}
	order := "ASC"
	if f.Desc {
		order = "DESC"
	}
	args = append(args, limit+1)
	sql := `SELECT seq, id, envelope FROM deedglass.deeds WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY seq ` + order + ` LIMIT $` + strconv.Itoa(len(args))
	var out []json.RawMessage
	var lastSeq int64
	var lastID string
	err = l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var seq int64
			var id string
			var env []byte
			if err := rows.Scan(&seq, &id, &env); err != nil {
				return err
			}
			if len(out) < limit {
				lastSeq, lastID = seq, id
			}
			out = append(out, json.RawMessage(env))
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = cursor{ID: lastID, K: strconv.FormatInt(lastSeq, 10), Q: q, V: 1}.encode()
	}
	return newList(out, next), nil
}

// StoredDeed is one deed as stored: the exact JCS(envelope) bytes, served verbatim.
type StoredDeed struct {
	Agent    string // body.agent.id, or "" (admin deeds about no agent)
	ID       string
	Seq      int64
	Kind     string
	BodyHash string
	IssuedAt time.Time
	Envelope []byte
}

// GetDeed returns one deed by id (deed_…) or by seq ("812"). ErrNotFound otherwise.
func (l *Ledger) GetDeed(ctx context.Context, org ids.OrgID, ref string) (*StoredDeed, error) {
	col, val := "id", any(ref)
	if n, err := strconv.ParseInt(ref, 10, 64); err == nil && n >= 1 {
		col, val = "seq", n
	} else if ids.Check(ids.Deed, ref) != nil {
		return nil, fmt.Errorf("%w: deed %s", ErrNotFound, ref)
	}
	var d StoredDeed
	err := l.readTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id, seq, kind, body_hash, issued_at, envelope, COALESCE(body->'agent'->>'id', '')
		    FROM deedglass.deeds WHERE org_id = $1 AND `+col+` = $2`, org, val).
			Scan(&d.ID, &d.Seq, &d.Kind, &d.BodyHash, &d.IssuedAt, &d.Envelope, &d.Agent)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: deed %s", ErrNotFound, ref)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

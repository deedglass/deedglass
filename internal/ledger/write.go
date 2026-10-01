package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// EventData is what an action_events row records beyond the edge: short codes and digests, at most
// 2 KiB, never payloads. The struct is closed on purpose: a new field is a code review.
type EventData struct {
	Reason       *string `json:"reason,omitempty"`  // short code: "policy", "timeout", "crash_recovery"
	RuleID       *string `json:"rule_id,omitempty"` //
	ErrorCode    *string `json:"error_code,omitempty"`
	Level        *string `json:"level,omitempty"`
	Verdict      *string `json:"verdict,omitempty"`
	ApprovalID   *string `json:"approval_id,omitempty"`
	DetailDigest *string `json:"detail_digest,omitempty"`

	// Actor is who caused the event: "system" (the default), agt_… or usr_…. RequestID is the API
	// request, when there is one. Both are columns, not data.
	Actor     string `json:"-"`
	RequestID string `json:"-"`
}

func (d EventData) marshal() ([]byte, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEventData {
		return nil, fmt.Errorf("%w: event data is %d bytes; the limit is %d", ErrPayload, len(b), MaxEventData)
	}
	return b, nil
}

func (d EventData) actor() string {
	if d.Actor == "" {
		return "system"
	}
	return d.Actor
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// stateColumn is the timestamp column each state sets. It is a fixed table, never input: the only
// identifier ever placed into SQL text in this package.
var stateColumn = map[gate.State]string{
	gate.Authenticated: "authenticated_at", gate.Evaluated: "evaluated_at", gate.Denied: "denied_at",
	gate.PendingApproval: "pending_approval_at", gate.Executing: "executing_at", gate.Executed: "executed_at",
	gate.Failed: "failed_at", gate.Witnessing: "witnessing_at", gate.Verified: "verified_at",
	gate.Mismatch: "mismatch_at", gate.Unverifiable: "unverifiable_at", gate.Sealed: "sealed_at",
	gate.Expired: "expired_at", gate.Cancelled: "cancelled_at",
}

// transitionSQL is built once per state from stateColumn.
var transitionSQL = func() map[gate.State]string {
	m := make(map[gate.State]string, len(stateColumn))
	for s, col := range stateColumn {
		set := ""
		if s == gate.Sealed {
			set = ", deed_id = $8" // the deed SealAction just sealed, in the same statement
		}
		m[s] = `WITH u AS (
  UPDATE deedglass.actions SET state = $3, ` + col + ` = pg_catalog.clock_timestamp()` + set + `
  WHERE id = $1 AND state = $2
  RETURNING id, org_id, ` + col + ` AS at)
INSERT INTO deedglass.action_events (id, org_id, action_id, seq, from_state, to_state, actor, request_id, data, at)
SELECT $4, u.org_id, u.id,
  (SELECT COALESCE(pg_catalog.max(e.seq), 0) + 1 FROM deedglass.action_events AS e WHERE e.action_id = u.id),
  $2, $3, $5, $6, $7::jsonb, u.at
FROM u
RETURNING org_id, at`
	}
	return m
}()

// NewAction is what CreateAction needs. S13's Gate fills it from POST /v1/actions.
type NewAction struct {
	ID             ids.ActionID // zero: a new id
	Agent          ids.AgentID
	Mode           string // gateway | proxy
	Tool           string
	Connector      *string
	OnBehalfOf     *string
	Session        *string
	IdempotencyKey *string
	Request        json.RawMessage // the tool arguments (a payload: retention prunes it)
	RequestDigest  string          // DigestJSON(Request) when empty
	IntentHash     *string
	IntentSigned   bool
	Actor          string // the event actor; "system" by default
	RequestID      string
}

// CreateAction inserts an action in received, appends its first event and publishes
// action.created, inside the caller's org transaction.
func (l *Ledger) CreateAction(ctx context.Context, tx pgx.Tx, org ids.OrgID, a NewAction) (ids.ActionID, error) {
	if org.IsZero() {
		return ids.ActionID{}, store.ErrNoOrg
	}
	id := a.ID
	if id.IsZero() {
		id = ids.New[ids.ActionKind]()
	}
	digest := a.RequestDigest
	if digest == "" {
		raw := a.Request
		if raw == nil {
			raw = json.RawMessage("null")
		}
		var err error
		if digest, err = deed.DigestJSON(raw); err != nil {
			return ids.ActionID{}, err
		}
	}
	var req *string
	if a.Request != nil {
		s := string(a.Request)
		req = &s
	}
	var created time.Time
	err := tx.QueryRow(ctx, `INSERT INTO deedglass.actions (id, org_id, agent_id, on_behalf_of, session_ref, idempotency_key,
	    mode, tool, connector_id, request, request_digest, intent_hash, intent_signed)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13) RETURNING created_at`,
		id, org, a.Agent, a.OnBehalfOf, a.Session, a.IdempotencyKey, a.Mode, a.Tool, a.Connector, req, digest,
		a.IntentHash, a.IntentSigned).Scan(&created)
	if err != nil {
		return ids.ActionID{}, err
	}
	evt := ids.New[ids.ActionEventKind]()
	actor := EventData{Actor: a.Actor}.actor()
	if _, err := tx.Exec(ctx, `INSERT INTO deedglass.action_events (id, org_id, action_id, seq, from_state, to_state, actor, request_id, data, at)
	    VALUES ($1, $2, $3, 1, NULL, 'received', $4, $5, '{}', $6)`, evt, org, id, actor, nullable(a.RequestID), created); err != nil {
		return ids.ActionID{}, err
	}
	act := id.String()
	return id, l.Publish(ctx, tx, Event{Event: evt.String(), Type: EventActionCreated, Org: org.String(), ID: act,
		Action: &act, State: strp(string(gate.Received)), At: deed.FormatTime(created)})
}

// Transition moves an action from → to: it updates the state and the state's timestamp column
// (UPDATE … WHERE id = $1 AND state = $from), appends an action_events row and publishes the
// mapped event, all in tx. It returns ErrIllegalTransition when gate.CanTransition is false and
// ErrStateConflict when no row matched. Sealing goes through SealAction, never Transition.
func (l *Ledger) Transition(ctx context.Context, tx pgx.Tx, actionID string, from, to gate.State, d EventData) error {
	if to == gate.Sealed {
		return fmt.Errorf("%w: an action is sealed by SealAction, which seals its deed", ErrIllegalTransition)
	}
	_, err := l.transition(ctx, tx, actionID, from, to, d, nil)
	return err
}

// sealedDeed is the deed a transition into sealed announces.
type sealedDeed struct {
	org string
	id  string
	seq int64
	at  string
}

func (l *Ledger) transition(ctx context.Context, tx pgx.Tx, actionID string, from, to gate.State, d EventData, sd *sealedDeed) (time.Time, error) {
	if !gate.CanTransition(from, to) {
		return time.Time{}, fmt.Errorf("%w: %s → %s", ErrIllegalTransition, from, to)
	}
	data, err := d.marshal()
	if err != nil {
		return time.Time{}, err
	}
	evt := ids.New[ids.ActionEventKind]()
	var (
		org string
		at  time.Time
	)
	args := []any{actionID, string(from), string(to), evt, d.actor(), nullable(d.RequestID), string(data)}
	if to == gate.Sealed {
		if sd == nil {
			return time.Time{}, fmt.Errorf("%w: an action is sealed by SealAction, which seals its deed", ErrIllegalTransition)
		}
		args = append(args, sd.id)
	}
	if sd != nil {
		// Sealing: the deed.sealed event is known in full before the statement runs, so the
		// transition and its NOTIFY go in one round trip.
		ev := Event{Event: evt.String(), Type: EventDeedSealed, Org: sd.org, ID: sd.id, Seq: &sd.seq, Action: &actionID,
			State: strp(string(gate.Sealed)), Deed: &sd.id, At: sd.at}
		payload, perr := marshalEvent(&ev, l.now())
		if perr != nil {
			return time.Time{}, perr
		}
		b := &pgx.Batch{}
		b.Queue(transitionSQL[to], args...)
		b.Queue(notifySQL, Channel, string(payload))
		br := tx.SendBatch(ctx, b)
		err = br.QueryRow().Scan(&org, &at)
		if err == nil {
			_, err = br.Exec()
		}
		if cerr := br.Close(); err == nil {
			err = cerr
		}
	} else {
		err = tx.QueryRow(ctx, transitionSQL[to], args...).Scan(&org, &at)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, fmt.Errorf("%w: %s is not %s", ErrStateConflict, actionID, from)
	}
	if store.SQLState(err) == store.CodeIllegalTransition {
		return time.Time{}, fmt.Errorf("%w: %s → %s (refused by the database)", ErrIllegalTransition, from, to)
	}
	if err != nil {
		return time.Time{}, err
	}
	typ := eventFor(from, to)
	if typ == "" || sd != nil {
		return at, nil // nothing to announce, or announced in the batch above
	}
	return at, l.Publish(ctx, tx, Event{Event: evt.String(), Type: typ, Org: org, ID: actionID, Action: &actionID,
		State: strp(string(to)), At: deed.FormatTime(at)})
}

// AppendEvent records a note on an action without changing its state (from_state = to_state =
// the current state): a retry, a reminder, a late verdict. The action row is locked so event seqs
// never collide.
func (l *Ledger) AppendEvent(ctx context.Context, tx pgx.Tx, actionID string, d EventData) error {
	data, err := d.marshal()
	if err != nil {
		return err
	}
	var org, state string
	err = tx.QueryRow(ctx, `SELECT org_id, state FROM deedglass.actions WHERE id = $1 FOR UPDATE`, actionID).Scan(&org, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: action %s", ErrNotFound, actionID)
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO deedglass.action_events (id, org_id, action_id, seq, from_state, to_state, actor, request_id, data)
	    SELECT $1::text, $2::text, $3::text, COALESCE(pg_catalog.max(e.seq), 0) + 1, $4::text, $4::text, $5::text, $6::text, $7::jsonb
	    FROM deedglass.action_events AS e WHERE e.action_id = $3::text`,
		ids.New[ids.ActionEventKind](), org, actionID, state, d.actor(), nullable(d.RequestID), string(data))
	return err
}

// snapshotSQL reads everything ForAction needs, locking the action row.
const snapshotSQL = `SELECT a.org_id, a.agent_id, g.name, a.on_behalf_of, a.session_ref, a.tool, a.connector_id, a.mode,
  a.request_digest, a.intent_hash, a.intent_signed, a.state, a.decision, a.decision_rule_id, a.policy_version_id,
  pv.policy_id, pv.version_hash, a.approval_id, ap.status, ap.approved_by, ap.decided_by, ap.decided_at, ap.deed_id,
  a.executing_at, a.executed_at, a.failed_at, a.execution_status, a.result_digest, a.error_code,
  a.witness_level, a.witness_verdict, a.witness_method, a.witness_detail_digest, a.cost_amount, a.cost_currency
FROM deedglass.actions AS a
JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
LEFT JOIN deedglass.policy_versions AS pv ON pv.org_id = a.org_id AND pv.id = a.policy_version_id
LEFT JOIN deedglass.approvals AS ap ON ap.org_id = a.org_id AND ap.id = a.approval_id
WHERE a.id = $1
FOR UPDATE OF a`

// snapshot reads an action as deed.ForAction needs it, and its state.
func snapshot(ctx context.Context, tx pgx.Tx, actionID string) (deed.ActionSnapshot, gate.State, error) {
	var (
		s                                            deed.ActionSnapshot
		state                                        string
		decision, pvID, polID, pvHash, apID, apState *string
		apApproved                                   []string
		apDecidedBy, apDeed                          *string
		apDecidedAt                                  *time.Time
		execAt, doneAt, failAt                       *time.Time
		execStatus, resultDigest, errorCode          *string
		wLevel, wVerdict, wMethod, wDigest           *string
		costAmount                                   *int64
		costCurrency                                 *string
	)
	err := tx.QueryRow(ctx, snapshotSQL, actionID).Scan(&s.Org, &s.Agent.ID, &s.Agent.Name, &s.OnBehalfOf, &s.Session,
		&s.Tool, &s.Connector, &s.Mode, &s.RequestDigest, &s.IntentHash, &s.IntentSigned, &state, &decision,
		&s.Policy.RuleID, &pvID, &polID, &pvHash, &apID, &apState, &apApproved, &apDecidedBy, &apDecidedAt, &apDeed,
		&execAt, &doneAt, &failAt, &execStatus, &resultDigest, &errorCode,
		&wLevel, &wVerdict, &wMethod, &wDigest, &costAmount, &costCurrency)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, "", fmt.Errorf("%w: action %s", ErrNotFound, actionID)
	}
	if err != nil {
		return s, "", err
	}
	s.ActionID = actionID
	st := gate.State(state)
	if !st.Sealable() {
		return s, st, fmt.Errorf("%w: %s is %s", ErrNotSealable, actionID, st)
	}
	if decision == nil {
		return s, st, fmt.Errorf("%w: %s has no policy decision", ErrNotSealable, actionID)
	}
	s.Policy.ID, s.Policy.VersionID, s.Policy.VersionHash = polID, pvID, pvHash
	s.Policy.Mode, s.Policy.Decision = "enforce", *decision
	if apID != nil {
		ap := &deed.ApprovalSnapshot{ID: *apID, DecidedAt: apDecidedAt, Deed: apDeed}
		if apState != nil {
			ap.Outcome = *apState
		}
		switch {
		case len(apApproved) > 0:
			ap.DecidedBy = apApproved
		case apDecidedBy != nil && ids.Check(ids.User, *apDecidedBy) == nil:
			ap.DecidedBy = []string{*apDecidedBy}
		}
		s.Approval = ap
	}
	if execAt != nil {
		end := doneAt
		if st == gate.Failed {
			end = failAt
		}
		if end == nil || execStatus == nil {
			return s, st, fmt.Errorf("%w: %s ran but its execution status or finish time is not set", ErrNotSealable, actionID)
		}
		s.Execution = &deed.ExecutionSnapshot{Status: *execStatus, StartedAt: *execAt, FinishedAt: *end,
			ResultDigest: resultDigest, ErrorCode: errorCode}
	}
	if wLevel != nil && wVerdict != nil {
		w := &deed.WitnessSection{Level: *wLevel, Verdict: *wVerdict, DetailDigest: wDigest}
		if wMethod != nil {
			w.Method = *wMethod
		}
		s.Witness = w
	}
	if costAmount != nil && costCurrency != nil {
		s.Cost = &deed.Cost{Amount: *costAmount, Currency: *costCurrency}
	}
	return s, st, nil
}

// SealAction seals an action in a final state: it reads the action (with its agent's name and
// approval), builds deed.ForAction, seals it into the org's chain, moves the action to sealed with
// actions.deed_id set (one statement) and publishes deed.sealed. One call, one transaction, one deed. On
// deed.ErrChainConflict the caller retries once in a new transaction.
func (l *Ledger) SealAction(ctx context.Context, tx pgx.Tx, actionID string) (*deed.Envelope, error) {
	if l.sealer == nil {
		return nil, ErrNoSigner
	}
	snap, st, err := snapshot(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	d, err := deed.ForAction(snap)
	if err != nil {
		return nil, err
	}
	env, err := l.sealer.Seal(ctx, store.DeedTx(tx), d)
	if err != nil {
		return nil, err
	}
	sd := &sealedDeed{org: env.Body.Org, id: env.ID, seq: env.Body.Seq, at: env.Body.IssuedAt}
	if _, err := l.transition(ctx, tx, actionID, st, gate.Sealed, EventData{}, sd); err != nil {
		return nil, err
	}
	return env, nil
}

// SealAmendment records a later witness verdict about a sealed action: it loads the action's deed,
// builds deed.ForAmendment, seals it, updates the action's witness columns, notes it on the
// action's events and publishes deed.sealed. The original deed is never touched. detail (the
// stored diff) may be nil; its digest goes into the deed, the detail itself into witness_detail.
func (l *Ledger) SealAmendment(ctx context.Context, tx pgx.Tx, actionID string, w deed.WitnessSection, detail any) (*deed.Envelope, error) {
	if l.sealer == nil {
		return nil, ErrNoSigner
	}
	var (
		state  string
		deedID *string
		raw    []byte
	)
	err := tx.QueryRow(ctx, `SELECT a.state, a.deed_id, d.envelope FROM deedglass.actions AS a
	    LEFT JOIN deedglass.deeds AS d ON d.org_id = a.org_id AND d.id = a.deed_id
	    WHERE a.id = $1 FOR UPDATE OF a`, actionID).Scan(&state, &deedID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: action %s", ErrNotFound, actionID)
	}
	if err != nil {
		return nil, err
	}
	if gate.State(state) != gate.Sealed || deedID == nil {
		return nil, fmt.Errorf("%w: only a sealed action can be amended (%s is %s)", ErrStateConflict, actionID, state)
	}
	orig, err := deed.ParseEnvelope(raw)
	if err != nil {
		return nil, fmt.Errorf("ledger: the stored deed of %s does not parse: %w", actionID, err)
	}
	var detailJSON *string
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return nil, err
		}
		dg, err := deed.DigestJSON(b)
		if err != nil {
			return nil, err
		}
		w.DetailDigest, detailJSON = &dg, strp(string(b))
	}
	d, err := deed.ForAmendment(&orig.Body, w)
	if err != nil {
		return nil, err
	}
	env, err := l.sealer.Seal(ctx, store.DeedTx(tx), d)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE deedglass.actions SET witness_level = $2, witness_verdict = $3, witness_method = $4,
	    witness_detail = $5::jsonb, witness_detail_digest = $6 WHERE id = $1`,
		actionID, w.Level, w.Verdict, w.Method, detailJSON, w.DetailDigest); err != nil {
		return nil, err
	}
	if err := l.AppendEvent(ctx, tx, actionID, EventData{Reason: strp("amended"), Level: &w.Level, Verdict: &w.Verdict, DetailDigest: w.DetailDigest}); err != nil {
		return nil, err
	}
	seq := env.Body.Seq
	return env, l.Publish(ctx, tx, Event{Type: EventDeedSealed, Org: env.Body.Org, ID: env.ID, Seq: &seq, Action: &actionID,
		State: strp(string(gate.Sealed)), Deed: &env.ID, At: env.Body.IssuedAt})
}

// AdminEvent is one administrative change: who did what to which object, and why.
type AdminEvent struct {
	Action    string         // an AdminActions name: ledger.pruned, agent.stopped, …
	Actor     deed.Ref       // {user, usr_…}, {agent, agt_…}, {system, deedglass} or {system, operator}
	Target    *deed.Ref      //
	Agent     *deed.AgentRef // set when the target is an agent (it appears in body.agent)
	Reason    string         // optional, ≤ 280 characters, shown to auditors
	Data      any            // admin_events.data: a JSON object ≤ 16 KiB; only its digest enters the deed
	RequestID string         // req_…, when an API request caused it
}

// AppendAdminEvent writes an admin_events row without sealing a deed, for security events that
// don't change who can act or what they may do: signing in and signing out (S07). The action must
// be one of AdminEventActions; anything that changes access goes through RecordAdmin instead.
func (l *Ledger) AppendAdminEvent(ctx context.Context, tx pgx.Tx, e AdminEvent) (ids.AdminEventID, error) {
	if !reAdminAction.MatchString(e.Action) || !slices.Contains(AdminEventActions, e.Action) {
		return ids.AdminEventID{}, fmt.Errorf("ledger: %q is not an unsealed admin event (add it to AdminEventActions)", e.Action)
	}
	if e.Actor.Type != "user" || e.Actor.ID == "" {
		return ids.AdminEventID{}, errors.New("ledger: an unsealed admin event names the person it is about")
	}
	if utf8.RuneCountInString(e.Reason) > deed.MaxReason {
		return ids.AdminEventID{}, fmt.Errorf("%w: the reason is longer than %d characters", ErrPayload, deed.MaxReason)
	}
	data := []byte("{}")
	if e.Data != nil {
		var err error
		if data, err = json.Marshal(e.Data); err != nil {
			return ids.AdminEventID{}, err
		}
	}
	if len(data) > MaxAdminData || len(data) < 2 || data[0] != '{' {
		return ids.AdminEventID{}, fmt.Errorf("%w: admin event data must be a JSON object of at most %d bytes", ErrPayload, MaxAdminData)
	}
	var org string
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.current_setting('deedglass.org_id', true)`).Scan(&org); err != nil {
		return ids.AdminEventID{}, err
	}
	if org == "" {
		return ids.AdminEventID{}, store.ErrNoOrg
	}
	var tType, tID *string
	if e.Target != nil {
		tType, tID = &e.Target.Type, &e.Target.ID
	}
	id := ids.New[ids.AdminEventKind]()
	_, err := tx.Exec(ctx, `INSERT INTO deedglass.admin_events (id, org_id, actor_type, actor_id, action, target_type, target_id,
	    reason, data, request_id) VALUES ($1, $2, 'user', $3, $4, $5, $6, $7, $8::jsonb, $9)`,
		id, org, e.Actor.ID, e.Action, tType, tID, nullable(e.Reason), string(data), nullable(e.RequestID))
	return id, err
}

// RecordAdmin seals an admin deed (deed.ForAdmin) for e, inserts the admin_events row that names
// it, and publishes deed.sealed, all in the caller's org transaction.
func (l *Ledger) RecordAdmin(ctx context.Context, tx pgx.Tx, e AdminEvent) (*deed.Envelope, error) {
	if l.sealer == nil {
		return nil, ErrNoSigner
	}
	if !reAdminAction.MatchString(e.Action) || !knownAdminAction(e.Action) {
		return nil, fmt.Errorf("ledger: unknown admin action %q (add it to admin_actions.go)", e.Action)
	}
	if utf8.RuneCountInString(e.Reason) > deed.MaxReason {
		return nil, fmt.Errorf("%w: the reason is longer than %d characters", ErrPayload, deed.MaxReason)
	}
	actorType, actorID := e.Actor.Type, nullable(e.Actor.ID)
	switch actorType {
	case "system":
		actorID = nil
	case "user", "agent", "api_key":
		if actorID == nil {
			return nil, fmt.Errorf("ledger: a %s actor needs an id", actorType)
		}
	default:
		return nil, fmt.Errorf("ledger: admin actor type %q is not user, agent, api_key or system", actorType)
	}
	data := []byte("{}")
	if e.Data != nil {
		var err error
		if data, err = json.Marshal(e.Data); err != nil {
			return nil, err
		}
	}
	if len(data) > MaxAdminData || len(data) < 2 || data[0] != '{' {
		return nil, fmt.Errorf("%w: admin event data must be a JSON object of at most %d bytes", ErrPayload, MaxAdminData)
	}
	digest, err := deed.DigestJSON(data)
	if err != nil {
		return nil, err
	}
	var org string
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.current_setting('deedglass.org_id', true)`).Scan(&org); err != nil {
		return nil, err
	}
	if org == "" {
		return nil, store.ErrNoOrg
	}
	id := ids.New[ids.AdminEventKind]().String()
	d, err := deed.ForAdmin(deed.AdminSnapshot{Org: org, Agent: e.Agent, Event: id, Action: e.Action, Actor: e.Actor,
		Target: e.Target, Reason: nullable(e.Reason), DataDigest: &digest})
	if err != nil {
		return nil, err
	}
	env, err := l.sealer.Seal(ctx, store.DeedTx(tx), d)
	if err != nil {
		return nil, err
	}
	var tType, tID *string
	if e.Target != nil {
		tType, tID = &e.Target.Type, &e.Target.ID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deedglass.admin_events (id, org_id, actor_type, actor_id, action, target_type, target_id,
	    reason, data, data_digest, request_id, deed_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12)`,
		id, org, actorType, actorID, e.Action, tType, tID, nullable(e.Reason), string(data), digest, nullable(e.RequestID), env.ID); err != nil {
		return nil, err
	}
	seq := env.Body.Seq
	return env, l.Publish(ctx, tx, Event{Type: EventDeedSealed, Org: org, ID: env.ID, Seq: &seq, Deed: &env.ID, At: env.Body.IssuedAt})
}

package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
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

// ActionExecution is what happened when the tool ran; null when it never ran.
type ActionExecution struct {
	Status     *string `json:"status"`
	StartedAt  *Time   `json:"started_at"`
	FinishedAt *Time   `json:"finished_at"`
	LatencyMS  *int64  `json:"latency_ms"`
	ErrorCode  *string `json:"error_code"`
}

// ActionWitness is the Witness's verdict; null until a witness ran.
type ActionWitness struct {
	Level   string  `json:"level"`
	Verdict string  `json:"verdict"`
	Method  *string `json:"method"`
}

// Money is integer minor units plus a lowercase ISO 4217 currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Action is the action object (GET /v1/actions). Its sections mirror the deed's, so the Console
// reads one shape everywhere.
type Action struct {
	Object          string           `json:"object"` // "action"
	ID              string           `json:"id"`
	CreatedAt       Time             `json:"created_at"`
	Agent           AgentRef         `json:"agent"`
	Tool            string           `json:"tool"`
	Connector       *string          `json:"connector"`
	Mode            string           `json:"mode"`
	State           string           `json:"state"`
	OnBehalfOf      *string          `json:"on_behalf_of"`
	Session         *string          `json:"session"`
	Policy          ActionPolicy     `json:"policy"`
	Execution       *ActionExecution `json:"execution"`
	Witness         *ActionWitness   `json:"witness"`
	Cost            *Money           `json:"cost"`
	RequestDigest   string           `json:"request_digest"`
	ResultDigest    *string          `json:"result_digest"`
	PayloadPrunedAt *Time            `json:"payload_pruned_at"`
	Deed            *string          `json:"deed"`
}

// DetailWitness is the witness section of one action, with the stored diff.
type DetailWitness struct {
	Level   string          `json:"level"`
	Verdict string          `json:"verdict"`
	Method  *string         `json:"method"`
	Detail  json.RawMessage `json:"detail"` // null after pruning
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
	Agent           AgentRef           `json:"agent"`
	Tool            string             `json:"tool"`
	Connector       *string            `json:"connector"`
	Mode            string             `json:"mode"`
	State           string             `json:"state"`
	OnBehalfOf      *string            `json:"on_behalf_of"`
	Session         *string            `json:"session"`
	Policy          ActionPolicy       `json:"policy"`
	Execution       *ActionExecution   `json:"execution"`
	Witness         *DetailWitness     `json:"witness"`
	Cost            *Money             `json:"cost"`
	RequestDigest   string             `json:"request_digest"`
	ResultDigest    *string            `json:"result_digest"`
	PayloadPrunedAt *Time              `json:"payload_pruned_at"`
	Deed            *string            `json:"deed"`
	Request         json.RawMessage    `json:"request"`
	Result          json.RawMessage    `json:"result"`
	Events          *List[ActionEvent] `json:"events"`
	Deeds           []DeedRef          `json:"deeds"`
	Approval        json.RawMessage    `json:"approval"` // null until S22
}

const actionCols = `a.id, a.created_at, a.agent_id, g.name, a.tool, a.connector_id, a.mode, a.state, a.on_behalf_of,
  a.session_ref, pv.policy_id, a.policy_version_id, a.decision, a.decision_rule_id, a.execution_status, a.executing_at,
  COALESCE(a.executed_at, a.failed_at), a.latency_ms, a.error_code, a.witness_level, a.witness_verdict, a.witness_method,
  a.cost_amount, a.cost_currency, a.request_digest, a.result_digest, a.payload_pruned_at, a.deed_id`

const actionFrom = ` FROM deedglass.actions AS a
JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
LEFT JOIN deedglass.policy_versions AS pv ON pv.org_id = a.org_id AND pv.id = a.policy_version_id`

func scanAction(row pgx.Row, extra ...any) (Action, time.Time, error) {
	var (
		a                          Action
		created                    time.Time
		started, finished, pruned  *time.Time
		latency                    *int64
		execStatus, errCode        *string
		wLevel, wVerdict, wMethod  *string
		costAmount                 *int64
		costCurrency               *string
		polID, pvID, decision, rID *string
	)
	dest := []any{&a.ID, &created, &a.Agent.ID, &a.Agent.Name, &a.Tool, &a.Connector, &a.Mode, &a.State, &a.OnBehalfOf,
		&a.Session, &polID, &pvID, &decision, &rID, &execStatus, &started, &finished, &latency, &errCode,
		&wLevel, &wVerdict, &wMethod, &costAmount, &costCurrency, &a.RequestDigest, &a.ResultDigest, &pruned, &a.Deed}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return a, created, err
	}
	a.Object, a.CreatedAt = "action", Time(created)
	a.Policy = ActionPolicy{ID: polID, VersionID: pvID, Mode: "enforce", Decision: decision, RuleID: rID}
	if started != nil {
		if latency == nil && finished != nil {
			ms := finished.Sub(*started).Milliseconds()
			latency = &ms
		}
		a.Execution = &ActionExecution{Status: execStatus, StartedAt: tptr(started), FinishedAt: tptr(finished), LatencyMS: latency, ErrorCode: errCode}
	}
	if wLevel != nil && wVerdict != nil {
		a.Witness = &ActionWitness{Level: *wLevel, Verdict: *wVerdict, Method: wMethod}
	}
	if costAmount != nil && costCurrency != nil {
		a.Cost = &Money{Amount: *costAmount, Currency: *costCurrency}
	}
	a.PayloadPrunedAt = tptr(pruned)
	return a, created, nil
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
		rows, err = tx.Query(ctx, `SELECT id, kind, seq, issued_at FROM deedglass.deeds WHERE org_id = $1 AND action_id = $2 ORDER BY seq`, org, id)
		if err != nil {
			return err
		}
		d.Deeds, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeedRef, error) {
			var x DeedRef
			var at time.Time
			err := r.Scan(&x.ID, &x.Kind, &x.Seq, &at)
			x.IssuedAt = Time(at)
			return x, err
		})
		if d.Deeds == nil {
			d.Deeds = []DeedRef{}
		}
		return err
	})
	return d, err
}

func detailOf(a Action, req, res, detail []byte) *ActionDetail {
	d := &ActionDetail{Object: a.Object, ID: a.ID, CreatedAt: a.CreatedAt, Agent: a.Agent, Tool: a.Tool, Connector: a.Connector,
		Mode: a.Mode, State: a.State, OnBehalfOf: a.OnBehalfOf, Session: a.Session, Policy: a.Policy, Execution: a.Execution,
		Cost: a.Cost, RequestDigest: a.RequestDigest, ResultDigest: a.ResultDigest, PayloadPrunedAt: a.PayloadPrunedAt,
		Deed: a.Deed, Request: rawOrNull(req), Result: rawOrNull(res), Approval: json.RawMessage("null")}
	if w := a.Witness; w != nil {
		d.Witness = &DetailWitness{Level: w.Level, Verdict: w.Verdict, Method: w.Method, Detail: rawOrNull(detail)}
	}
	return d
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

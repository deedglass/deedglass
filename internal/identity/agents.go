package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
)

// Agents (S08 §3.1). Every agent has a name people recognise, labels policies can match, budgets,
// keys and a status. Every change is sealed as an admin deed and announced on deedglass_identity
// in the same transaction, so no instance keeps serving a stale status for longer than the
// listener's liveness bound.

// AgentStatus is an agent's status.
type AgentStatus string

const (
	AgentActive    AgentStatus = "active"
	AgentSuspended AgentStatus = "suspended"
	AgentStopped   AgentStatus = "stopped"
)

// Agent rules.
const (
	MaxLabels         = 32
	MaxLabelValue     = 128
	MaxDescription    = 500
	MaxStatusReason   = 280
	MinStartReason    = 3
	maxCallsPerMinute = 1_000_000
	maxCallsPerDay    = 100_000_000
	maxMoney          = 1<<53 - 1
)

var (
	agentNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	labelKeyRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,62}$`)
	currencyRE  = regexp.MustCompile(`^[a-z]{3}$`)
)

// ReservedAgentNames can't be registered (contract §3): S23's system agent is "console".
var ReservedAgentNames = []string{"console", "system", "deedglass"}

// Budgets are an agent's limits, in contract money. S08 stores and validates them; S11 enforces
// them.
type Budgets struct {
	CallsPerMinute *int64       `json:"calls_per_minute,omitempty"`
	CallsPerDay    *int64       `json:"calls_per_day,omitempty"`
	SpendPerDay    *httpx.Money `json:"spend_per_day,omitempty"`
}

// Agent is the agent object (GET /v1/agents/{id}).
type Agent struct {
	Object          string            `json:"object"` // "agent"
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Labels          map[string]string `json:"labels"`
	Budgets         Budgets           `json:"budgets"`
	Status          AgentStatus       `json:"status"`
	StatusReason    *string           `json:"status_reason"`
	StatusChangedAt *httpx.Time       `json:"status_changed_at"`
	StatusChangedBy *string           `json:"status_changed_by"`
	Owner           *string           `json:"owner"`
	ActiveKeys      int               `json:"active_keys"`
	IntentMode      IntentMode        `json:"intent_mode"` // S09: off | optional | required
	LastUsedAt      *httpx.Time       `json:"last_used_at"`
	CreatedAt       httpx.Time        `json:"created_at"`
	UpdatedAt       httpx.Time        `json:"updated_at"`
}

// AgentResult is an agent after a change, with the deed that sealed it (null for a no-op).
type AgentResult struct {
	Agent
	Deed *string `json:"deed"`
}

// Actor is who makes a change: the deed's actor and the text stored in created_by,
// status_changed_by, revoked_by and stopped_by (usr_…, key_… or operator).
type Actor struct {
	Ref deed.Ref
	By  string
}

// Operator is the actor of database-direct CLI commands.
var Operator = Actor{Ref: deed.Ref{Type: "system", ID: "operator"}, By: "operator"}

// ActorOf maps the request's principal to an actor. Agent keys are never actors: no agent key
// changes any agent, including its own (S08 §3.4).
func ActorOf(p *Principal) (Actor, error) {
	switch {
	case p == nil:
		return Actor{}, httpx.E(httpx.CodeAuthenticationRequired)
	case p.Kind == KindUser && !p.UserID.IsZero():
		return Actor{Ref: deed.Ref{Type: "user", ID: p.UserID.String()}, By: p.UserID.String()}, nil
	case p.Kind == httpx.KindAdminKey && !p.KeyID.IsZero():
		return Actor{Ref: deed.Ref{Type: "api_key", ID: p.KeyID.String()}, By: p.KeyID.String()}, nil
	}
	return Actor{}, httpx.ErrAgentKeyForAdmin()
}

// --- validation ---

// ValidAgentName reports whether name is a usable agent name.
func ValidAgentName(name string) bool { return agentNameRE.MatchString(name) }

func errAgentName() *httpx.Error {
	return httpx.ErrValidation("name", "Agent names use lowercase letters, digits and hyphens, 2 to 63 characters, like support-bot.")
}

func checkAgentName(name string) error {
	if !ValidAgentName(name) {
		return errAgentName()
	}
	if slices.Contains(ReservedAgentNames, name) {
		return httpx.ErrValidation("name", fmt.Sprintf("%s is reserved for Deedglass. Pick another name, like support-bot.", name))
	}
	return nil
}

func checkDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if utf8.RuneCountInString(d) > MaxDescription || !utf8.ValidString(d) || strings.ContainsFunc(d, func(r rune) bool { return r < 0x20 && r != '\n' || r == 0x7f }) {
		return "", httpx.ErrValidation("description", "The description must be at most 500 characters, without control characters.")
	}
	return d, nil
}

// CheckLabels validates labels: at most 32, keys like env or team/owner, values up to 128
// printable characters.
func CheckLabels(l map[string]string) error {
	if len(l) > MaxLabels {
		return httpx.ErrValidation("labels", fmt.Sprintf("An agent can have at most %d labels. You sent %d.", MaxLabels, len(l)))
	}
	for _, k := range slices.Sorted(maps.Keys(l)) {
		if !labelKeyRE.MatchString(k) {
			return httpx.ErrValidation("labels", fmt.Sprintf("Label keys use lowercase letters, digits and . _ / -, up to 63 characters, like env or team. %q isn't one.", clipText(k, 64)))
		}
		v := l[k]
		if utf8.RuneCountInString(v) > MaxLabelValue || !utf8.ValidString(v) || strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return httpx.ErrValidation("labels", fmt.Sprintf("The value of label %s must be at most 128 printable characters.", k))
		}
	}
	return nil
}

// CheckBudgets validates budgets: positive call counts and contract money (integer minor units,
// lowercase ISO 4217).
func CheckBudgets(b Budgets) error {
	if b.CallsPerMinute != nil && (*b.CallsPerMinute < 1 || *b.CallsPerMinute > maxCallsPerMinute) {
		return httpx.ErrValidation("budgets.calls_per_minute", "calls_per_minute must be a whole number from 1 to 1,000,000.")
	}
	if b.CallsPerDay != nil && (*b.CallsPerDay < 1 || *b.CallsPerDay > maxCallsPerDay) {
		return httpx.ErrValidation("budgets.calls_per_day", "calls_per_day must be a whole number from 1 to 100,000,000.")
	}
	if m := b.SpendPerDay; m != nil {
		if m.Amount < 0 || m.Amount > maxMoney {
			return httpx.ErrValidation("budgets.spend_per_day.amount", "spend_per_day.amount is in minor units (cents for usd): a whole number, 0 or more.")
		}
		if !currencyRE.MatchString(m.Currency) {
			return httpx.ErrValidation("budgets.spend_per_day.currency", "spend_per_day.currency is a lowercase ISO 4217 code, like usd or inr.")
		}
	}
	return nil
}

// checkReason trims and checks a reason: at most 280 characters, at least min when min > 0.
func checkReason(param, reason string, min int) (string, error) {
	reason = strings.TrimSpace(reason)
	n := utf8.RuneCountInString(reason)
	switch {
	case min > 1 && n < min:
		return "", httpx.ErrValidation(param, "Starting needs a reason of 3 to 280 characters, saying why it's safe now. It goes into the deed.")
	case min == 1 && n == 0:
		return "", httpx.ErrValidation(param, "Starting all agents needs a reason, saying why it's safe now. It goes into the deed.")
	case n > MaxStatusReason:
		return "", httpx.ErrValidation(param, "The reason must be at most 280 characters.")
	case strings.ContainsFunc(reason, func(r rune) bool { return r < 0x20 || r == 0x7f }) || !utf8.ValidString(reason):
		return "", httpx.ErrValidation(param, "The reason can't contain line breaks or control characters.")
	}
	return reason, nil
}

func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// --- reads ---

const agentCols = `a.id, a.name, COALESCE(a.description, ''), a.labels, a.budgets, a.status, a.status_reason,
  a.status_changed_at, a.status_changed_by, a.owner_user_id, a.created_at, a.updated_at, a.intent_mode,
  (SELECT pg_catalog.count(*) FROM deedglass.api_keys AS k WHERE k.org_id = a.org_id AND k.agent_id = a.id
     AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > pg_catalog.now()))::integer,
  (SELECT pg_catalog.max(k.last_used_at) FROM deedglass.api_keys AS k WHERE k.org_id = a.org_id AND k.agent_id = a.id)`

func scanAgent(row pgx.Row) (Agent, error) {
	var (
		a                   Agent
		labels, budgets     []byte
		changedAt, lastUsed *time.Time
		owner               *string
		created, updated    time.Time
	)
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &labels, &budgets, &a.Status, &a.StatusReason,
		&changedAt, &a.StatusChangedBy, &owner, &created, &updated, &a.IntentMode, &a.ActiveKeys, &lastUsed); err != nil {
		return Agent{}, err
	}
	a.Object = "agent"
	a.Labels = map[string]string{}
	if err := json.Unmarshal(labels, &a.Labels); err != nil {
		return Agent{}, err
	}
	if err := json.Unmarshal(budgets, &a.Budgets); err != nil {
		return Agent{}, err
	}
	a.StatusChangedAt, a.LastUsedAt = optTime(changedAt), optTime(lastUsed)
	a.Owner = owner
	a.CreatedAt, a.UpdatedAt = httpx.Time(created), httpx.Time(updated)
	return a, nil
}

func optTime(t *time.Time) *httpx.Time {
	if t == nil {
		return nil
	}
	v := httpx.Time(*t)
	return &v
}

// AgentFilter is GET /v1/agents's query.
type AgentFilter struct {
	Status  AgentStatus
	LabelK  string
	LabelV  string
	Name    string
	Limit   int
	AfterID string
}

// ListAgents returns one page of agents, oldest first.
func (s *Service) ListAgents(ctx context.Context, org ids.OrgID, f AgentFilter) ([]Agent, bool, error) {
	if f.Limit <= 0 {
		f.Limit = httpx.DefaultLimit
	}
	var label []byte
	if f.LabelK != "" {
		label, _ = json.Marshal(map[string]string{f.LabelK: f.LabelV})
	}
	var out []Agent
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+agentCols+` FROM deedglass.agents AS a
		    WHERE a.org_id = $1 AND ($2 = '' OR a.status = $2) AND ($3::jsonb IS NULL OR a.labels @> $3::jsonb)
		      AND ($4 = '' OR a.name = $4) AND ($5 = '' OR a.id > $5::text)
		    ORDER BY a.id LIMIT $6`, org, string(f.Status), label, f.Name, f.AfterID, f.Limit+1)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Agent, error) { return scanAgent(r) })
		return err
	})
	if err != nil {
		return nil, false, err
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}

// GetAgent returns one agent, or store.ErrNotFound.
func (s *Service) GetAgent(ctx context.Context, org ids.OrgID, id string) (Agent, error) {
	var a Agent
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = getAgent(ctx, tx, org, id)
		return err
	})
	return a, err
}

func getAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (Agent, error) {
	if ids.Check(ids.Agent, id) != nil {
		return Agent{}, store.ErrNotFound
	}
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentCols+` FROM deedglass.agents AS a WHERE a.org_id = $1 AND a.id = $2`, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, store.ErrNotFound
	}
	return a, err
}

// lockAgent takes the agent's row lock (lock order: org row, agent row, then the seal lock).
func lockAgent(ctx context.Context, tx pgx.Tx, org ids.OrgID, id string) (name string, status AgentStatus, err error) {
	if ids.Check(ids.Agent, id) != nil {
		return "", "", store.ErrNotFound
	}
	err = tx.QueryRow(ctx, `SELECT name, status FROM deedglass.agents WHERE org_id = $1 AND id = $2 FOR UPDATE`, org, id).Scan(&name, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", store.ErrNotFound
	}
	return name, status, err
}

// ErrAgentNotFound is the 404 for an agent id.
func ErrAgentNotFound(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("There's no agent %s in this org. Check the id, or list agents: GET /v1/agents.", clipText(id, 64)))
}

// --- create and patch ---

// CreateAgentInput is POST /v1/agents's body.
type CreateAgentInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
	Budgets     Budgets           `json:"budgets"`
	Owner       *string           `json:"owner"`
}

// CreateAgent registers an agent and seals agent.created.
func (s *Service) CreateAgent(ctx context.Context, a Actor, org ids.OrgID, in CreateAgentInput) (AgentResult, error) {
	if err := checkAgentName(in.Name); err != nil {
		return AgentResult{}, err
	}
	desc, err := checkDescription(in.Description)
	if err != nil {
		return AgentResult{}, err
	}
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	if err := CheckLabels(in.Labels); err != nil {
		return AgentResult{}, err
	}
	if err := CheckBudgets(in.Budgets); err != nil {
		return AgentResult{}, err
	}
	owner := in.Owner
	if owner == nil && a.Ref.Type == "user" {
		owner = &a.Ref.ID
	}
	labels, _ := json.Marshal(in.Labels)
	budgets, _ := json.Marshal(in.Budgets)
	id := ids.New[ids.AgentKind]()
	var res AgentResult
	err = s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if owner != nil {
			if err := checkOwner(ctx, tx, org, *owner); err != nil {
				return err
			}
		}
		// A savepoint, so a duplicate name can be reported without aborting the transaction.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		_, err = sp.Exec(ctx, `INSERT INTO deedglass.agents (id, org_id, name, description, owner_user_id, labels, budgets, created_by)
		    VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6::jsonb, $7::jsonb, $8)`, id, org, in.Name, desc, owner, string(labels), string(budgets), a.By)
		if store.SQLState(err) == store.CodeUniqueViolation {
			_ = sp.Rollback(ctx)
			oname, _ := orgName(ctx, tx, org)
			return httpx.E(httpx.CodeConflict, httpx.Param("name"),
				httpx.Msgf("An agent named %s already exists in %s. Pick another name.", in.Name, nonEmpty(oname, "this org")))
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return err
		}
		if err := sp.Commit(ctx); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAgentCreated, Actor: a.Ref,
			Target: &deed.Ref{Type: "agent", ID: id.String()}, Agent: &deed.AgentRef{ID: id.String(), Name: in.Name},
			Data: map[string]any{"name": in.Name, "labels": in.Labels, "budgets": in.Budgets}})
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, identityMsg{T: "agent", Org: org.String(), ID: id.String(), S: string(AgentActive)}); err != nil {
			return err
		}
		ag, err := getAgent(ctx, tx, org, id.String())
		res = AgentResult{Agent: ag, Deed: d}
		return err
	})
	return res, err
}

func checkOwner(ctx context.Context, tx pgx.Tx, org ids.OrgID, user string) error {
	if ids.Check(ids.User, user) != nil {
		return httpx.ErrValidation("owner", "owner must be the id of a person in this org, like usr_01k6c9kb2zcjsyn7b8g2k1vqvp.")
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2 AND status = 'active')`,
		org, user).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return httpx.ErrValidation("owner", "owner must be an active member of this org. Invite them first, or leave owner out.")
	}
	return nil
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// PatchAgentInput is PATCH /v1/agents/{id}'s body. Name is declared only to refuse it: names are
// immutable, because policies match on them.
type PatchAgentInput struct {
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	Labels      *map[string]string `json:"labels"`
	Budgets     *Budgets           `json:"budgets"`
	Owner       *string            `json:"owner"`       // "" clears it
	IntentMode  *IntentMode        `json:"intent_mode"` // S09: sealed as its own deed, agent.intent_mode_changed
}

// ErrNameImmutable is the 422 for a rename.
func ErrNameImmutable() *httpx.Error {
	e := httpx.ErrValidation("name", "Agent names can't change, because policies match on them. To rename, create a new agent with the new name and move its keys.")
	e.Status = 422
	return e
}

// PatchAgent changes description, labels, budgets or owner, and seals agent.updated with the
// changed field names (and the new labels and budgets, which feed policy matching). A patch that
// changes nothing seals nothing.
func (s *Service) PatchAgent(ctx context.Context, a Actor, org ids.OrgID, id string, in PatchAgentInput) (AgentResult, error) {
	if in.Name != nil {
		return AgentResult{}, ErrNameImmutable()
	}
	var res AgentResult
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		name, _, err := lockAgent(ctx, tx, org, id)
		if err != nil {
			return err
		}
		cur, err := getAgent(ctx, tx, org, id)
		if err != nil {
			return err
		}
		next := cur
		var changed []string
		data := map[string]any{}
		if in.Description != nil {
			d, err := checkDescription(*in.Description)
			if err != nil {
				return err
			}
			if d != cur.Description {
				next.Description, changed = d, append(changed, "description")
			}
		}
		if in.Labels != nil {
			l := *in.Labels
			if l == nil {
				l = map[string]string{}
			}
			if err := CheckLabels(l); err != nil {
				return err
			}
			if !maps.Equal(l, cur.Labels) {
				next.Labels, changed = l, append(changed, "labels")
				data["labels"] = l
			}
		}
		if in.Budgets != nil {
			if err := CheckBudgets(*in.Budgets); err != nil {
				return err
			}
			nb, _ := json.Marshal(*in.Budgets)
			ob, _ := json.Marshal(cur.Budgets)
			if string(nb) != string(ob) {
				next.Budgets, changed = *in.Budgets, append(changed, "budgets")
				data["budgets"] = *in.Budgets
			}
		}
		if in.Owner != nil {
			var o *string
			if *in.Owner != "" {
				if err := checkOwner(ctx, tx, org, *in.Owner); err != nil {
					return err
				}
				o = in.Owner
			}
			if !eqPtr(o, cur.Owner) {
				next.Owner, changed = o, append(changed, "owner")
			}
		}
		var modeDeed *string
		if in.IntentMode != nil {
			if modeDeed, err = s.setIntentMode(ctx, tx, a, org, id, name, cur.IntentMode, *in.IntentMode); err != nil {
				return err
			}
		}
		if len(changed) == 0 {
			ag, err := getAgent(ctx, tx, org, id)
			res = AgentResult{Agent: ag, Deed: modeDeed}
			return err
		}
		labels, _ := json.Marshal(next.Labels)
		budgets, _ := json.Marshal(next.Budgets)
		if _, err := tx.Exec(ctx, `UPDATE deedglass.agents SET description = NULLIF($3, ''), labels = $4::jsonb, budgets = $5::jsonb,
		    owner_user_id = $6 WHERE org_id = $1 AND id = $2`, org, id, next.Description, string(labels), string(budgets), next.Owner); err != nil {
			return err
		}
		data["fields"] = changed
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminAgentUpdated, Actor: a.Ref,
			Target: &deed.Ref{Type: "agent", ID: id}, Agent: &deed.AgentRef{ID: id, Name: name}, Data: data})
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, identityMsg{T: "agent", Org: org.String(), ID: id, S: string(cur.Status)}); err != nil {
			return err
		}
		ag, err := getAgent(ctx, tx, org, id)
		res = AgentResult{Agent: ag, Deed: d}
		return err
	})
	return res, err
}

func eqPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// --- the state machine ---

// StatusOp is one edge family of §3.1.
type StatusOp string

const (
	OpSuspend StatusOp = "suspend"
	OpResume  StatusOp = "resume"
	OpStop    StatusOp = "stop"
	OpStart   StatusOp = "start"
)

type opRule struct {
	from   []AgentStatus
	to     AgentStatus
	action string
}

var opRules = map[StatusOp]opRule{
	OpSuspend: {from: []AgentStatus{AgentActive}, to: AgentSuspended, action: ledger.AdminAgentSuspended},
	OpResume:  {from: []AgentStatus{AgentSuspended}, to: AgentActive, action: ledger.AdminAgentResumed},
	OpStop:    {from: []AgentStatus{AgentActive, AgentSuspended}, to: AgentStopped, action: ledger.AdminAgentStopped},
	OpStart:   {from: []AgentStatus{AgentStopped}, to: AgentActive, action: ledger.AdminAgentStarted},
}

// Transition reports what op does from status: "apply", "noop", or a 409 explaining the right
// call. It is the pure edge table of §3.1.
func Transition(op StatusOp, from AgentStatus) (string, bool) {
	r, ok := opRules[op]
	if !ok {
		return "", false
	}
	if slices.Contains(r.from, from) {
		return "apply", true
	}
	if from == r.to {
		return "noop", true
	}
	return "", false
}

func errBadEdge(op StatusOp, name string, from AgentStatus, id string) *httpx.Error {
	var msg string
	switch {
	case op == OpResume && from == AgentStopped:
		msg = fmt.Sprintf("%s is stopped, not suspended. Start it with POST /v1/agents/%s/start.", name, id)
	case op == OpStart && from == AgentSuspended:
		msg = fmt.Sprintf("%s is suspended, not stopped. Resume it with POST /v1/agents/%s/resume.", name, id)
	case op == OpSuspend && from == AgentStopped:
		msg = fmt.Sprintf("%s is stopped, which already refuses every call. Start it with POST /v1/agents/%s/start first if you mean to suspend it.", name, id)
	default:
		msg = fmt.Sprintf("%s is %s, so it can't be %s. Fetch the agent to see its status.", name, from, string(op)+"ed")
	}
	return httpx.E(httpx.CodeConflict, httpx.Msg(msg))
}

// errStopFailed is the 500 when a stop can't be recorded: nothing changed, and the API never says
// stopped unless the change committed.
func errStopFailed(err error) *httpx.Error {
	return httpx.E(httpx.CodeInternalError, httpx.Msg("Deedglass couldn't record the stop, so nothing changed. Try again. If it keeps failing, revoke the agent's keys."),
		httpx.Cause(err))
}

// SetAgentStatus applies op: suspend, resume, stop or start. One org-scoped transaction locks the
// agent row, updates it, seals the deed and sends the NOTIFY; if any step fails, nothing changes.
// Repeating an op that is already in effect returns the agent unchanged with no deed.
func (s *Service) SetAgentStatus(ctx context.Context, a Actor, org ids.OrgID, id string, op StatusOp, reason string) (AgentResult, error) {
	r, ok := opRules[op]
	if !ok {
		return AgentResult{}, fmt.Errorf("identity: unknown status op %q", op)
	}
	min := 0
	if op == OpStart {
		min = MinStartReason
	}
	reason, err := checkReason("reason", reason, min)
	if err != nil {
		return AgentResult{}, err
	}
	var res AgentResult
	err = s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		name, from, err := lockAgent(ctx, tx, org, id)
		if err != nil {
			return err
		}
		what, ok := Transition(op, from)
		if !ok {
			return errBadEdge(op, name, from, id)
		}
		if what == "noop" {
			ag, err := getAgent(ctx, tx, org, id)
			res = AgentResult{Agent: ag}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deedglass.agents SET status = $3, status_reason = NULLIF($4, ''),
		    status_changed_at = pg_catalog.now(), status_changed_by = $5 WHERE org_id = $1 AND id = $2`,
			org, id, string(r.to), reason, a.By); err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: r.action, Actor: a.Ref, Reason: reason,
			Target: &deed.Ref{Type: "agent", ID: id}, Agent: &deed.AgentRef{ID: id, Name: name},
			Data: map[string]any{"from": string(from), "to": string(r.to)}})
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, identityMsg{T: "agent", Org: org.String(), ID: id, S: string(r.to)}); err != nil {
			return err
		}
		// The contract event (S22 publishes it, so webhooks see Stop): start and resume both emit
		// agent.resumed.
		ev := map[StatusOp]string{OpSuspend: ledger.EventAgentSuspended, OpResume: ledger.EventAgentResumed,
			OpStop: ledger.EventAgentStopped, OpStart: ledger.EventAgentResumed}[op]
		st := string(r.to)
		if err := s.led.Publish(ctx, tx, ledger.Event{Type: ev, Org: org.String(), ID: id, State: &st, Deed: d}); err != nil {
			return err
		}
		ag, err := getAgent(ctx, tx, org, id)
		res = AgentResult{Agent: ag, Deed: d}
		return err
	})
	if err != nil && op == OpStop && httpx.AsError(err) == nil && !errors.Is(err, store.ErrNotFound) {
		return AgentResult{}, errStopFailed(err)
	}
	return res, err
}

// --- Stop all ---

// OrgStop is the org's Stop all overlay (POST /v1/org/stop-all and start-all).
type OrgStop struct {
	Object         string      `json:"object"` // "org_stop"
	Org            string      `json:"org"`
	Stopped        bool        `json:"stopped"`
	StoppedAt      *httpx.Time `json:"stopped_at"`
	StoppedBy      *string     `json:"stopped_by"`
	Reason         *string     `json:"reason"`
	AgentsAffected int         `json:"agents_affected"`
	Deed           *string     `json:"deed"`
}

// StopAll turns on the org overlay: every agent in the org is refused with org_stopped, whatever
// its own status. Repeating it changes nothing and seals nothing.
func (s *Service) StopAll(ctx context.Context, a Actor, org ids.OrgID, reason string) (OrgStop, error) {
	reason, err := checkReason("reason", reason, 0)
	if err != nil {
		return OrgStop{}, err
	}
	out, err := s.setOrgStop(ctx, a, org, true, reason)
	if err != nil && httpx.AsError(err) == nil {
		return OrgStop{}, errStopFailed(err)
	}
	return out, err
}

// StartAll clears the overlay. Agents stopped one by one stay stopped. Only an owner session or
// the server operator calls it; the reason is required.
func (s *Service) StartAll(ctx context.Context, a Actor, org ids.OrgID, reason string) (OrgStop, error) {
	reason, err := checkReason("reason", reason, 1)
	if err != nil {
		return OrgStop{}, err
	}
	return s.setOrgStop(ctx, a, org, false, reason)
}

func (s *Service) setOrgStop(ctx context.Context, a Actor, org ids.OrgID, stop bool, reason string) (OrgStop, error) {
	var out OrgStop
	err := s.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var (
			stoppedAt          *time.Time
			stoppedBy, stopWhy *string
		)
		err := tx.QueryRow(ctx, `SELECT stopped_at, stopped_by, stop_reason FROM deedglass.orgs WHERE id = $1 FOR NO KEY UPDATE`, org).
			Scan(&stoppedAt, &stoppedBy, &stopWhy)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		var affected int
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::integer FROM deedglass.agents WHERE org_id = $1 AND status <> 'stopped'`, org).
			Scan(&affected); err != nil {
			return err
		}
		out = OrgStop{Object: "org_stop", Org: org.String(), Stopped: stoppedAt != nil, StoppedAt: optTime(stoppedAt),
			StoppedBy: stoppedBy, Reason: stopWhy, AgentsAffected: affected}
		if (stoppedAt != nil) == stop {
			return nil // already in that state: no second deed
		}
		action := ledger.AdminOrgStartedAll
		state := "active"
		if stop {
			action, state = ledger.AdminOrgStoppedAll, "stopped"
			err = tx.QueryRow(ctx, `UPDATE deedglass.orgs SET stopped_at = pg_catalog.now(), stopped_by = $2, stop_reason = NULLIF($3, '')
			    WHERE id = $1 RETURNING stopped_at`, org, a.By, reason).Scan(&stoppedAt)
		} else {
			stoppedAt = nil
			_, err = tx.Exec(ctx, `UPDATE deedglass.orgs SET stopped_at = NULL, stopped_by = NULL, stop_reason = NULL WHERE id = $1`, org)
		}
		if err != nil {
			return err
		}
		d, err := s.recordAdmin(ctx, tx, ledger.AdminEvent{Action: action, Actor: a.Ref, Reason: reason,
			Target: &deed.Ref{Type: "org", ID: org.String()}, Data: map[string]any{"agents_affected": affected}})
		if err != nil {
			return err
		}
		if err := s.notify(ctx, tx, identityMsg{T: "org", Org: org.String(), ID: org.String(), S: state}); err != nil {
			return err
		}
		ev := ledger.EventOrgStarted
		if stop {
			ev = ledger.EventOrgStopped
		}
		if err := s.led.Publish(ctx, tx, ledger.Event{Type: ev, Org: org.String(), ID: org.String(), State: &state, Deed: d}); err != nil {
			return err
		}
		out.Stopped, out.StoppedAt, out.Deed = stop, optTime(stoppedAt), d
		out.StoppedBy, out.Reason = nil, nil
		if stop {
			by := a.By
			out.StoppedBy = &by
			if reason != "" {
				out.Reason = &reason
			}
		}
		return nil
	})
	return out, err
}

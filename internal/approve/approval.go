package approve

//deedglass:allow-sql-build List joins constant WHERE fragments (approval columns only); every filter value is a $n parameter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// Approval is the approval object (S22 §3.9). People and admin keys see all of it; an agent sees
// the agent view (AgentView): no context, no approvers, countersigned as a count, no names.
type Approval struct {
	Object         string          `json:"object"` // "approval"
	ID             string          `json:"id"`
	Action         string          `json:"action"`
	Agent          AgentRef        `json:"agent"`
	Tool           string          `json:"tool"`
	Status         Status          `json:"status"`
	RuleID         *string         `json:"rule_id"`
	Reason         *string         `json:"reason"`
	Required       int             `json:"required"`
	Approvers      []string        `json:"approvers,omitempty"`
	Countersigned  any             `json:"countersigned"` // []Countersign, or a count in the agent view
	DeniedBy       *UserRef        `json:"denied_by,omitempty"`
	DecisionReason *string         `json:"decision_reason"`
	Summary        string          `json:"summary"`
	Context        json.RawMessage `json:"context,omitempty"`
	ContextDigest  *string         `json:"context_digest"`
	You            *You            `json:"you,omitempty"`
	CreatedAt      ledger.Time     `json:"created_at"`
	ExpiresAt      ledger.Time     `json:"expires_at"`
	DecidedAt      *ledger.Time    `json:"decided_at"`
	Deed           *string         `json:"deed"`
	Livemode       bool            `json:"livemode"`
	// ActionStatus is the action's status after a decision (the decide responses): processing once
	// approved (the worker runs it; the approver never waits for the tool), denied, or
	// pending_approval while a four-eyes rule waits for another person.
	ActionStatus string `json:"action_status,omitempty"`
}

// AgentRef names the agent.
type AgentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// UserRef names a person.
type UserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Countersign is one person's approve vote.
type Countersign struct {
	User   UserRef `json:"user"`
	At     string  `json:"at"`
	Via    Via     `json:"via"`
	Reason *string `json:"reason"`
	Deed   string  `json:"deed"`
}

// You says what the person reading the approval may do.
type You struct {
	Eligible  bool    `json:"eligible"`
	CanDecide bool    `json:"can_decide"`
	WhyNot    *string `json:"why_not"`
}

// Viewer is who reads an approval: a person (a session), an org admin key, or an agent key (only its
// own agent's approvals, in the agent view).
type Viewer struct {
	Kind    string // httpx.KindUser | KindAdminKey | KindAgentKey
	Org     ids.OrgID
	UserID  ids.UserID
	AgentID ids.AgentID
}

// ViewerOf is the viewer behind a request's principal.
func ViewerOf(p httpx.Principal) Viewer {
	return Viewer{Kind: p.Kind, Org: p.OrgID, UserID: p.UserID, AgentID: p.AgentID}
}

func (v Viewer) agent() bool { return v.Kind == httpx.KindAgentKey }

// row is one approval as stored, with its action's facts.
type row struct {
	ID             ids.ApprovalID
	Org            ids.OrgID
	Action         ids.ActionID
	Status         Status
	RuleID         *string
	Reason         *string
	Required       int
	Spec           Spec
	ApprovedBy     []string
	DecidedBy      *string
	DecidedAt      *time.Time
	DecisionReason *string
	ExpiresAt      time.Time
	Context        []byte
	ContextDigest  *string
	Deed           *string
	CreatedAt      time.Time
	Agent          string
	AgentName      string
	Tool           string
	OnBehalfOf     *string
	Livemode       bool
	ActionState    string
	RequestDigest  string
}

const rowSQL = `SELECT ap.id, ap.org_id, ap.action_id, ap.status, ap.rule_id, ap.reason, ap.approvers_required, ap.approver_spec,
  ap.approved_by, ap.decided_by, ap.decided_at, ap.decision_reason, ap.expires_at, ap.context, ap.context_digest, ap.deed_id,
  ap.created_at, a.agent_id, g.name, COALESCE(ap.tool, a.tool), COALESCE(ap.on_behalf_of, a.on_behalf_of), a.livemode, a.state,
  a.request_digest
FROM deedglass.approvals AS ap
JOIN deedglass.actions AS a ON a.org_id = ap.org_id AND a.id = ap.action_id
JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id`

func scanRow(r pgx.Row) (*row, error) {
	var (
		x    row
		spec []byte
	)
	err := r.Scan(&x.ID, &x.Org, &x.Action, &x.Status, &x.RuleID, &x.Reason, &x.Required, &spec, &x.ApprovedBy, &x.DecidedBy,
		&x.DecidedAt, &x.DecisionReason, &x.ExpiresAt, &x.Context, &x.ContextDigest, &x.Deed, &x.CreatedAt, &x.Agent, &x.AgentName,
		&x.Tool, &x.OnBehalfOf, &x.Livemode, &x.ActionState, &x.RequestDigest)
	if err != nil {
		return nil, err
	}
	x.Spec = parseSpec(spec)
	x.Spec.Required = x.Required
	return &x, nil
}

// readRow reads one approval of org; lock takes the approval row FOR UPDATE.
func readRow(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ApprovalID, lock bool) (*row, error) {
	q := rowSQL + ` WHERE ap.org_id = $1 AND ap.id = $2`
	if lock {
		q += ` FOR UPDATE OF ap`
	}
	x, err := scanRow(tx.QueryRow(ctx, q, org, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound(id.String())
	}
	return x, err
}

// vote is one approval_votes row with the person's name.
type vote struct {
	User   ids.UserID
	Name   string
	Vote   Vote
	Reason *string
	Via    Via
	Deed   string
	At     time.Time
}

func readVotes(ctx context.Context, tx pgx.Tx, org ids.OrgID, id ids.ApprovalID) ([]vote, error) {
	rows, err := tx.Query(ctx, `SELECT v.user_id, COALESCE(u.display_name, pg_catalog.split_part(u.email, '@', 1)), v.vote, v.reason,
	    v.via, v.deed_id, v.created_at
	    FROM deedglass.approval_votes AS v JOIN deedglass.users AS u ON u.id = v.user_id
	    WHERE v.org_id = $1 AND v.approval_id = $2 ORDER BY v.created_at, v.user_id`, org, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (vote, error) {
		var v vote
		err := r.Scan(&v.User, &v.Name, &v.Vote, &v.Reason, &v.Via, &v.Deed, &v.At)
		return v, err
	})
}

func errNotFound(id string) *httpx.Error {
	return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no approval with id "+clip(id, 40)+". Check the id, or list them with GET /v1/approvals."))
}

// object builds the API object of x for viewer v (votes may be nil for the agent view).
func (s *Service) object(ctx context.Context, tx pgx.Tx, v Viewer, x *row, votes []vote, org orgInfo) (*Approval, error) {
	a := &Approval{Object: "approval", ID: x.ID.String(), Action: x.Action.String(), Agent: AgentRef{ID: x.Agent, Name: x.AgentName},
		Tool: x.Tool, Status: x.Status, RuleID: x.RuleID, Reason: x.Reason, Required: x.Required, ContextDigest: x.ContextDigest,
		CreatedAt: ledger.Time(x.CreatedAt), ExpiresAt: ledger.Time(x.ExpiresAt), Deed: x.Deed, Livemode: x.Livemode}
	if x.DecidedAt != nil {
		t := ledger.Time(*x.DecidedAt)
		a.DecidedAt = &t
	}
	snap, err := parseSnapshot(x.Context)
	if err != nil {
		return nil, err
	}
	if snap != nil {
		a.Summary = snap.Summary
	} else {
		a.Summary = Summary(x.AgentName, SummaryFacts{Tool: x.Tool})
	}
	approves := 0
	for _, vt := range votes {
		if vt.Vote == VoteApprove {
			approves++
		}
	}
	if v.agent() {
		// The agent view: a count, no names, no context; the deny reason is for the agent.
		a.Countersigned = len(x.ApprovedBy)
		if x.Status == StatusDenied && x.DecidedBy != nil && strings.HasPrefix(*x.DecidedBy, "usr_") {
			a.DecisionReason = x.DecisionReason
		}
		return a, nil
	}
	a.Approvers = append([]string{}, x.Spec.Approvers...)
	a.Context = x.Context
	a.DecisionReason = x.DecisionReason
	cs := []Countersign{}
	for _, vt := range votes {
		switch vt.Vote {
		case VoteApprove:
			cs = append(cs, Countersign{User: UserRef{ID: vt.User.String(), Name: vt.Name}, At: formatTime(vt.At), Via: vt.Via, Reason: vt.Reason, Deed: vt.Deed})
		case VoteDeny:
			a.DeniedBy = &UserRef{ID: vt.User.String(), Name: vt.Name}
		}
	}
	a.Countersigned = cs
	if v.Kind == httpx.KindUser && !v.UserID.IsZero() {
		you, err := s.you(ctx, tx, v, x, votes, org)
		if err != nil {
			return nil, err
		}
		a.You = you
	}
	return a, nil
}

// you is what the reading person may do now.
func (s *Service) you(ctx context.Context, tx pgx.Tx, v Viewer, x *row, votes []vote, org orgInfo) (*You, error) {
	p, ok, err := member(ctx, tx, v.Org, v.UserID)
	if err != nil {
		return nil, err
	}
	y := &You{}
	why := func(m string) (*You, error) { y.WhyNot = &m; return y, nil }
	if !ok || !x.Spec.Eligible(p) {
		return why(MsgNotEligible(deref(x.RuleID), x.Spec.Approvers))
	}
	y.Eligible = true
	switch {
	case x.Status != StatusPending:
		return why("This approval was already decided.")
	case !s.now().Before(x.ExpiresAt):
		return why("This approval has expired.")
	}
	for _, vt := range votes {
		if vt.User == v.UserID {
			return why("You already countersigned this.")
		}
	}
	y.CanDecide = true
	if obo := deref(x.OnBehalfOf); IsSelf(obo, p) && org.Settings.SelfCountersign != "allow" {
		m := "This action runs on your behalf, so another approver has to countersign it. You can still deny it."
		y.WhyNot = &m
	}
	return y, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Get returns one approval for v: an agent key sees only its own agent's approvals (anything else is
// not found).
func (s *Service) Get(ctx context.Context, v Viewer, id string) (*Approval, error) {
	aid, err := ids.Parse[ids.ApprovalKind](id)
	if err != nil {
		return nil, errNotFound(id)
	}
	var out *Approval
	err = s.o.DB.OrgReadTx(ctx, v.Org, func(ctx context.Context, tx pgx.Tx) error {
		x, err := readRow(ctx, tx, v.Org, aid, false)
		if err != nil {
			return err
		}
		if v.agent() && x.Agent != v.AgentID.String() {
			return errNotFound(id)
		}
		org, err := readOrg(ctx, tx, v.Org)
		if err != nil {
			return err
		}
		var votes []vote
		if !v.agent() {
			if votes, err = readVotes(ctx, tx, v.Org, aid); err != nil {
				return err
			}
		}
		out, err = s.object(ctx, tx, v, x, votes, org)
		return err
	})
	return out, err
}

// Wait is GET /v1/approvals/{id}?wait=: it returns when the approval's status is no longer pending,
// or when d passes. It wakes on the action's events (every approval change publishes one).
func (s *Service) Wait(ctx context.Context, v Viewer, id string, d time.Duration) (*Approval, error) {
	a, err := s.Get(ctx, v, id)
	if err != nil || d <= 0 || a.Status != StatusPending || s.gate == nil {
		return a, err
	}
	ch, unsub := s.gate.Broker().Subscribe(a.Action)
	defer unsub()
	wctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ch:
		case <-tick.C:
		case <-wctx.Done():
			return s.Get(context.WithoutCancel(ctx), v, id)
		}
		if a, err = s.Get(ctx, v, id); err != nil || a.Status != StatusPending {
			return a, err
		}
	}
}

// MaxByID is the most ids ByID reads at once.
const MaxByID = 200

// ByID returns v's view of the approvals with the given ids, in the list shape GET /v1/approvals
// uses, read in one read-only org transaction (S24: the live stream hydrates a batch of events with
// it). Unknown ids, another org's ids and, for an agent key, another agent's approvals are absent.
func (s *Service) ByID(ctx context.Context, v Viewer, approvalIDs []string) (map[string]*Approval, error) {
	if len(approvalIDs) > MaxByID {
		return nil, errors.New("approve: ByID takes at most " + itoa(MaxByID) + " ids")
	}
	want := make([]string, 0, len(approvalIDs))
	for _, id := range approvalIDs {
		if ids.Check(ids.Approval, id) == nil {
			want = append(want, id)
		}
	}
	out := make(map[string]*Approval, len(want))
	if len(want) == 0 {
		return out, nil
	}
	q := rowSQL + ` WHERE ap.org_id = $1 AND ap.id = ANY ($2::text[])`
	args := []any{v.Org, want}
	if v.agent() {
		q += ` AND a.agent_id = $3`
		args = append(args, v.AgentID.String())
	}
	err := s.o.DB.OrgReadTx(ctx, v.Org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		var xs []*row
		for rows.Next() {
			x, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			xs = append(xs, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(xs) == 0 {
			return nil
		}
		org, err := readOrg(ctx, tx, v.Org)
		if err != nil {
			return err
		}
		for _, x := range xs {
			var votes []vote
			if !v.agent() {
				if votes, err = readVotes(ctx, tx, v.Org, x.ID); err != nil {
					return err
				}
			}
			a, err := s.object(ctx, tx, v, x, votes, org)
			if err != nil {
				return err
			}
			out[a.ID] = a
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Filter narrows a list of approvals.
type Filter struct {
	Statuses []Status
	Agent    string
	Tool     string
}

// List is a page of approvals.
type List struct {
	Object     string      `json:"object"` // "list"
	Data       []*Approval `json:"data"`
	HasMore    bool        `json:"has_more"`
	NextCursor *string     `json:"next_cursor"`
}

// List returns approvals for v: pending ones (status exactly pending) oldest expiry first, others
// newest first. An agent key lists only its own agent's approvals.
func (s *Service) List(ctx context.Context, v Viewer, f Filter, limit int, cursor string) (*List, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	pendingOnly := len(f.Statuses) == 1 && f.Statuses[0] == StatusPending
	var cur struct {
		T  time.Time `json:"t"`
		ID string    `json:"id"`
	}
	if cursor != "" {
		if err := decodeCursor(cursor, &cur); err != nil {
			return nil, httpx.ErrValidation("cursor", "cursor isn't a cursor from this list. Use next_cursor from the previous page.")
		}
	}
	where := []string{"ap.org_id = $1"}
	args := []any{v.Org}
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, strings.ReplaceAll(cond, "$?", "$"+itoa(len(args))))
	}
	if len(f.Statuses) > 0 {
		st := make([]string, len(f.Statuses))
		for i, x := range f.Statuses {
			st[i] = string(x)
		}
		add("ap.status = ANY ($?::text[])", st)
	}
	if v.agent() {
		add("a.agent_id = $?", v.AgentID.String())
	} else if f.Agent != "" {
		add("(a.agent_id = $? OR g.name = $?)", f.Agent)
	}
	if f.Tool != "" {
		add("COALESCE(ap.tool, a.tool) = $?", f.Tool)
	}
	order := " ORDER BY ap.created_at DESC, ap.id DESC"
	if pendingOnly {
		order = " ORDER BY ap.expires_at, ap.id"
		if cursor != "" {
			args = append(args, cur.T, cur.ID)
			where = append(where, "(ap.expires_at, ap.id) > ($"+itoa(len(args)-1)+", $"+itoa(len(args))+")")
		}
	} else if cursor != "" {
		args = append(args, cur.T, cur.ID)
		where = append(where, "(ap.created_at, ap.id) < ($"+itoa(len(args)-1)+", $"+itoa(len(args))+")")
	}
	args = append(args, limit+1)
	q := rowSQL + " WHERE " + strings.Join(where, " AND ") + order + " LIMIT $" + itoa(len(args))
	out := &List{Object: "list", Data: []*Approval{}}
	err := s.o.DB.OrgReadTx(ctx, v.Org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		var xs []*row
		for rows.Next() {
			x, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			xs = append(xs, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(xs) > limit {
			xs, out.HasMore = xs[:limit], true
		}
		org, err := readOrg(ctx, tx, v.Org)
		if err != nil {
			return err
		}
		for _, x := range xs {
			var votes []vote
			if !v.agent() {
				if votes, err = readVotes(ctx, tx, v.Org, x.ID); err != nil {
					return err
				}
			}
			a, err := s.object(ctx, tx, v, x, votes, org)
			if err != nil {
				return err
			}
			out.Data = append(out.Data, a)
		}
		if out.HasMore {
			last := xs[len(xs)-1]
			t := last.CreatedAt
			if pendingOnly {
				t = last.ExpiresAt
			}
			c := encodeCursor(t, last.ID.String())
			out.NextCursor = &c
		}
		return nil
	})
	return out, err
}

// ParseStatuses reads ?status=pending,approved.
func ParseStatuses(s string) ([]Status, error) {
	if s == "" {
		return nil, nil
	}
	var out []Status
	for _, p := range strings.Split(s, ",") {
		switch st := Status(strings.TrimSpace(p)); st {
		case StatusPending, StatusApproved, StatusDenied, StatusExpired, StatusCancelled:
			out = append(out, st)
		default:
			return nil, httpx.ErrValidation("status", MsgStatusFilter)
		}
	}
	return out, nil
}

func itoa(n int) string { return strconv.Itoa(n) }

func encodeCursor(t time.Time, id string) string {
	b, _ := json.Marshal(struct {
		T  time.Time `json:"t"`
		ID string    `json:"id"`
	}{t.UTC(), id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(c string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

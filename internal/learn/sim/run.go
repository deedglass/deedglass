package sim

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/witness"
)

// Env is the deployment the simulator writes into (cmd/deedglass wires it from the config).
type Env struct {
	DB     *store.DB
	Ledger *ledger.Ledger // with a seal key
	People *identity.Service
	Log    *slog.Logger
}

// Options are one run's choices.
type Options struct {
	Scenario *Scenario
	Seed     uint64
	Days     int
	End      time.Time
	OrgID    string // --org: append into this simulated org
	OrgSlug  string // --org-slug: the fresh org's slug (default the scenario's), or the org to append into
	Append   bool
	Pace     time.Duration // a pause between actions (live demos)
	Progress func(done, total int)
}

// Errors the CLI turns into its sentences.
var (
	ErrNotSimulated = errors.New("sim: the org isn't a simulated org")
	ErrOverlap      = errors.New("sim: the new days overlap the org's history")
	ErrNoOrg        = errors.New("sim: no such org")
)

// OverlapError says up to when the org already has actions.
type OverlapError struct {
	Slug  string
	Until time.Time
}

func (e *OverlapError) Error() string {
	return fmt.Sprintf("sim: %s already has actions up to %s", e.Slug, e.Until)
}
func (e *OverlapError) Unwrap() error { return ErrOverlap }

// Run plans the scenario and pushes it through the real Gate, in virtual-time order (S27 §3.3).
func Run(ctx context.Context, env Env, o Options) (*Result, error) {
	if env.DB == nil || env.Ledger == nil || env.People == nil {
		return nil, errors.New("sim: DB, Ledger and People are required")
	}
	log := env.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sc := o.Scenario
	if sc == nil {
		sc = Default()
	}
	if o.Append && len(sc.Incidents) > 0 {
		// The scenario's incidents describe its first week, counted from the org's first day; days
		// appended later are ordinary traffic (S28: two quiet days for the shadow to watch).
		quiet := *sc
		quiet.Incidents = nil
		sc = &quiet
	}
	started := time.Now()
	w, err := Span(sc, o.Days, o.End)
	if err != nil {
		return nil, err
	}
	plan, err := Plan(sc, o.Seed, w)
	if err != nil {
		return nil, err
	}
	res := &Result{Seed: o.Seed, Window: w, Planned: len(plan), PlanDigest: PlanDigest(plan), Scenario: sc}
	// 1. The org: fresh (created like any org: default-deny and its bootstrap deed), or an existing
	// simulated org whose history ends before the new window.
	org, err := simOrg(ctx, env, sc, o, w)
	if err != nil {
		return nil, err
	}
	res.Org, res.Slug, res.Name, res.Appended = org.ID, org.Slug, org.Name, o.Append
	// 2. The approvers (active admins) and the agents.
	approvers := map[string]ids.UserID{}
	for i, email := range sc.Approvers {
		if i > 0 || o.Append {
			if err := addMember(ctx, env, org.ID, email); err != nil {
				return nil, err
			}
		}
		id, err := memberID(ctx, env.DB, org.ID, email)
		if err != nil {
			return nil, fmt.Errorf("sim: approver %s: %w", email, err)
		}
		approvers[email] = id
	}
	agents := map[string]ids.AgentID{}
	for _, a := range sc.Agents {
		id, err := ensureAgent(ctx, env, org.ID, a.Name)
		if err != nil {
			return nil, err
		}
		agents[a.Name] = id
		res.Agents = append(res.Agents, a.Name)
	}
	// 3. The policies: the enforced version now; the shadow when the virtual clock reaches its day.
	reg := policy.NewRegistry(policystore.NewRepo(env.DB, nil), nil, policy.WithRegistryLogger(log))
	svc := policy.NewService(policystore.NewRepo(env.DB, env.Ledger), policy.WithRegistry(reg))
	caller := policy.Caller{Org: org.ID.String(), Actor: policy.SystemActor}
	ev, err := pushAndActivate(ctx, svc, caller, sc.Policies.Enforce, policy.ModeEnforce, "Simulated org (deedglass dev simulate)")
	if err != nil {
		return nil, fmt.Errorf("sim: enforcing %s: %w", sc.Policies.EnforceFile, err)
	}
	res.Policy, res.EnforceVersion = ev.name, ev.version
	shadowDone := sc.Policies.Shadow == nil || sc.Policies.ShadowFromDay > o.Days
	// 4. The Gate, with the three simulated seams and the real approvals.
	clock := &Clock{}
	clock.Set(w.Start)
	sp := &script{}
	tools := map[string]bool{}
	for _, a := range plan {
		tools[a.Tool] = true
	}
	aps, err := approve.New(approve.Options{DB: env.DB, Ledger: env.Ledger, Logger: log, Clock: clock.Now})
	if err != nil {
		return nil, err
	}
	gp, err := gate.New(gate.Options{DB: env.DB, Ledger: env.Ledger, Policies: reg, Usage: policystore.NewUsage(env.DB), Approvals: aps,
		Resolver: &Resolver{clock: clock, script: sp, tools: tools}, Simulated: &Witness{script: sp, maxBytes: witness.DefaultMaxDetailBytes},
		Clock: clock.Now, Logger: log, Config: gate.Config{WaitTimeout: gate.MaxWait, ExecuteTimeout: 30 * time.Second}})
	if err != nil {
		return nil, err
	}
	aps.Attach(gp)
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = gp.Close(cctx)
		cancel()
	}()
	// 5. The events in virtual-time order: each planned action, then each approval's decision or
	// expiry as it falls due. Decisions after the window's end are left pending.
	q := &events{}
	for i := range plan {
		heap.Push(q, event{at: plan[i].At, seq: i, kind: evAction, a: &plan[i]})
	}
	n := 0
	for q.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e := heap.Pop(q).(event)
		if !e.at.Before(w.End) && e.kind != evAction {
			res.Pending++
			continue
		}
		clock.Set(e.at)
		switch e.kind {
		case evAction:
			if !shadowDone && e.a.Day >= sc.Policies.ShadowFromDay {
				sv, err := pushAndActivate(ctx, svc, caller, sc.Policies.Shadow, policy.ModeShadow, "Simulated shadow (deedglass dev simulate)")
				if err != nil {
					return nil, fmt.Errorf("sim: the shadow %s: %w", sc.Policies.ShadowFile, err)
				}
				res.ShadowVersion, res.ShadowFromDay, shadowDone = sv.version, sc.Policies.ShadowFromDay, true
			}
			sp.set(e.a)
			act, err := gp.Submit(ctx, principalOf(org, agents[e.a.Agent], e.a.Agent), gate.ActionRequest{Tool: e.a.Tool, Args: e.a.Args,
				Mode: gate.ModeGateway, Wait: gate.MaxWait, WaitSet: true, OnBehalfOf: e.a.UserRef, RequestID: ids.New[ids.RequestKind]().String(),
				Context: &gate.RequestContext{Client: &gate.ClientInfo{Name: "deedglass-simulate"}, Transport: gate.TransportSDK}})
			sp.set(nil)
			if act == nil {
				return nil, fmt.Errorf("sim: action %d (%s by %s): %w", e.a.Seq, e.a.Tool, e.a.Agent, err)
			}
			res.Submitted++
			if act.Status == ledger.StatusPendingApproval && act.Approval != nil {
				apr := act.Approval
				due := e.a.At.Add(e.a.Approval.Wait)
				if exp := time.Time(apr.ExpiresAt); !due.Before(exp) {
					heap.Push(q, event{at: exp, seq: e.seq, kind: evExpire, a: e.a, action: act.ID, approval: apr.ID})
				} else {
					heap.Push(q, event{at: due, seq: e.seq, kind: evDecide, a: e.a, action: act.ID, approval: apr.ID})
				}
			}
			n++
			if o.Progress != nil {
				o.Progress(n, len(plan))
			}
			if o.Pace > 0 {
				time.Sleep(o.Pace)
			}
		case evDecide:
			if err := decide(ctx, env, aps, gp, sp, org.ID, e, approvers); err != nil {
				return nil, err
			}
		case evExpire:
			sp.set(e.a)
			_, err := gp.Resume(ctx, org.ID, mustAction(e.action))
			sp.set(nil)
			if err != nil {
				return nil, fmt.Errorf("sim: expiring %s: %w", e.approval, err)
			}
		}
	}
	// 6. Approval jobs the Gate queued for a worker this process doesn't run: the simulator decided
	// the approvals itself, and simulated approvals send no email.
	if err := env.DB.OrgTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM deedglass.witness_jobs WHERE org_id = $1 AND kind IN ('approval_notify', 'approval_resume')`, org.ID)
		return err
	}); err != nil {
		return nil, err
	}
	if err := summarize(ctx, env, org.ID, res); err != nil {
		return nil, err
	}
	chk, err := env.Ledger.CheckChain(ctx, org.ID, ledger.Range{}, ledger.CheckOptions{})
	if err != nil {
		return nil, err
	}
	res.ChainIntact, res.ChainStatus = chk.Status == ledger.CheckIntact, string(chk.Status)
	res.Elapsed = time.Since(started)
	return res, nil
}

func mustAction(id string) ids.ActionID {
	a, _ := ids.Parse[ids.ActionKind](id)
	return a
}

// decide records the scripted person's vote through S22's service, then runs an approved action.
func decide(ctx context.Context, env Env, aps *approve.Service, gp *gate.Pipeline, sp *script, org ids.OrgID, e event, approvers map[string]ids.UserID) error {
	var digest string
	if err := env.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(context_digest, '') FROM deedglass.approvals WHERE org_id = $1 AND id = $2`, org, e.approval).Scan(&digest)
	}); err != nil {
		return err
	}
	vote, reason := approve.VoteApprove, "Approved in the simulation."
	if e.a.Approval.Vote == "deny" {
		vote, reason = approve.VoteDeny, "Denied in the simulation."
	}
	user, ok := approvers[e.a.Approval.Approver]
	if !ok {
		for _, u := range approvers {
			user = u
			break
		}
	}
	_, err := aps.Decide(ctx, approve.DecideInput{Org: org, ApprovalID: e.approval, Approver: approve.Approver{UserID: user, Via: approve.ViaConsole},
		Vote: vote, Reason: reason, ContextDigest: digest, RequestID: ids.New[ids.RequestKind]().String()})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) && (he.Code == httpx.CodeApprovalExpired || he.Code == httpx.CodeApprovalDecided) {
			return nil
		}
		return fmt.Errorf("sim: deciding %s: %w", e.approval, err)
	}
	if vote != approve.VoteApprove {
		return nil
	}
	sp.set(e.a)
	_, err = gp.Resume(ctx, org, mustAction(e.action))
	sp.set(nil)
	return err
}

func principalOf(org store.Org, agent ids.AgentID, name string) identity.Principal {
	return identity.Principal{Kind: httpx.KindAgentKey, OrgID: org.ID, AgentID: agent, AgentName: name, AgentStatus: policy.StatusActive,
		Scopes: []string{httpx.ScopeActionsExecute}, OrgName: org.Name}
}

// --- the event queue ---

type evKind int

const (
	evAction evKind = iota
	evDecide
	evExpire
)

type event struct {
	at       time.Time
	seq      int
	kind     evKind
	a        *PlannedAction
	action   string
	approval string
}

type events []event

func (q events) Len() int { return len(q) }
func (q events) Less(i, j int) bool {
	if !q[i].at.Equal(q[j].at) {
		return q[i].at.Before(q[j].at)
	}
	if q[i].kind != q[j].kind {
		return q[i].kind > q[j].kind // a decision due at the same instant goes before a new action
	}
	return q[i].seq < q[j].seq
}
func (q events) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *events) Push(x any)   { *q = append(*q, x.(event)) }
func (q *events) Pop() any {
	old := *q
	x := old[len(old)-1]
	*q = old[:len(old)-1]
	return x
}

// --- the org, its people and agents ---

func simOrg(ctx context.Context, env Env, sc *Scenario, o Options, w Window) (store.Org, error) {
	if o.Append {
		org, err := findOrg(ctx, env.DB, o.OrgID, o.OrgSlug)
		if err != nil {
			return org, err
		}
		var simulated bool
		var last *time.Time
		if err := env.DB.OrgReadTx(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT COALESCE((settings ->> 'simulated') = 'true', false) FROM deedglass.orgs WHERE id = $1`, org.ID).
				Scan(&simulated); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT pg_catalog.max(evaluated_at) FROM deedglass.actions WHERE org_id = $1`, org.ID).Scan(&last)
		}); err != nil {
			return org, err
		}
		if !simulated {
			return org, fmt.Errorf("%w: %s", ErrNotSimulated, org.Slug)
		}
		if last != nil && last.After(w.Start) {
			return org, &OverlapError{Slug: org.Slug, Until: last.UTC()}
		}
		return org, nil
	}
	base := o.OrgSlug
	if base == "" {
		base = sc.Org.Slug
	}
	slug := base
	for i := 2; ; i++ {
		_, err := findOrg(ctx, env.DB, "", slug)
		if errors.Is(err, ErrNoOrg) {
			break
		}
		if err != nil {
			return store.Org{}, err
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
	owner := "owner@example.test"
	if len(sc.Approvers) > 0 {
		owner = sc.Approvers[0]
	}
	c, err := identity.CreateOrg(ctx, env.DB, env.Ledger, sc.Org.Name, slug, owner)
	if err != nil {
		return store.Org{}, err
	}
	org := c.Org
	err = env.Ledger.OrgTxRetry(ctx, org.ID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET timezone = $2,
		      settings = pg_catalog.jsonb_set(settings, '{simulated}', 'true'::jsonb) WHERE id = $1`, org.ID, sc.Org.Timezone); err != nil {
			return err
		}
		_, err := env.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminOrgUpdated, Actor: deed.Ref{Type: "system", ID: "operator"},
			Target: &deed.Ref{Type: "org", ID: org.ID.String()}, Reason: "Marked as a simulated org (deedglass dev simulate).",
			Data: map[string]any{"timezone": sc.Org.Timezone, "simulated": true}})
		return err
	})
	org.Timezone = sc.Org.Timezone
	return org, err
}

func findOrg(ctx context.Context, db *store.DB, id, slug string) (store.Org, error) {
	var org store.Org
	err := db.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if id != "" {
			oid, perr := ids.Parse[ids.OrgKind](id)
			if perr != nil {
				return store.ErrNotFound
			}
			org, err = store.GetOrg(ctx, tx, oid)
		} else {
			org, err = store.GetOrgBySlug(ctx, tx, slug)
		}
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		return org, ErrNoOrg
	}
	return org, err
}

// addMember makes email an active admin of org once (and seals member.joined).
func addMember(ctx context.Context, env Env, org ids.OrgID, email string) error {
	var user ids.UserID
	if err := env.DB.SystemTx(ctx, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		u, err := store.UpsertUserByEmail(ctx, tx, email, "")
		if err != nil {
			return err
		}
		user = u.ID
		_, err = tx.Exec(ctx, `UPDATE deedglass.users SET status = 'active' WHERE id = $1 AND status = 'invited'`, u.ID)
		return err
	}); err != nil {
		return err
	}
	return env.Ledger.OrgTxRetry(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var mem ids.MembershipID
		err := tx.QueryRow(ctx, `SELECT id FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2`, org, user).Scan(&mem)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			mem = ids.New[ids.MembershipKind]()
			if _, err := tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
			    VALUES ($1, $2, $3, 'admin', 'active', pg_catalog.now())`, mem, org, user); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			return nil // already a member: nothing to seal
		}
		_, err = env.Ledger.RecordAdmin(ctx, tx, ledger.AdminEvent{Action: ledger.AdminMemberJoined, Actor: deed.Ref{Type: "system", ID: "operator"},
			Target: &deed.Ref{Type: "membership", ID: mem.String()}, Reason: "Added as a simulated approver (deedglass dev simulate)."})
		return err
	})
}

func memberID(ctx context.Context, db *store.DB, org ids.OrgID, email string) (ids.UserID, error) {
	var id ids.UserID
	err := db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT u.id FROM deedglass.users AS u JOIN deedglass.memberships AS m ON m.user_id = u.id
		    WHERE m.org_id = $1 AND u.email = $2 AND m.status = 'active'`, org, email).Scan(&id)
	})
	return id, err
}

func ensureAgent(ctx context.Context, env Env, org ids.OrgID, name string) (ids.AgentID, error) {
	var id ids.AgentID
	err := env.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, err := store.GetAgentByName(ctx, tx, org, name)
		id = a.ID
		return err
	})
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return id, err
	}
	r, err := env.People.CreateAgent(ctx, identity.Operator, org, identity.CreateAgentInput{Name: name,
		Description: "A simulated agent (deedglass dev simulate).", Labels: map[string]string{"simulated": "true"}})
	if err != nil {
		return id, fmt.Errorf("sim: agent %s: %w", name, err)
	}
	return ids.Parse[ids.AgentKind](r.ID)
}

type pushed struct {
	name    string
	version int
}

// pushAndActivate pushes src (a new version only when it changed) and activates it in mode.
func pushAndActivate(ctx context.Context, svc *policy.Service, caller policy.Caller, src []byte, mode policy.Mode, reason string) (pushed, error) {
	c, diags := policy.Compile(src, policy.Options{})
	if c == nil {
		return pushed{}, fmt.Errorf("the policy doesn't compile: %v", diags)
	}
	req := policy.PushRequest{Source: src, Origin: policy.OriginStarter, Notes: "Simulated org (deedglass dev simulate)."}
	var version int
	if _, err := svc.Get(ctx, caller, c.Name); err == nil {
		v, _, err := svc.AddVersion(ctx, caller, c.Name, req)
		if err != nil {
			return pushed{}, err
		}
		version = v.Version
	} else {
		_, v, err := svc.Create(ctx, caller, req)
		if err != nil {
			return pushed{}, err
		}
		version = v.Version
	}
	if _, err := svc.Activate(ctx, caller, c.Name, policy.VersionRef{Number: version}, policy.ActivateRequest{Mode: mode, Reason: reason}); err != nil {
		var pe *policy.Error
		if !errors.As(err, &pe) || pe.Kind != policy.KindConflict { // already active in this mode: nothing to do
			return pushed{}, err
		}
	}
	return pushed{name: c.Name, version: version}, nil
}

// sortedKeys is the map's keys, sorted.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

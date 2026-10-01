package identity

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// Guard is how the Gate (S13), the MCP gateway (S14, S15) and approvals (S22) honour Stop for work
// that started before it: a queued, approved or long-polled action re-checks right before it runs,
// and a long poll or stream ends the moment its agent or org is stopped.
var (
	ErrAgentStopped   = errors.New("agent stopped")
	ErrAgentSuspended = errors.New("agent suspended")
	ErrOrgStopped     = errors.New("org stopped")
)

// Guard is S08 §3.7's interface; *KeyAuth implements it.
type Guard interface {
	// CheckActive returns nil, or an *httpx.Error (org_stopped | agent_stopped | agent_suspended)
	// whose cause is ErrOrgStopped, ErrAgentStopped or ErrAgentSuspended. fresh=true reads
	// Postgres and skips every cache: use it right before executing anything that was queued,
	// approved or long-polled (S13, S22).
	CheckActive(ctx context.Context, org ids.OrgID, agent ids.AgentID, fresh bool) error
	// Watch returns a context cancelled with cause ErrAgentStopped or ErrOrgStopped when the agent
	// or its org is stopped (context.WithCancelCause). Long polls and MCP streams select on it.
	Watch(parent context.Context, org ids.OrgID, agent ids.AgentID) (context.Context, context.CancelFunc)
}

var _ Guard = (*KeyAuth)(nil)

// agentState is one fresh read of an agent and its org.
type agentState struct {
	name      string
	status    AgentStatus
	orgName   string
	orgStopAt *time.Time
}

func (k *KeyAuth) readState(ctx context.Context, org ids.OrgID, agent ids.AgentID) (agentState, error) {
	var st agentState
	err := k.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT a.name, a.status, o.name, o.stopped_at FROM deedglass.agents AS a
		    JOIN deedglass.orgs AS o ON o.id = a.org_id WHERE a.org_id = $1 AND a.id = $2`, org, agent).
			Scan(&st.name, &st.status, &st.orgName, &st.orgStopAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		return err
	})
	return st, err
}

func stateError(st agentState) error {
	switch {
	case st.orgStopAt != nil:
		e := httpx.ErrOrgStopped(st.orgName)
		httpx.Cause(ErrOrgStopped)(e)
		return e
	case st.status == AgentStopped:
		e := httpx.ErrAgentStopped(st.name)
		httpx.Cause(ErrAgentStopped)(e)
		return e
	case st.status == AgentSuspended:
		e := httpx.ErrAgentSuspended(st.name)
		httpx.Cause(ErrAgentSuspended)(e)
		return e
	case st.status != AgentActive:
		e := httpx.ErrAgentStopped(st.name) // an unknown status fails closed
		httpx.Cause(ErrAgentStopped)(e)
		return e
	}
	return nil
}

// CheckActive implements Guard.
func (k *KeyAuth) CheckActive(ctx context.Context, org ids.OrgID, agent ids.AgentID, fresh bool) error {
	if !fresh && !k.cache.Bypassed() {
		ag, okA := k.cache.Agent(agent)
		o, okO := k.cache.Org(org)
		if okA && okO && ag.OrgID == org {
			st := agentState{name: ag.Name, status: ag.Status, orgName: o.Name}
			if !o.StoppedAt.IsZero() {
				t := o.StoppedAt
				st.orgStopAt = &t
			}
			return stateError(st)
		}
	}
	e0 := k.cache.Epoch()
	st, err := k.readState(ctx, org, agent)
	if errors.Is(err, store.ErrNotFound) {
		e := httpx.ErrAgentStopped(agent.String()) // an agent that doesn't exist never acts
		httpx.Cause(ErrAgentStopped)(e)
		return e
	}
	if err != nil {
		return httpx.E(httpx.CodeServiceUnavailable, httpx.Msg("Deedglass can't check this agent's status because its database is unreachable. Retry in a few seconds."),
			httpx.Cause(err))
	}
	if !fresh {
		// This read lacks the org's API version, so only the agent is cached from it.
		k.cache.PutIfCurrent(e0, Loaded{Agent: &AgentEntry{ID: agent, OrgID: org, Name: st.name, Status: st.status}})
	}
	return stateError(st)
}

// Watch implements Guard.
func (k *KeyAuth) Watch(parent context.Context, org ids.OrgID, agent ids.AgentID) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	w := &watcher{org: org, agent: agent, cancel: cancel}
	k.watch.add(w)
	// A watcher registered after the stop must not wait for a notification that already came.
	go k.watch.check(w)
	return ctx, func() {
		k.watch.remove(w)
		cancel(context.Canceled)
	}
}

type watcher struct {
	org    ids.OrgID
	agent  ids.AgentID
	cancel context.CancelCauseFunc
}

// watchers is the set of open Watch contexts, by org.
type watchers struct {
	k  *KeyAuth
	mu sync.Mutex
	m  map[ids.OrgID]map[*watcher]struct{}
}

func newWatchers(k *KeyAuth) *watchers {
	return &watchers{k: k, m: map[ids.OrgID]map[*watcher]struct{}{}}
}

func (ws *watchers) add(w *watcher) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.m[w.org] == nil {
		ws.m[w.org] = map[*watcher]struct{}{}
	}
	ws.m[w.org][w] = struct{}{}
}

func (ws *watchers) remove(w *watcher) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	delete(ws.m[w.org], w)
	if len(ws.m[w.org]) == 0 {
		delete(ws.m, w.org)
	}
}

// Len is the number of open watchers.
func (ws *watchers) Len() int {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	n := 0
	for _, s := range ws.m {
		n += len(s)
	}
	return n
}

func (ws *watchers) snapshot(org, agent string) []*watcher {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	var out []*watcher
	for o, set := range ws.m {
		if org != "" && o.String() != org {
			continue
		}
		for w := range set {
			if agent == "" || w.agent.String() == agent {
				out = append(out, w)
			}
		}
	}
	return out
}

// recheck re-reads the state of the watchers for an org (agent "" = all of its agents) in the
// background, so the listener never blocks on a query.
func (ws *watchers) recheck(org, agent string) {
	list := ws.snapshot(org, agent)
	if len(list) == 0 {
		return
	}
	go func() {
		for _, w := range list {
			ws.check(w)
		}
	}()
}

func (ws *watchers) recheckAll() { ws.recheck("", "") }

func (ws *watchers) check(w *watcher) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := ws.k.readState(ctx, w.org, w.agent)
	switch {
	case errors.Is(err, store.ErrNotFound):
		w.cancel(ErrAgentStopped)
	case err != nil:
		return // unreachable: the next recheck (every 500 ms while bypassed) tries again
	case st.orgStopAt != nil:
		w.cancel(ErrOrgStopped)
	case st.status == AgentStopped:
		w.cancel(ErrAgentStopped)
	default:
		return
	}
	ws.remove(w)
}

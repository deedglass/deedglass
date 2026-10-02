package console

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// Hydrator reads the objects a batch of events is about, in the shapes the REST lists use
// (GET /v1/actions, /v1/approvals, /v1/agents), one read-only org transaction per call. The hub
// calls each method at most once per batch, and only for orgs someone is watching. Missing ids are
// left out of the map; an error sends the batch's events of that kind thin (hydrated:false).
type Hydrator interface {
	Actions(ctx context.Context, org string, ids []string) (map[string]json.RawMessage, error)
	Approvals(ctx context.Context, org string, ids []string) (map[string]json.RawMessage, error)
	Agents(ctx context.Context, org string, ids []string) (map[string]json.RawMessage, error)
}

// hydrated is one org's batch of list shapes; a nil map means that read failed or didn't run.
type hydrated struct {
	actions, approvals, agents map[string]json.RawMessage
}

// hydrate reads every object org's events in evs are about. memo is the org's known action →
// agent map: a deed.sealed whose action is already attributed needs no read.
func (h *Hub) hydrate(ctx context.Context, org string, evs []*ledger.Event, memo map[string]string) *hydrated {
	var acts, aprs, agts []string
	seen := map[string]bool{}
	add := func(list *[]string, id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			*list = append(*list, id)
		}
	}
	for _, ev := range evs {
		if ev.Org != org {
			continue
		}
		switch {
		case strings.HasPrefix(ev.Type, "action."):
			add(&acts, actionOf(ev))
		case ev.Type == ledger.EventDeedSealed:
			if ev.Action != nil && memo[*ev.Action] == "" && (ev.Kind == nil || *ev.Kind != "admin") {
				add(&acts, *ev.Action) // only to attribute it to its agent
			}
		case strings.HasPrefix(ev.Type, "approval."):
			add(&aprs, ev.ID)
		case strings.HasPrefix(ev.Type, "agent."):
			add(&agts, ev.ID)
		}
	}
	out := &hydrated{}
	call := func(ids []string, fn func(context.Context, string, []string) (map[string]json.RawMessage, error)) map[string]json.RawMessage {
		if len(ids) == 0 {
			return map[string]json.RawMessage{}
		}
		h.stats.hydrateCalls.Add(1)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.o.HydrateTimeout)
		defer cancel()
		m, err := fn(cctx, org, ids)
		if err != nil {
			h.stats.hydrateErrors.Add(1)
			h.o.Log.Warn("live events: reading the objects failed; sending them thin", "org", org, "error", err.Error())
			return nil
		}
		return m
	}
	out.actions = call(acts, h.o.Hydrator.Actions)
	out.approvals = call(aprs, h.o.Hydrator.Approvals)
	out.agents = call(agts, h.o.Hydrator.Agents)
	return out
}

// actionOf is the action an action.* event is about.
func actionOf(ev *ledger.Event) string {
	if ev.Action != nil {
		return *ev.Action
	}
	return ev.ID
}

// wireEvent is the SSE event object: S22's webhook envelope plus hydrated (contract §8).
type wireEvent struct {
	Object     string   `json:"object"` // "event"
	ID         string   `json:"id"`     // evt_…
	Type       string   `json:"type"`
	APIVersion string   `json:"api_version"`
	CreatedAt  string   `json:"created_at"`
	Livemode   bool     `json:"livemode"`
	Org        string   `json:"org"`
	Hydrated   bool     `json:"hydrated"`
	Data       wireData `json:"data"`
}

type wireData struct {
	Object any `json:"object"`
}

// DeedSummary is deed.sealed's hydrated object.
type DeedSummary struct {
	Object   string  `json:"object"` // "deed_summary"
	ID       string  `json:"id"`
	Seq      *int64  `json:"seq"`
	Kind     string  `json:"kind"` // action | amendment | admin
	Action   *string `json:"action"`
	IssuedAt string  `json:"issued_at"`
}

// listFacts are the few fields the hub reads from a list shape.
type listFacts struct {
	Agent *struct {
		ID string `json:"id"`
	} `json:"agent"`
	Livemode bool `json:"livemode"`
}

// approvalHidden are the approval list fields SSE never carries: the context snapshot (contract
// §8: never context), the approver spec (it can name people by email) and the per-person you.
var approvalHidden = []string{"context", "approvers", "you"}

// buildItem turns one event into its ring entry: the org-wide bytes, the agent-key bytes (nil when
// agent keys can't see it), the agent it is about, and an action → agent fact learned on the way.
func buildItem(ev *ledger.Event, hyd *hydrated, memo map[string]string) (it *item, act, agent string) {
	env := ledger.Envelope(*ev)
	w := wireEvent{Object: env.Object, ID: env.ID, Type: env.Type, APIVersion: env.APIVersion, CreatedAt: env.CreatedAt,
		Org: env.Org, Data: wireData{Object: env.Data.Object}}
	if hyd == nil {
		hyd = &hydrated{}
	}
	facts := func(raw json.RawMessage) listFacts {
		var f listFacts
		_ = json.Unmarshal(raw, &f)
		return f
	}
	agentVisible := false
	var thinAgent []byte // the agent view, when it differs from the org-wide one
	switch {
	case strings.HasPrefix(ev.Type, "action."):
		act = actionOf(ev)
		agent = memo[act]
		if raw, ok := hyd.actions[act]; ok {
			f := facts(raw)
			w.Data.Object, w.Hydrated, w.Livemode = raw, true, f.Livemode
			if f.Agent != nil {
				agent = f.Agent.ID
			}
		}
		agentVisible = true
	case ev.Type == ledger.EventDeedSealed:
		if ev.Kind != nil {
			w.Data.Object = DeedSummary{Object: "deed_summary", ID: ev.ID, Seq: ev.Seq, Kind: *ev.Kind, Action: ev.Action, IssuedAt: ev.At}
			w.Hydrated = true
		}
		if ev.Action != nil && (ev.Kind == nil || *ev.Kind != "admin") {
			act = *ev.Action
			agent = memo[act]
			if raw, ok := hyd.actions[act]; ok {
				f := facts(raw)
				w.Livemode = f.Livemode
				if f.Agent != nil {
					agent = f.Agent.ID
				}
			}
			agentVisible = true
		}
	case strings.HasPrefix(ev.Type, "approval."):
		if ev.Agent != nil {
			agent = *ev.Agent
		}
		if ev.Action != nil {
			act = *ev.Action
		}
		thin, _ := json.Marshal(w) // the agent view: the thin approval is exactly what an agent key may read
		thinAgent = thin
		agentVisible = true
		if raw, ok := hyd.approvals[ev.ID]; ok {
			if stripped, err := stripFields(raw, approvalHidden); err == nil {
				w.Data.Object, w.Hydrated, w.Livemode = stripped, true, facts(raw).Livemode
			}
		}
	case strings.HasPrefix(ev.Type, "agent."):
		agent = ev.ID
		if raw, ok := hyd.agents[ev.ID]; ok {
			w.Data.Object, w.Hydrated = raw, true
		}
	}
	full, err := json.Marshal(w)
	if err != nil {
		// A list shape that doesn't marshal: send the thin event rather than nothing.
		w.Data.Object, w.Hydrated = env.Data.Object, false
		full, _ = json.Marshal(w)
	}
	it = &item{typ: ev.Type, agent: agent, full: full}
	switch {
	case !agentVisible:
	case thinAgent != nil:
		it.agentV = thinAgent
	default:
		it.agentV = full
	}
	return it, act, agent
}

// stripFields removes top-level fields from a JSON object.
func stripFields(raw json.RawMessage, fields []string) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for _, f := range fields {
		delete(m, f)
	}
	return json.Marshal(m)
}

// DBHydrator reads list shapes from Postgres through the packages that own them, so the stream's
// objects are byte for byte what the REST lists return: ledger (actions), approve (approvals, in
// the org-wide view an admin key gets: no per-person you) and identity (agents).
type DBHydrator struct {
	Ledger  *ledger.Ledger
	Approve *approve.Service
	People  *identity.Service
}

var errNoSource = errors.New("console: this hydrator has no source for that kind")

func orgID(org string) (ids.OrgID, error) { return ids.Parse[ids.OrgKind](org) }

// Actions is GET /v1/actions's item shape.
func (d DBHydrator) Actions(ctx context.Context, org string, actionIDs []string) (map[string]json.RawMessage, error) {
	if d.Ledger == nil {
		return nil, errNoSource
	}
	o, err := orgID(org)
	if err != nil {
		return nil, err
	}
	m, err := d.Ledger.ActionsByID(ctx, o, actionIDs)
	if err != nil {
		return nil, err
	}
	return marshalAll(m)
}

// Approvals is GET /v1/approvals's item shape, as an org admin key sees it.
func (d DBHydrator) Approvals(ctx context.Context, org string, approvalIDs []string) (map[string]json.RawMessage, error) {
	if d.Approve == nil {
		return nil, errNoSource
	}
	o, err := orgID(org)
	if err != nil {
		return nil, err
	}
	m, err := d.Approve.ByID(ctx, approve.Viewer{Kind: httpx.KindAdminKey, Org: o}, approvalIDs)
	if err != nil {
		return nil, err
	}
	return marshalAll(m)
}

// Agents is GET /v1/agents's item shape.
func (d DBHydrator) Agents(ctx context.Context, org string, agentIDs []string) (map[string]json.RawMessage, error) {
	if d.People == nil {
		return nil, errNoSource
	}
	o, err := orgID(org)
	if err != nil {
		return nil, err
	}
	m, err := d.People.AgentsByID(ctx, o, agentIDs)
	if err != nil {
		return nil, err
	}
	return marshalAll(m)
}

func marshalAll[T any](m map[string]T) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		out[k] = b
	}
	return out, nil
}

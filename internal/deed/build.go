package deed

import (
	"fmt"
	"slices"
	"time"
)

// Draft is a Body without the fields the Sealer owns: DeedID, Seq, PrevHash, IssuedAt and
// SealKeyID. The builders below return valid drafts; Sealer.Seal fills the rest.
type Draft struct{ Body Body }

// ActionSnapshot is what ForAction needs: an action in a final state (contract §5). S06's
// SealAction and S13's Gate fill it from the actions row and its events.
type ActionSnapshot struct {
	Org        string
	Agent      AgentRef
	OnBehalfOf *string
	Session    *string

	ActionID      string
	Tool          string
	Connector     *string
	Mode          string // gateway | proxy
	RequestDigest string // DigestJSON of the tool arguments
	IntentHash    *string
	IntentSigned  bool

	Policy    PolicySection
	Approval  *ApprovalSnapshot  // nil when no approval was asked for
	Execution *ExecutionSnapshot // nil when the tool never ran (denied, expired, cancelled)
	Witness   *WitnessSection    // nil when no witness ran
	Cost      *Cost
}

// ApprovalSnapshot is the approval as stored; DecidedBy need not be sorted.
type ApprovalSnapshot struct {
	ID        string
	Outcome   string
	DecidedBy []string
	DecidedAt *time.Time
	Deed      *string
}

// ExecutionSnapshot is the execution as stored. LatencyMS is FinishedAt − StartedAt in whole
// milliseconds.
type ExecutionSnapshot struct {
	Status       string // succeeded | failed | unknown
	StartedAt    time.Time
	FinishedAt   time.Time
	ResultDigest *string
	ErrorCode    *string
}

// AdminSnapshot is what ForAdmin needs. S06's RecordAdmin fills it from the admin_events row.
type AdminSnapshot struct {
	Org        string
	Agent      *AgentRef // the agent the change was about, when there is one
	Event      string    // adm_…
	Action     string    // agent.stopped, member.invited, ledger.pruned
	Actor      Ref
	Target     *Ref
	Reason     *string
	DataDigest *string
}

// ForAction builds the draft of an action deed. It never reads a clock and does no I/O; times
// are formatted to the millisecond in UTC, and every field is validated.
func ForAction(a ActionSnapshot) (Draft, error) {
	agent := a.Agent
	b := Body{
		V: Version, Kind: KindAction, Org: a.Org, Agent: &agent,
		OnBehalfOf: cloneStr(a.OnBehalfOf), Session: cloneStr(a.Session),
		Action: &ActionSection{ID: a.ActionID, Tool: a.Tool, Connector: cloneStr(a.Connector), Mode: a.Mode,
			RequestDigest: a.RequestDigest, IntentHash: cloneStr(a.IntentHash), IntentSigned: a.IntentSigned},
	}
	pol := a.Policy
	pol.ID, pol.VersionID, pol.VersionHash, pol.RuleID = cloneStr(pol.ID), cloneStr(pol.VersionID), cloneStr(pol.VersionHash), cloneStr(pol.RuleID)
	b.Policy = &pol
	if ap := a.Approval; ap != nil {
		by := slices.Clone(ap.DecidedBy)
		if by == nil {
			by = []string{}
		}
		slices.Sort(by)
		s := &ApprovalSection{ID: ap.ID, Outcome: ap.Outcome, DecidedBy: by, Deed: cloneStr(ap.Deed)}
		if ap.DecidedAt != nil {
			t := FormatTime(*ap.DecidedAt)
			s.DecidedAt = &t
		}
		b.Approval = s
	}
	if ex := a.Execution; ex != nil {
		start, end := ex.StartedAt.Truncate(time.Millisecond), ex.FinishedAt.Truncate(time.Millisecond)
		if end.Before(start) {
			return Draft{}, fmt.Errorf("%w: %w", ErrDraft, errSchemaField("execution.finished_at", "must not be before started_at"))
		}
		b.Execution = &ExecutionSection{Status: ex.Status, StartedAt: FormatTime(start), FinishedAt: FormatTime(end),
			LatencyMS: end.Sub(start).Milliseconds(), ResultDigest: cloneStr(ex.ResultDigest), ErrorCode: cloneStr(ex.ErrorCode)}
	}
	if w := a.Witness; w != nil {
		c := *w
		c.DetailDigest = cloneStr(w.DetailDigest)
		b.Witness = &c
	}
	if a.Cost != nil {
		c := *a.Cost
		b.Cost = &c
	}
	return draft(b)
}

// ForAdmin builds the draft of an admin deed.
func ForAdmin(e AdminSnapshot) (Draft, error) {
	b := Body{V: Version, Kind: KindAdmin, Org: e.Org,
		Admin: &AdminSection{Event: e.Event, Action: e.Action, Actor: e.Actor, Reason: cloneStr(e.Reason), DataDigest: cloneStr(e.DataDigest)}}
	if e.Agent != nil {
		a := *e.Agent
		b.Agent = &a
	}
	if e.Target != nil {
		t := *e.Target
		b.Admin.Target = &t
	}
	return draft(b)
}

// ForAmendment builds the draft of an amendment deed: a later witness verdict about a sealed
// action deed. The org, agent, references and action section are copied from the original
// exactly; the original is never changed.
func ForAmendment(original *Body, w WitnessSection) (Draft, error) {
	if original == nil || original.Kind != KindAction {
		return Draft{}, fmt.Errorf("%w: only an action deed can be amended", ErrDraft)
	}
	if !isID("deed", original.DeedID) {
		return Draft{}, fmt.Errorf("%w: the original deed has no deed_id; seal it first", ErrDraft)
	}
	b := Body{V: Version, Kind: KindAmendment, Org: original.Org,
		OnBehalfOf: cloneStr(original.OnBehalfOf), Session: cloneStr(original.Session)}
	if original.Agent != nil {
		a := *original.Agent
		b.Agent = &a
	}
	if original.Action != nil {
		a := *original.Action
		a.Connector, a.IntentHash = cloneStr(a.Connector), cloneStr(a.IntentHash)
		b.Action = &a
	}
	w.DetailDigest = cloneStr(w.DetailDigest)
	b.Witness = &w
	id := original.DeedID
	b.Amends = &id
	return draft(b)
}

func draft(b Body) (Draft, error) {
	if err := b.validate(true); err != nil {
		return Draft{}, fmt.Errorf("%w: %w", ErrDraft, err)
	}
	if _, err := StrictCanonical(&b); err != nil {
		return Draft{}, fmt.Errorf("%w: %w", ErrDraft, err)
	}
	return Draft{Body: b}, nil
}

func cloneStr(p *string) *string {
	if p == nil {
		return nil
	}
	s := *p
	return &s
}

package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	gate "github.com/deedglass/deedglass/internal/gate/lifecycle"
	"github.com/deedglass/deedglass/internal/ids"
)

// Channel is the one NOTIFY channel for action and deed events (contract §8).
const Channel = "deedglass_events"

// Event types published on Channel (contract §8). S13, S22 and S24 add theirs through Publish.
const (
	EventActionCreated   = "action.created"
	EventActionDecided   = "action.decided"
	EventActionExecuted  = "action.executed"
	EventActionWitnessed = "action.witnessed"
	EventDeedSealed      = "deed.sealed"
	// EventPolicyActivated (S12): id is the activation (pa_…), state its mode, deed the
	// policy.activated deed.
	EventPolicyActivated = "policy.activated"
	// The decoder (S20): id is the connector (con_…). degraded and recovered carry the status in
	// state, the reason and the LSN; alert carries the kind (forged_marker, forged_marker_nontx,
	// foreign_ledger), the frame's commit LSN in lsn, and the named action when there is one.
	EventDecoderDegraded  = "decoder.degraded"
	EventDecoderRecovered = "decoder.recovered"
	EventDecoderAlert     = "decoder.alert"
	// Approvals (S22): id is the approval (apr_…); action, status, agent, tool, rule_id, required
	// and countersigned (a count) are set; deed is the decision's admin deed. decided carries the
	// final status: approved, denied, expired or cancelled.
	EventApprovalRequested = "approval.requested"
	EventApprovalDecided   = "approval.decided"
	// Agents and the org's Stop all (S22 publishes S08's changes): id is the agent (or org), state
	// the new status, deed the admin deed. Start and resume both emit agent.resumed (contract §8).
	EventAgentStopped   = "agent.stopped"
	EventAgentResumed   = "agent.resumed"
	EventAgentSuspended = "agent.suspended"
	EventOrgStopped     = "org.stopped"
	EventOrgStarted     = "org.started"
	// EventWebhookTest is sent to one endpoint only, by POST /v1/webhooks/{id}/test (S22).
	EventWebhookTest = "webhook.test"
)

// WebhookEvents are the event types an endpoint may subscribe to (contract §8): every type above
// that is published, plus the Insights events S27 and S28 add.
var WebhookEvents = []string{
	EventActionCreated, EventActionDecided, EventActionExecuted, EventActionWitnessed, EventDeedSealed,
	EventApprovalRequested, EventApprovalDecided,
	EventAgentStopped, EventAgentResumed, EventAgentSuspended, EventOrgStopped, EventOrgStarted,
	"anomaly.detected", "suggestion.created",
	EventPolicyActivated, EventDecoderDegraded, EventDecoderRecovered, EventDecoderAlert, EventWebhookTest,
}

// Event is the one payload shape on deedglass_events. Every key is always present, null when it
// doesn't apply. Payloads are ids and states, never request or result data.
type Event struct {
	V      int     `json:"v"`      // 1
	Event  string  `json:"event"`  // evt_…, assigned by Publish when empty
	Type   string  `json:"type"`   // action.created, deed.sealed, …
	Org    string  `json:"org"`    //
	ID     string  `json:"id"`     // the object the event is about (act_… or deed_…)
	Seq    *int64  `json:"seq"`    // deeds only
	Action *string `json:"action"` // act_…
	State  *string `json:"state"`  // action events: the state entered
	Deed   *string `json:"deed"`   // deed_…
	At     string  `json:"at"`     // RFC 3339, milliseconds, UTC
	// action.witnessed only (S19, additive): the claim, the level the policy required, and the
	// claim-level codes (level_below_required, no_checks). Absent on every other event.
	Level         *string  `json:"level,omitempty"`
	Verdict       *string  `json:"verdict,omitempty"`
	RequiredLevel *string  `json:"required_level,omitempty"`
	Flags         []string `json:"flags,omitempty"`
	// decoder.* (S20, additive): why the status changed or what an alert is, and the WAL position
	// it is about. Kind is also set on deed.sealed (S24): the deed's kind (action, amendment or
	// admin). Absent on every other event.
	Reason *string `json:"reason,omitempty"`
	Kind   *string `json:"kind,omitempty"`
	LSN    *string `json:"lsn,omitempty"`
	// approval.* only (S22, additive): the approval's status and request, never its context, a
	// person's name or an email address. Absent on every other event.
	Status        *string `json:"status,omitempty"`
	Agent         *string `json:"agent,omitempty"`
	Tool          *string `json:"tool,omitempty"`
	RuleID        *string `json:"rule_id,omitempty"`
	Required      *int    `json:"required,omitempty"`
	Countersigned *int    `json:"countersigned,omitempty"`
}

// eventFor maps a transition into to (contract §8 table): "" when the transition publishes nothing
// on its own (received → authenticated → evaluated; executed → witnessing).
func eventFor(from, to gate.State) string {
	switch to {
	case gate.Received:
		return EventActionCreated
	case gate.Denied, gate.PendingApproval, gate.Executing:
		if from == gate.Evaluated {
			return EventActionDecided
		}
		return "" // after an approval, S22 publishes approval.decided
	case gate.Executed, gate.Failed:
		return EventActionExecuted
	case gate.Verified, gate.Mismatch, gate.Unverifiable:
		return EventActionWitnessed
	case gate.Sealed:
		return EventDeedSealed
	}
	return ""
}

// Publish sends ev on deedglass_events inside tx: Postgres delivers it at COMMIT and drops it on
// ROLLBACK. It assigns the evt_ id and the time when they are empty.
func (l *Ledger) Publish(ctx context.Context, tx pgx.Tx, ev Event) error {
	b, err := marshalEvent(&ev, l.now())
	if err != nil {
		return err
	}
	bt := &pgx.Batch{}
	bt.Queue(notifySQL, Channel, string(b))
	if err := queueOutbox(bt, ev); err != nil {
		return err
	}
	return tx.SendBatch(ctx, bt).Close()
}

// notifySQL is the only pg_notify in Deedglass (TestOneWritePath): Publish runs it, and SealAction
// queues it in the same round trip as the sealed transition.
const notifySQL = `SELECT pg_catalog.pg_notify($1, $2)`

func marshalEvent(ev *Event, now time.Time) ([]byte, error) {
	ev.V = 1
	if ev.Event == "" {
		ev.Event = ids.New[ids.ActionEventKind]().String()
	}
	if ev.At == "" {
		ev.At = deed.FormatTime(now)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxNotifyPayload {
		return nil, fmt.Errorf("%w: event %s is %d bytes; the limit is %d", ErrPayload, ev.Type, len(b), MaxNotifyPayload)
	}
	return b, nil
}

func strp(s string) *string { return &s }

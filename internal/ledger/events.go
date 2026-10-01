package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
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
)

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
	_, err = tx.Exec(ctx, notifySQL, Channel, string(b))
	return err
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

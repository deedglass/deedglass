package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	slackc "github.com/deedglass/deedglass/internal/connector/slack"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/notify"
	"github.com/deedglass/deedglass/internal/store"
)

// JobNotifyUpdate is the job kind that edits a posted card (migration 0026). It always renders
// the approval as it is when the job runs, so a late job shows the newest state, and the card's
// version (the state's rank) never goes back.
const JobNotifyUpdate = "notify_update"

// MaxUpdateAttempts: a card update tries 4 times (the Gate's job backoff between them), then the
// card is marked delivery_failed for the Console. Slack never blocks an approval: the Console and
// email keep working.
const MaxUpdateAttempts = 4

// API is the Slack calls approvals make. Production uses internal/connector/slack (the only code
// that opens the bot token and the signing secret); unit tests replace it.
type API interface {
	Approvals(inst *connector.Instance) (slackc.ApprovalSettings, error)
	PostApproval(ctx context.Context, inst *connector.Instance, blocks json.RawMessage, text string) (channelID, ts string, err error)
	UpdateApproval(ctx context.Context, inst *connector.Instance, channelID, ts string, blocks json.RawMessage, text string) error
	LookupUser(ctx context.Context, inst *connector.Instance, userID string) (slackc.User, error)
	OpenView(ctx context.Context, inst *connector.Instance, triggerID string, view json.RawMessage) error
	Respond(ctx context.Context, inst *connector.Instance, responseURL, text string) error
	VerifyInteraction(ctx context.Context, inst *connector.Instance, h http.Header, body []byte, now time.Time) error
}

// ConnectorAPI is API over internal/connector/slack.
type ConnectorAPI struct{}

var _ API = ConnectorAPI{}

func (ConnectorAPI) Approvals(inst *connector.Instance) (slackc.ApprovalSettings, error) {
	return slackc.Approvals(inst)
}
func (ConnectorAPI) PostApproval(ctx context.Context, inst *connector.Instance, blocks json.RawMessage, text string) (string, string, error) {
	return slackc.PostApproval(ctx, inst, blocks, text)
}
func (ConnectorAPI) UpdateApproval(ctx context.Context, inst *connector.Instance, ch, ts string, blocks json.RawMessage, text string) error {
	return slackc.UpdateApproval(ctx, inst, ch, ts, blocks, text)
}
func (ConnectorAPI) LookupUser(ctx context.Context, inst *connector.Instance, userID string) (slackc.User, error) {
	return slackc.LookupUser(ctx, inst, userID)
}
func (ConnectorAPI) OpenView(ctx context.Context, inst *connector.Instance, triggerID string, view json.RawMessage) error {
	return slackc.OpenView(ctx, inst, triggerID, view)
}
func (ConnectorAPI) Respond(ctx context.Context, inst *connector.Instance, responseURL, text string) error {
	return slackc.Respond(ctx, inst, responseURL, text)
}
func (ConnectorAPI) VerifyInteraction(ctx context.Context, inst *connector.Instance, h http.Header, body []byte, now time.Time) error {
	return slackc.VerifyInteraction(ctx, inst, h, body, now)
}

// Instances turns a connectors row into its cached instance (*connector.Registry).
type Instances interface {
	Instance(org ids.OrgID, row store.Connector) (*connector.Instance, error)
	Type(t string) (connector.Connector, bool)
}

// Notifier is the Slack notifier (notify.Notifier): it posts the approval card when a rule names
// slack in notify:, and edits it in place until the deed is sealed.
type Notifier struct {
	Store   Store
	Conns   Instances
	API     API
	BaseURL string
	Log     *slog.Logger
	Now     func() time.Time
}

var (
	_ notify.Notifier        = (*Notifier)(nil)
	_ notify.ApprovalUpdater = (*Notifier)(nil)
)

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) log() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.Default()
}

func (n *Notifier) api() API {
	if n.API != nil {
		return n.API
	}
	return ConnectorAPI{}
}

// Name implements notify.Notifier.
func (n *Notifier) Name() string { return "slack" }

// ApprovalRequested posts the card (S22's approval_notify job calls it once). A failed post
// leaves a notify_update job behind that posts it later, while the approval is still open.
func (n *Notifier) ApprovalRequested(ctx context.Context, x notify.ApprovalNotice) error {
	err := n.ensure(ctx, x.OrgID, x.ApprovalID)
	if err != nil {
		if qerr := n.enqueue(ctx, x.OrgID, x.ActionID, 2*time.Second); qerr != nil {
			n.log().Warn("slack: queueing a card retry failed", "org_id", x.OrgID.String(), "approval_id", x.ApprovalID, "err", qerr.Error())
		}
	}
	return err
}

// ApprovalResolved implements notify.Notifier: the card is edited by a notify_update job.
func (n *Notifier) ApprovalResolved(ctx context.Context, x notify.ApprovalNotice) error {
	return n.enqueue(ctx, x.OrgID, x.ActionID, 0)
}

// ApprovalChanged implements notify.ApprovalUpdater: a countersign that isn't final yet (four-eyes).
func (n *Notifier) ApprovalChanged(ctx context.Context, org ids.OrgID, approvalID, actionID string) error {
	return n.enqueue(ctx, org, actionID, 0)
}

// enqueue adds a notify_update job for the action.
func (n *Notifier) enqueue(ctx context.Context, org ids.OrgID, action string, delay time.Duration) error {
	return n.Store.Enqueue(ctx, org, action, n.now().Add(delay), MaxUpdateAttempts)
}

// Watch follows the instance's event bus and queues a card update when an approval with a card is
// decided, expires or is cancelled, and when its action's deed is sealed. It returns when ctx ends.
func (n *Notifier) Watch(ctx context.Context, bus *ledger.Bus) {
	ch, cancel := bus.Subscribe(256)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-ch:
			if m.Event == nil || m.Event.Action == nil {
				continue
			}
			ev := m.Event
			switch {
			case ev.Type == ledger.EventApprovalDecided:
			case ev.Type == ledger.EventDeedSealed && ev.Kind != nil && *ev.Kind == "action":
			default:
				continue
			}
			org, err := ids.Parse[ids.OrgKind](ev.Org)
			if err != nil {
				continue
			}
			action := *ev.Action
			go func() {
				ctx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer done()
				// A short delay folds the burst approved → executed → witnessed → sealed into one edit.
				if err := n.Store.EnqueueIfCard(ctx, org, action, n.now().Add(300*time.Millisecond), MaxUpdateAttempts); err != nil {
					n.log().Warn("slack: queueing a card update failed", "org_id", org.String(), "action_id", action, "err", err.Error())
				}
			}()
		}
	}
}

// UpdateJob is the gate.JobHandler of notify_update: post the card if it was never posted (and
// the approval is still open), else edit every posted card to the current state.
func (n *Notifier) UpdateJob(ctx context.Context, org ids.OrgID, action ids.ActionID, attempts int) error {
	apr, err := n.Store.ApprovalOfAction(ctx, org, action.String())
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	err = n.ensure(ctx, org, apr)
	if err != nil && attempts >= MaxUpdateAttempts {
		n.markFailed(ctx, org, apr, err)
		return nil
	}
	return err
}

// ensure posts or edits the approval's card so it shows the approval as it is now. It repeats
// (at most 3 rounds) while the approval changes under it, so an event that lands while a job is
// running is never lost.
func (n *Notifier) ensure(ctx context.Context, org ids.OrgID, apr string) error {
	var last []byte
	for round := 0; round < 3; round++ {
		st, err := n.Store.CardState(ctx, org, apr)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !wantsSlack(st.Row.Notify) && len(st.Msgs) == 0 {
			return nil
		}
		if !st.Has {
			if len(st.Msgs) == 0 {
				n.log().Info("slack: a rule asks for Slack, but this org has no Slack connector with approvals enabled",
					"org_id", org.String(), "approval_id", apr)
			}
			return nil
		}
		inst, err := n.Conns.Instance(org, st.Conn)
		if err != nil {
			return err
		}
		view := viewOf(st.Row, n.Conns, n.BaseURL)
		blocks, text, err := Card(view)
		if err != nil {
			return fmt.Errorf("slack: building the card: %w", err)
		}
		if bytes.Equal(blocks, last) {
			return nil
		}
		rank := view.State.Rank()
		if len(st.Msgs) == 0 {
			if rank >= StateExpired.Rank() || view.State == StateApproved {
				return nil // decided before the card could be posted: nothing to ask for any more
			}
			ch, ts, err := n.api().PostApproval(ctx, inst, blocks, text)
			if err != nil {
				return err
			}
			if err := n.Store.InsertMessage(ctx, store.NotificationMessage{Org: org, ApprovalID: apr, ActionID: st.Row.ActionID,
				ConnectorID: st.Conn.ID, ChannelID: ch, TS: ts, Version: rank}); err != nil {
				return err
			}
		} else {
			for _, m := range st.Msgs {
				if m.Version > rank {
					continue // a newer state is already shown
				}
				if err := n.api().UpdateApproval(ctx, inst, m.ChannelID, m.TS, blocks, text); err != nil {
					return err
				}
				if err := n.Store.MarkShown(ctx, org, m.ID, rank); err != nil {
					return err
				}
			}
		}
		last = blocks
	}
	return nil
}

func (n *Notifier) markFailed(ctx context.Context, org ids.OrgID, apr string, cause error) {
	err := n.Store.MarkFailed(ctx, org, apr, failureText(cause))
	n.log().Warn("slack: a card couldn't be updated; the Console shows the approval", "org_id", org.String(), "approval_id", apr,
		"err", failureText(cause), "record_err", fmt.Sprint(err))
}

// failureText is what last_error keeps: a connector's code and message, never a body or a secret.
func failureText(err error) string {
	if ce := connector.AsError(err); ce != nil {
		return ce.Code + ": " + ce.AgentMessage
	}
	return "upstream_error"
}

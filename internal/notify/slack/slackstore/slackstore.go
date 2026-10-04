// Package slackstore is approvals in Slack over Postgres (S29): every call is one short org
// transaction with row-level security, except the routing lookup, which is the one cross-org
// SECURITY DEFINER routing function behind store.PreAuth.
package slackstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	slacknotify "github.com/deedglass/deedglass/internal/notify/slack"
	"github.com/deedglass/deedglass/internal/store"
)

// JobKind is the job kind card updates use.
const JobKind = slacknotify.JobNotifyUpdate

// DB is slack.Store over Postgres.
type DB struct{ DB *store.DB }

var _ slacknotify.Store = DB{}

// SlackRoute implements Store.
func (d DB) SlackRoute(ctx context.Context, team, app string) (ids.OrgID, string, bool, error) {
	return d.DB.PreAuth().SlackRoute(ctx, team, app)
}

// Connector implements Store.
func (d DB) Connector(ctx context.Context, org ids.OrgID, id string) (store.Connector, error) {
	var c store.Connector
	err := d.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = store.GetConnector(ctx, tx, id, false)
		return err
	})
	return c, err
}

// RecordInbound implements Store.
func (d DB) RecordInbound(ctx context.Context, org ids.OrgID, digest string, expires time.Time) (bool, error) {
	var fresh bool
	err := d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		fresh, err = store.RecordInbound(ctx, tx, org, "slack", digest, expires)
		return err
	})
	return fresh, err
}

// MemberByEmail implements Store.
func (d DB) MemberByEmail(ctx context.Context, org ids.OrgID, email string) (ids.UserID, error) {
	var id ids.UserID
	err := d.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = store.MemberByEmail(ctx, tx, org, email)
		return err
	})
	return id, err
}

// ApprovalCard implements Store.
func (d DB) ApprovalCard(ctx context.Context, org ids.OrgID, approvalID string) (*store.ApprovalCardRow, error) {
	var r *store.ApprovalCardRow
	err := d.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		r, err = store.ApprovalCard(ctx, tx, org, approvalID)
		return err
	})
	return r, err
}

// CardState implements Store.
func (d DB) CardState(ctx context.Context, org ids.OrgID, approvalID string) (slacknotify.CardState, error) {
	var st slacknotify.CardState
	err := d.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		r, err := store.ApprovalCard(ctx, tx, org, approvalID)
		if err != nil {
			return err
		}
		st.Row = r
		if st.Msgs, err = store.NotificationMessagesFor(ctx, tx, org, approvalID); err != nil {
			return err
		}
		c, err := store.SlackApprovalsConnector(ctx, tx, org)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if len(st.Msgs) > 0 {
				// The card was posted through a connector whose approvals were turned off since:
				// keep editing it there.
				if c, err = store.GetConnector(ctx, tx, st.Msgs[0].ConnectorID, false); err == nil {
					st.Conn, st.Has = c, true
				}
			}
			return nil
		case err != nil:
			return err
		}
		st.Conn, st.Has = c, true
		return nil
	})
	return st, err
}

// InsertMessage implements Store.
func (d DB) InsertMessage(ctx context.Context, m store.NotificationMessage) error {
	return d.DB.OrgTx(ctx, m.Org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.InsertNotificationMessage(ctx, tx, m)
		return err
	})
}

// MarkShown implements Store.
func (d DB) MarkShown(ctx context.Context, org ids.OrgID, id int64, version int) error {
	return d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := store.MarkNotificationShown(ctx, tx, org, id, version)
		return err
	})
}

// MarkFailed implements Store.
func (d DB) MarkFailed(ctx context.Context, org ids.OrgID, approvalID, lastError string) error {
	return d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		msgs, err := store.NotificationMessagesFor(ctx, tx, org, approvalID)
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if err := store.MarkNotificationFailed(ctx, tx, org, m.ID, lastError); err != nil {
				return err
			}
		}
		return nil
	})
}

// ApprovalOfAction implements Store.
func (d DB) ApprovalOfAction(ctx context.Context, org ids.OrgID, action string) (string, error) {
	var id string
	err := d.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		id, err = store.ApprovalOfAction(ctx, tx, org, action)
		return err
	})
	return id, err
}

// Enqueue implements Store.
func (d DB) Enqueue(ctx context.Context, org ids.OrgID, action string, runAfter time.Time, maxAttempts int) error {
	aid, err := ids.Parse[ids.ActionKind](action)
	if err != nil {
		return err
	}
	return d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return store.EnqueueJob(ctx, tx, org, aid, JobKind, runAfter, maxAttempts)
	})
}

// EnqueueIfCard implements Store.
func (d DB) EnqueueIfCard(ctx context.Context, org ids.OrgID, action string, runAfter time.Time, maxAttempts int) error {
	aid, err := ids.Parse[ids.ActionKind](action)
	if err != nil {
		return nil
	}
	return d.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		ok, err := store.HasNotificationMessage(ctx, tx, org, action)
		if err != nil || !ok {
			return err
		}
		return store.EnqueueJob(ctx, tx, org, aid, JobKind, runAfter, maxAttempts)
	})
}

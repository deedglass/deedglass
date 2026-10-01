package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// IdentityChannel is the NOTIFY channel for changes to who may act (contract §8): agents, API
// keys, the org stop overlay and memberships, plus each instance's liveness pings. Payloads carry
// ids and states only, never secrets, names or reasons (S08 §3.6).
const IdentityChannel = "deedglass_identity"

// MaxNotifyPayload is the payload limit Deedglass holds itself to (Postgres allows just under
// 8,000 bytes).
const MaxNotifyPayload = 200

// NotifyIdentity sends payload on deedglass_identity. Inside a transaction (tx non-nil) Postgres
// delivers it only if the transaction commits, and a full notification queue fails the
// transaction, so a change is never committed without its notification. With tx nil it runs on a
// pooled connection (the listener's self-ping).
func (db *DB) NotifyIdentity(ctx context.Context, tx pgx.Tx, payload string) error {
	if len(payload) > MaxNotifyPayload {
		return errors.New("store: identity notification payload is too long")
	}
	const q = `SELECT pg_catalog.pg_notify('deedglass_identity', $1)`
	if tx != nil {
		_, err := tx.Exec(ctx, q, payload)
		return err
	}
	_, err := db.pool.Exec(ctx, q, payload)
	return err
}

// PolicyChannel is the NOTIFY channel for policy activations (contract §8). The payload is the
// org id, nothing else: every instance reloads that org's policies from Postgres (S12 §3.5).
const PolicyChannel = "deedglass_policy"

// NotifyPolicy sends org on deedglass_policy inside tx: delivered only if the activation commits,
// and a failed notification rolls the activation back.
func NotifyPolicy(ctx context.Context, tx pgx.Tx, org string) error {
	if len(org) > MaxNotifyPayload || tx == nil {
		return errors.New("store: a policy notification needs a transaction and an org id")
	}
	_, err := tx.Exec(ctx, `SELECT pg_catalog.pg_notify('deedglass_policy', $1)`, org)
	return err
}

// ChannelListener runs extra NOTIFY channels on an instance's one dedicated LISTEN connection
// (S08's identity listener, generalized in S12). onMsg gets each payload; onReset runs after every
// (re)connect, because anything cached may have missed a notification while it was away.
type ChannelListener interface {
	Listen(channel string, onMsg func(payload string), onReset func())
}

// ListenConfig returns a copy of the pool's connection settings, for a dedicated connection that
// isn't part of the pool (the identity listener holds one for LISTEN).
func (db *DB) ListenConfig() *pgx.ConnConfig { return db.pool.Config().ConnConfig.Copy() }

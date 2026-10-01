package policystore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// BootstrapOrg is the identity.OrgCreateHook that gives a new org default-deny, enforced and sealed,
// inside org creation's own transaction (already scoped to the new org): an org without an
// enforced policy can't exist (S12 §3.10).
func BootstrapOrg(ctx context.Context, tx pgx.Tx, led *ledger.Ledger, org store.Org) error {
	if _, err := policy.Bootstrap(ctx, TxFor(tx, org.ID.String(), nil, led)); err != nil {
		return fmt.Errorf("enforcing default-deny for the new org: %w", err)
	}
	return nil
}

// BootstrapExisting enforces default-deny in an org that has no enforced policy (dev databases
// from before S12), in its own org transaction. It returns whether it activated anything.
func BootstrapExisting(ctx context.Context, db *store.DB, led *ledger.Ledger, org string) (bool, error) {
	var done bool
	err := NewRepo(db, led).InOrgTx(ctx, org, func(t policy.Tx) error {
		a, err := policy.Bootstrap(ctx, t)
		done = a != nil
		return err
	})
	return done, err
}

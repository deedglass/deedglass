package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/deedglass/deedglass/examples/policies"
)

// Onboarding (S12 §3.10): every new org starts with default-deny enforced, so nothing runs until a
// person activates a policy. Bootstrap runs inside org creation's one transaction (S07's
// identity.OrgCreateHooks), so an org without an enforced policy can't exist.

// DefaultDenyName is the starter every org is enforced with at creation.
const DefaultDenyName = "default-deny"

// BootstrapReason is the reason recorded in the onboarding deed.
const BootstrapReason = "New orgs start with default-deny."

// DefaultDenySource is examples/policies/default-deny.yaml, embedded.
func DefaultDenySource() ([]byte, error) { return policies.Source(DefaultDenyName) }

// Bootstrap saves default-deny v1 (origin onboarding, author system) in tx's org and enforces it
// with a sealed policy.activated deed whose actor is {system, deedglass}. An org that already has
// an enforced policy is left alone (nil, nil).
func Bootstrap(ctx context.Context, tx Tx) (*Activation, error) {
	if err := tx.LockOrgPolicies(ctx); err != nil {
		return nil, err
	}
	live, err := tx.LiveActivations(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range live {
		if a.Mode == ModeEnforce {
			return nil, nil
		}
	}
	src, err := DefaultDenySource()
	if err != nil {
		return nil, fmt.Errorf("policy: default-deny is not embedded: %w", err)
	}
	c, diags := Compile(src, Options{File: DefaultDenyName + ".yaml"})
	if c == nil {
		return nil, fmt.Errorf("policy: the embedded default-deny doesn't compile (%d diagnostics)", len(diags))
	}
	caller := Caller{Actor: SystemActor}
	req := PushRequest{Source: src, Origin: OriginOnboarding}
	var versionID string
	p, err := tx.GetPolicy(ctx, DefaultDenyName)
	switch {
	case errors.Is(err, ErrNotFound):
		_, v, err := createTx(ctx, tx, caller, c, req)
		if err != nil {
			return nil, err
		}
		versionID = v.ID
	case err != nil:
		return nil, err
	default: // an org that had default-deny pushed but nothing enforced (dev databases)
		v, err := tx.GetVersion(ctx, p.ID, VersionRef{Hash: c.VersionHash})
		if errors.Is(err, ErrNotFound) {
			n, err := tx.NextVersion(ctx, p.ID)
			if err != nil {
				return nil, err
			}
			row := versionRow(c, req, caller, p.ID, tx.NewID("pv"), n)
			if err := tx.InsertVersion(ctx, row); err != nil {
				return nil, err
			}
			versionID = row.ID
		} else if err != nil {
			return nil, err
		} else {
			versionID = v.ID
		}
	}
	return activateTx(ctx, tx, caller, DefaultDenyName, VersionRef{ID: versionID}, ModeEnforce, BootstrapReason, nil)
}

package licence

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Manager ties the files, the key set, the clock and the network together. Commands build one per
// run; tests inject every field.
type Manager struct {
	Path   string // the token file; the key sits next to it
	Keys   KeySet
	Client *Client
	Now    func() time.Time
	Live   bool // this build accepts live-mode tokens (else test-mode ones)
}

// Local is the token on disk, checked offline.
type Local struct {
	Token Token
	State State
}

// ErrMode means the token was issued in the other mode (test vs live).
var ErrMode = errors.New("licence: the token is for the other mode (test or live)")

// Load reads and verifies the token without touching the network.
func (m *Manager) Load() (Local, error) {
	t, err := LoadToken(m.Path)
	if err != nil {
		return Local{}, err
	}
	if err := t.Verify(m.Keys); err != nil {
		return Local{}, err
	}
	st := Evaluate(t, m.Now(), m.Live)
	if st.ModeMismatch {
		return Local{Token: t, State: st}, ErrMode
	}
	return Local{Token: t, State: st}, nil
}

// Activate registers the key on this machine, fetches a signed token, verifies it and writes the
// token and the key (both 0600).
func (m *Manager) Activate(ctx context.Context, key, hostname string) (Local, error) {
	if !ValidKey(key) {
		return Local{}, ErrKeyUnknown
	}
	inst, err := m.Client.Activate(ctx, key, InstanceName(hostname))
	if err != nil {
		return Local{}, err
	}
	if err := m.Client.Validate(ctx, key, inst); err != nil {
		return Local{}, err
	}
	return m.fetchAndSave(ctx, key, inst)
}

func (m *Manager) fetchAndSave(ctx context.Context, key, inst string) (Local, error) {
	t, err := m.Client.FetchToken(ctx, key, inst)
	if err != nil {
		return Local{}, err
	}
	if t.KeySHA256 != KeyHash(key) || t.Instance != inst {
		return Local{}, fmt.Errorf("%w: it names another key or machine", ErrBadTokenAnswer)
	}
	if err := t.Verify(m.Keys); err != nil {
		return Local{}, fmt.Errorf("%w: %v", ErrBadTokenAnswer, err)
	}
	st := Evaluate(t, m.Now(), m.Live)
	if st.ModeMismatch {
		return Local{}, ErrMode
	}
	if err := SaveKey(m.Path, key); err != nil {
		return Local{}, err
	}
	if err := SaveToken(m.Path, t); err != nil {
		return Local{}, err
	}
	return Local{Token: t, State: st}, nil
}

// Refresh re-validates the stored key with Dodo and replaces the token. A key Dodo no longer
// accepts (refund, cancel, expiry) removes the local licence at once; an unreachable service
// leaves the old token in place, so Pro keeps working inside its grace.
func (m *Manager) Refresh(ctx context.Context) (Local, error) {
	old, err := m.Load()
	if err != nil {
		return Local{}, err
	}
	key, err := LoadKey(m.Path)
	if err != nil {
		return old, err
	}
	if err := m.Client.Validate(ctx, key, old.Token.Instance); err != nil {
		if errors.Is(err, ErrKeyInactive) || errors.Is(err, ErrKeyUnknown) {
			_ = Remove(m.Path)
		}
		return old, err
	}
	l, err := m.fetchAndSave(ctx, key, old.Token.Instance)
	if err != nil {
		if errors.Is(err, ErrKeyInactive) || errors.Is(err, ErrKeyUnknown) {
			_ = Remove(m.Path)
		}
		return old, err
	}
	return l, nil
}

// Deactivate frees this machine's activation at Dodo and deletes the local files. With the
// network off or Dodo unreachable, the files stay so the person can try again.
func (m *Manager) Deactivate(ctx context.Context) error {
	t, err := LoadToken(m.Path)
	if err != nil {
		return err
	}
	key, err := LoadKey(m.Path)
	if err != nil {
		return err
	}
	if err := m.Client.Deactivate(ctx, key, t.Instance); err != nil && !errors.Is(err, ErrKeyInactive) {
		return err
	}
	return Remove(m.Path)
}

// Decision is the answer to "may this Pro feature run now?".
type Decision struct {
	Allowed   bool
	Local     Local
	Refreshed bool
	Err       error // why not, or the refresh problem that was tolerated
}

// Require answers whether feature may run. It refreshes at most once a day and only here, when a
// Pro feature is about to run; a failed refresh inside the grace period still allows it.
func (m *Manager) Require(ctx context.Context, feature string) Decision {
	l, err := m.Load()
	if err != nil {
		return Decision{Err: err}
	}
	if !l.Token.Has(feature) {
		return Decision{Local: l, Err: ErrFeature}
	}
	d := Decision{Local: l}
	if l.State.DueRefresh && !m.Client.NoNetwork {
		nl, rerr := m.Refresh(ctx)
		switch {
		case rerr == nil:
			d.Local, d.Refreshed = nl, true
		case errors.Is(rerr, ErrKeyInactive) || errors.Is(rerr, ErrKeyUnknown):
			return Decision{Local: l, Err: rerr}
		default:
			d.Err = rerr
		}
	}
	d.Allowed = d.Local.State.Active
	return d
}

// ErrFeature means the plan doesn't include the feature.
var ErrFeature = errors.New("licence: this plan doesn't include the feature")

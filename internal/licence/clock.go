package licence

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Offline clock rollback (security pre-launch M1). The grace formula trusts the local clock, so a
// clock set back (or pinned with libfaketime, or a CI container frozen on the activation day)
// would keep Pro working offline after a refund or cancel. Two floors stop that:
//
//   - the token's validated_at: the clock may not run more than ClockSkew behind it;
//   - the latest time this machine has seen for the token, kept in licence.clock next to
//     licence.json and moved forward on every check.
//
// A clock behind either floor marks the state ClockBack: Pro is paused and a refresh is due, so
// the grace window counts as over until the next successful online check (Require refreshes;
// with DEEDGLASS_NO_NETWORK or Dodo unreachable, Pro stays paused). A missing or altered mark
// counts the same way, so deleting it doesn't help. The mark is tied to the token id and carries
// an HMAC keyed by the licence key, so it can't be copied from another licence or edited by hand
// without deliberate work. Like the rest of an offline licence, it raises the cost; it is not a
// wall against someone who rewrites the files with the key in hand.

// ClockSkew is how far the clock may run behind the latest time seen (NTP corrections, VMs waking).
const ClockSkew = 5 * time.Minute

const (
	objectClock = "licence_clock"
	clockTag    = "deedglass/licence-clock/v1\n"
)

// ClockFileFor is the clock mark's file next to the token: licence.json → licence.clock.
func ClockFileFor(tokenPath string) string {
	return strings.TrimSuffix(tokenPath, filepath.Ext(tokenPath)) + ".clock"
}

type clockMark struct {
	Object  string `json:"object"`
	V       int    `json:"v"`
	TokenID string `json:"token_id"`
	MaxSeen string `json:"max_seen"`
	MAC     string `json:"mac"`
}

func clockMAC(key, tokenID, maxSeen string) string {
	k := sha256.Sum256([]byte(clockTag + key))
	m := hmac.New(sha256.New, k[:])
	m.Write([]byte(tokenID + "\n" + maxSeen))
	return "hmac-sha256:" + hex.EncodeToString(m.Sum(nil))
}

// loadClock returns the latest time seen for t. ok is false when the mark is missing, unreadable,
// for another token, or altered.
func loadClock(tokenPath, key string, t Token) (time.Time, bool) {
	b, err := readPrivate(ClockFileFor(tokenPath))
	if err != nil {
		return time.Time{}, false
	}
	var c clockMark
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || c.Object != objectClock || c.V != 1 || c.TokenID != t.ID { //deedglass:allow-compare the token id is public (it is in licence.json)
		return time.Time{}, false
	}
	if !hmac.Equal([]byte(c.MAC), []byte(clockMAC(key, t.ID, c.MaxSeen))) {
		return time.Time{}, false
	}
	seen, err := time.Parse(TimeLayout, c.MaxSeen)
	if err != nil {
		return time.Time{}, false
	}
	return seen, true
}

// saveClock writes the mark (0600) for t at seen.
func saveClock(tokenPath, key string, t Token, seen time.Time) error {
	s := seen.UTC().Format(TimeLayout)
	b, err := json.Marshal(clockMark{Object: objectClock, V: 1, TokenID: t.ID, MaxSeen: s, MAC: clockMAC(key, t.ID, s)})
	if err != nil {
		return err
	}
	return writePrivate(ClockFileFor(tokenPath), append(b, '\n'))
}

// behind applies the rollback rule to s: a clock more than ClockSkew behind floor pauses Pro and
// makes a refresh due.
func behind(s State, now, floor time.Time) State {
	if now.Before(floor.Add(-ClockSkew)) {
		s.ClockBack, s.Active, s.DueRefresh = true, false, true
	}
	return s
}

// checkClock applies the stored mark to st and moves the mark forward. A missing or altered mark
// (or an unreadable key) is treated as a clock that can't be trusted.
func (m *Manager) checkClock(t Token, now time.Time, st State) State {
	key, err := LoadKey(m.Path)
	if err != nil {
		st.ClockBack, st.Active, st.DueRefresh = true, false, true
		return st
	}
	seen, ok := loadClock(m.Path, key, t)
	if !ok {
		st.ClockBack, st.Active, st.DueRefresh = true, false, true
		return st
	}
	st = behind(st, now, seen)
	if !st.ClockBack && now.After(seen) {
		_ = saveClock(m.Path, key, t, now) // best effort: a read-only state dir only loses the forward move
	}
	return st
}

// trusted is the later of the local clock and the token's validated_at (the site's clock): right
// after a successful online check the site's time is the one to trust.
func trusted(t Token, now time.Time) time.Time {
	if v, err := time.Parse(TimeLayout, t.ValidatedAt); err == nil && v.After(now) {
		return v
	}
	return now
}

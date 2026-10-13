package licence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Security pre-launch M1: a clock set back (libfaketime, a pinned container) must not keep Pro
// working offline.

func TestEvaluateClockBehindValidatedAt(t *testing.T) {
	v := time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC)
	tok := Token{Body: Body{ValidatedAt: v.Format(TimeLayout), GraceDays: 14}}
	if s := Evaluate(tok, v.Add(-ClockSkew+time.Second), false); !s.Active || s.ClockBack {
		t.Fatalf("inside the skew: %+v", s)
	}
	s := Evaluate(tok, v.Add(-ClockSkew-time.Second), false)
	if s.Active || !s.ClockBack || !s.DueRefresh {
		t.Fatalf("behind validated_at: %+v", s)
	}
	if s := Evaluate(tok, v.Add(-30*24*time.Hour), false); s.Active || !s.ClockBack {
		t.Fatalf("a month behind: %+v", s)
	}
}

// activated returns a manager with a licence activated at *now and then used two days later, so
// the mark holds activation + 2 days.
func activated(t *testing.T) (*Manager, *fakeServices, *time.Time) {
	t.Helper()
	m, f, now := newManager(t)
	if _, err := m.Activate(context.Background(), "DG-7Q2M-TEST-0001", "h"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(48 * time.Hour)
	if l, err := m.Load(); err != nil || !l.State.Active || l.State.ClockBack {
		t.Fatalf("two days later: %+v %v", l.State, err)
	}
	return m, f, now
}

func TestClockMarkMovesForward(t *testing.T) {
	m, _, now := activated(t)
	key, _ := LoadKey(m.Path)
	tok, _ := LoadToken(m.Path)
	seen, ok := loadClock(m.Path, key, tok)
	if !ok || !seen.Equal(*now) {
		t.Fatalf("mark %v %v, want %v", seen, ok, *now)
	}
	if fi, err := os.Stat(ClockFileFor(m.Path)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("clock file: %v %v", fi, err)
	}
	// A clock a little behind (an NTP step) changes nothing and doesn't move the mark back.
	*now = now.Add(-time.Minute)
	if l, _ := m.Load(); !l.State.Active || l.State.ClockBack {
		t.Fatalf("one minute behind: %+v", l.State)
	}
	if again, _ := loadClock(m.Path, key, tok); !again.Equal(seen) {
		t.Fatal("the mark moved backwards")
	}
}

func TestClockRollbackPausesPro(t *testing.T) {
	m, f, now := activated(t)
	// The buyer refunds, then sets the clock back to the activation day and stays offline.
	f.valid.Store(false)
	*now = now.Add(-36 * time.Hour)
	l, err := m.Load()
	if err != nil || l.State.Active || !l.State.ClockBack || !l.State.DueRefresh {
		t.Fatalf("rolled back: %+v %v", l.State, err)
	}
	m.Client.NoNetwork = true
	if d := m.Require(context.Background(), FeatureScanFull); d.Allowed {
		t.Fatal("a rolled-back clock kept Pro offline")
	}
	// With the network on, Require checks online: the refunded key is refused and removed.
	m.Client.NoNetwork = false
	d := m.Require(context.Background(), FeatureScanFull)
	if d.Allowed || !errors.Is(d.Err, ErrKeyInactive) {
		t.Fatalf("online check after rollback: %+v", d)
	}
	if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
		t.Fatal("the refunded licence survived")
	}
	if _, err := os.Stat(ClockFileFor(m.Path)); !os.IsNotExist(err) {
		t.Fatal("the clock mark survived Remove")
	}
}

func TestClockRollbackUnreachableStaysPaused(t *testing.T) {
	m, _, now := activated(t)
	*now = now.Add(-36 * time.Hour)
	m.Client.Server = "http://127.0.0.1:1" // our token route is down; Dodo still answers
	d := m.Require(context.Background(), FeatureScanFull)
	if d.Allowed || d.Err == nil {
		t.Fatalf("unreachable during a rollback must not allow: %+v", d)
	}
}

func TestClockRollbackOnlineCheckRestores(t *testing.T) {
	m, f, now := activated(t)
	// The clock is a day behind and stays wrong; the site's time is the activation + 2 days.
	f.now = now.Add(time.Minute)
	*now = now.Add(-24 * time.Hour)
	d := m.Require(context.Background(), FeatureScanFull)
	if !d.Allowed || !d.Refreshed || d.Local.State.ClockBack {
		t.Fatalf("a successful online check restores Pro: %+v", d)
	}
	// The next offline run still sees a clock behind the site's validated_at: paused again.
	m.Client.NoNetwork = true
	if d := m.Require(context.Background(), FeatureScanFull); d.Allowed {
		t.Fatal("offline with the clock still behind")
	}
	// Once the clock is right, offline works again.
	*now = f.now.Add(time.Hour)
	if d := m.Require(context.Background(), FeatureScanFull); !d.Allowed {
		t.Fatalf("clock fixed: %+v", d)
	}
}

func TestClockMarkMissingOrAltered(t *testing.T) {
	cases := map[string]func(m *Manager){
		"deleted": func(m *Manager) { os.Remove(ClockFileFor(m.Path)) },
		"edited back": func(m *Manager) {
			b, _ := os.ReadFile(ClockFileFor(m.Path))
			s := strings.Replace(string(b), "2026-10-23", "2026-10-21", 1)
			os.WriteFile(ClockFileFor(m.Path), []byte(s), 0o600)
		},
		"another token's mark": func(m *Manager) {
			key, _ := LoadKey(m.Path)
			tok, _ := LoadToken(m.Path)
			tok.ID = "lic_01jdzzzzzzzzzzzzzzzzzzzzzz"
			saveClock(m.Path, key, tok, time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC))
		},
		"another key's mac": func(m *Manager) {
			tok, _ := LoadToken(m.Path)
			saveClock(m.Path, "DG-OTHER-KEY-0001", tok, time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC))
		},
		"a signed mark with a bad time": func(m *Manager) {
			key, _ := LoadKey(m.Path)
			tok, _ := LoadToken(m.Path)
			b, _ := json.Marshal(clockMark{Object: objectClock, V: 1, TokenID: tok.ID, MaxSeen: "soon", MAC: clockMAC(key, tok.ID, "soon")})
			os.WriteFile(ClockFileFor(m.Path), b, 0o600)
		},
		"not json":       func(m *Manager) { os.WriteFile(ClockFileFor(m.Path), []byte("{"), 0o600) },
		"key gone":       func(m *Manager) { os.Remove(KeyFileFor(m.Path)) },
		"world readable": func(m *Manager) { os.Chmod(ClockFileFor(m.Path), 0o644) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, _, _ := activated(t)
			mutate(m)
			l, err := m.Load()
			if err != nil || l.State.Active || !l.State.ClockBack || !l.State.DueRefresh {
				t.Fatalf("%s: %+v %v", name, l.State, err)
			}
			m.Client.NoNetwork = true
			if d := m.Require(context.Background(), FeatureScanFull); d.Allowed {
				t.Fatalf("%s: Pro ran offline", name)
			}
		})
	}
	// A deleted mark comes back with the next successful online check.
	m, _, _ := activated(t)
	os.Remove(ClockFileFor(m.Path))
	if d := m.Require(context.Background(), FeatureScanFull); !d.Allowed || !d.Refreshed {
		t.Fatalf("online check after a deleted mark: %+v", d)
	}
	if _, err := os.Stat(ClockFileFor(m.Path)); err != nil {
		t.Fatal("the mark wasn't rewritten")
	}
}

func TestClockMarkWriteFailures(t *testing.T) {
	// fetchAndSave reports a mark it can't write; Load's forward move is best effort.
	m, _, now := activated(t)
	os.Remove(ClockFileFor(m.Path))
	os.Mkdir(ClockFileFor(m.Path), 0o700)
	if _, err := m.Refresh(context.Background()); err == nil {
		t.Fatal("a mark that can't be written must fail the save")
	}
	os.Remove(ClockFileFor(m.Path))
	key, _ := LoadKey(m.Path)
	tok, _ := LoadToken(m.Path)
	if err := saveClock(m.Path, key, tok, *now); err != nil {
		t.Fatal(err)
	}
	v, _ := time.Parse(TimeLayout, tok.ValidatedAt)
	if got := trusted(tok, v.Add(-time.Hour)); !got.Equal(v) {
		t.Fatalf("a clock behind validated_at trusts the site: %v", got)
	}
	if got := trusted(tok, v.Add(time.Hour)); !got.Equal(v.Add(time.Hour)) {
		t.Fatalf("a clock ahead is kept: %v", got)
	}
	if got := trusted(Token{}, *now); !got.Equal(*now) {
		t.Fatal("trusted without validated_at is the local clock")
	}
}

func TestKeySetsSeparateTestAndLive(t *testing.T) {
	live, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	test, err := ParseKeySet(mustRead(t, "licence-keys-test.json"))
	if err != nil {
		t.Fatal(err)
	}
	for id := range test.keys {
		if _, ok := live.lookup(id); ok {
			t.Fatalf("test key %s is in the live set", id)
		}
		for _, pub := range live.keys {
			if pub.Equal(test.keys[id]) {
				t.Fatal("the test and live key sets share a public key")
			}
		}
	}
	if l, _ := EmbeddedFor(true); l.Len() != live.Len() {
		t.Fatal("EmbeddedFor(true) is not the live set")
	}
	tk, err := EmbeddedFor(false)
	if err != nil {
		t.Fatal(err)
	}
	if TestKeysBuiltIn() {
		if tk.Len() != test.Len() {
			t.Fatal("a licencetest build must embed licence-keys-test.json")
		}
	} else if tk.Len() != 0 {
		t.Fatal("a release build must embed no test-mode key")
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

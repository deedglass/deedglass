package licence

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite testdata/tokens.golden.json")

// The test key: never a real licence key. The site's unit tests sign with the same seed and must
// produce the same bytes (site/tests/unit/commerce-licence.test.ts reads a copy of the golden).
const testKeyID = "lk_01jdtestkey00000000000000a"

func testSigner(t *testing.T) (ed25519.PrivateKey, KeySet) {
	t.Helper()
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	priv := ed25519.NewKeyFromSeed(seed)
	raw, err := KeySetJSON([]KeyRecord{Record(testKeyID, priv.Public().(ed25519.PublicKey))})
	if err != nil {
		t.Fatal(err)
	}
	ks, err := ParseKeySet(raw)
	if err != nil {
		t.Fatal(err)
	}
	return priv, ks
}

func ptr(s string) *string { return &s }

func bodies() []Body {
	return []Body{
		{Object: ObjectToken, V: 1, ID: "lic_01jdaaaaaaaaaaaaaaaaaaaaaa", Plan: PlanReport, Features: V1PlanFeatures[PlanReport],
			KeySHA256: KeyHash("DG-7Q2M-TEST-0001"), Instance: "lki_test_0001", ValidatedAt: "2026-10-21T04:12:09.000Z",
			GraceDays: 14, PeriodEnd: ptr("2026-11-20T04:12:09.000Z"), Livemode: false},
		{Object: ObjectToken, V: 1, ID: "lic_01jdbbbbbbbbbbbbbbbbbbbbbb", Plan: PlanFounding, Features: V1PlanFeatures[PlanFounding],
			KeySHA256: KeyHash("DG-FOUND-TEST-0002"), Instance: "lki_test_0002", ValidatedAt: "2026-10-21T04:12:09.000Z",
			GraceDays: 14, PeriodEnd: ptr("2027-04-21T04:12:09.000Z"), Livemode: true},
		{Object: ObjectToken, V: 1, ID: "lic_01jdcccccccccccccccccccccc", Plan: PlanPro, Features: V1PlanFeatures[PlanPro],
			KeySHA256: KeyHash("DG-PRO-TEST-0003 é"), Instance: "lki_test_0003", ValidatedAt: "2026-10-21T04:12:09.000Z",
			GraceDays: 14, PeriodEnd: nil, Livemode: false},
		// S50: a client that sends features_known gets the plan's full list, undo_session included.
		{Object: ObjectToken, V: 1, ID: "lic_01jdddddddddddddddddddddda", Plan: PlanFounding, Features: NegotiateFeatures(PlanFounding, KnownFeatures(), true),
			KeySHA256: KeyHash("DG-FOUND-TEST-0004"), Instance: "lki_test_0004", ValidatedAt: "2026-10-21T04:12:09.000Z",
			GraceDays: 14, PeriodEnd: ptr("2027-04-21T04:12:09.000Z"), Livemode: true},
		{Object: ObjectToken, V: 1, ID: "lic_01jdeeeeeeeeeeeeeeeeeeeeee", Plan: PlanPro, Features: NegotiateFeatures(PlanPro, KnownFeatures(), true),
			KeySHA256: KeyHash("DG-PRO-TEST-0005"), Instance: "lki_test_0005", ValidatedAt: "2026-10-21T04:12:09.000Z",
			GraceDays: 14, PeriodEnd: nil, Livemode: false},
		// S52: the agency_monthly sku is plan pro plus agency, for a client that lists agency.
		{Object: ObjectToken, V: 1, ID: "lic_01jdffffffffffffffffffffff", Plan: PlanPro, Features: NegotiateSkuFeatures(SkuAgencyMonthly, PlanPro, KnownFeatures(), true),
			KeySHA256: KeyHash("DG-AGENCY-TEST-0006"), Instance: "lki_test_0006", ValidatedAt: "2026-10-21T04:12:09.000Z",
			GraceDays: 14, PeriodEnd: ptr("2026-11-21T04:12:09.000Z"), Livemode: false},
	}
}

type golden struct {
	Comment string          `json:"$comment"`
	KeyID   string          `json:"key_id"`
	SeedHex string          `json:"seed_hex"`
	KeySet  json.RawMessage `json:"key_set"`
	Tokens  []Token         `json:"tokens"`
}

func TestGoldenTokens(t *testing.T) {
	priv, _ := testSigner(t)
	var g golden
	g.Comment = "Licence token goldens (S48; S50 adds two negotiated tokens; S52 one agency_monthly token). Go signs these; the site's WebCrypto signer must produce the same body_hash and sig. Tokens 1-3 carry the v1 lists (a request without features_known), 4-5 the lists a client that sends features_known gets, 6 the agency_monthly sku (plan pro plus agency). Test key only."
	g.KeyID, g.SeedHex = testKeyID, strings.Repeat("07", 32)
	ksj, _ := KeySetJSON([]KeyRecord{Record(testKeyID, priv.Public().(ed25519.PublicKey))})
	g.KeySet = json.RawMessage(bytes.TrimSpace(ksj))
	for _, b := range bodies() {
		tok, err := Sign(testKeyID, priv, b)
		if err != nil {
			t.Fatal(err)
		}
		g.Tokens = append(g.Tokens, tok)
	}
	out, _ := json.MarshalIndent(g, "", "  ")
	out = append(out, '\n')
	path := filepath.Join("testdata", "tokens.golden.json")
	if *update {
		if err := os.WriteFile(path, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, out) {
		t.Fatal("tokens.golden.json is stale; run go test ./internal/licence -run TestGoldenTokens -update and copy it to site/tests/fixtures/commerce/")
	}
	// The site's copy, when the site checkout sits next to the product (the founder's layout).
	if site, err := os.ReadFile(filepath.Join("..", "..", "..", "site", "tests", "fixtures", "commerce", "tokens.golden.json")); err == nil && !bytes.Equal(site, want) {
		t.Error("site/tests/fixtures/commerce/tokens.golden.json differs from the Go golden; copy it over")
	}
}

func TestVerify(t *testing.T) {
	priv, ks := testSigner(t)
	for _, b := range bodies() {
		tok, err := Sign(testKeyID, priv, b)
		if err != nil {
			t.Fatal(err)
		}
		if err := tok.Verify(ks); err != nil {
			t.Fatalf("%s: %v", b.Plan, err)
		}
		raw, _ := tok.Marshal()
		if bytes.Contains(raw, []byte("DG-")) {
			t.Fatal("the token holds the licence key")
		}
		back, err := Parse(raw)
		if err != nil || back.Verify(ks) != nil {
			t.Fatalf("round trip: %v", err)
		}
		// One changed byte anywhere in a signed field fails.
		for _, mut := range []func(*Token){
			func(x *Token) {
				if x.Plan == PlanPro {
					x.Plan = PlanFounding
				} else {
					x.Plan = PlanPro
				}
			},
			func(x *Token) { x.GraceDays = 15 },
			func(x *Token) { x.Livemode = !x.Livemode },
			func(x *Token) { x.ValidatedAt = "2026-10-22T04:12:09.000Z" },
			func(x *Token) { x.Features = append([]string{}, FeatureScanFull) },
			func(x *Token) { x.Sig = x.Sig[:20] + flip(x.Sig[20]) + x.Sig[21:] },
		} {
			c := tok
			c.Features = append([]string(nil), tok.Features...)
			mut(&c)
			if err := c.Verify(ks); !errors.Is(err, ErrSignature) {
				t.Errorf("%s: a changed token verified (%v)", b.Plan, err)
			}
		}
	}
}

func flip(c byte) string {
	if c == 'A' {
		return "B"
	}
	return "A"
}

func TestSealKeyNeverLicence(t *testing.T) {
	priv, ks := testSigner(t)
	if _, err := Sign("sk_01jdtestkey00000000000000a", priv, bodies()[0]); err == nil {
		t.Fatal("a token was signed with an sk_ key id")
	}
	raw := []byte(`{"object":"licence_key_set","keys":[{"id":"sk_01jdtestkey00000000000000a","alg":"Ed25519","public_key":"x","thumbprint":"y"}]}`)
	if _, err := ParseKeySet(raw); err == nil {
		t.Fatal("an sk_ key was accepted as a licence key")
	}
	tok, _ := Sign(testKeyID, priv, bodies()[0])
	tok.KeyID = "sk_01jdtestkey00000000000000a"
	if err := tok.Verify(ks); err == nil {
		t.Fatal("a token naming an sk_ key verified")
	}
}

func TestParseStrict(t *testing.T) {
	priv, _ := testSigner(t)
	tok, _ := Sign(testKeyID, priv, bodies()[0])
	raw, _ := tok.Marshal()
	for name, in := range map[string]string{
		"unknown field": strings.Replace(string(raw), `{"body_hash"`, `{"extra":1,"body_hash"`, 1),
		"duplicate":     strings.Replace(string(raw), `{"body_hash"`, `{"v":1,"body_hash"`, 1),
		"fraction":      strings.Replace(string(raw), `"grace_days":14`, `"grace_days":14.5`, 1),
		"not json":      "nope",
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestGrace(t *testing.T) {
	v := time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC)
	tok := Token{Body: Body{ValidatedAt: v.Format(TimeLayout), GraceDays: 14}}
	day := 24 * time.Hour
	cases := []struct {
		name   string
		now    time.Time
		period *time.Time
		live   bool
		active bool
		days   int
	}{
		{"checked today", v, nil, false, true, 0},
		{"+13 d", v.Add(13 * day), nil, false, true, 13},
		{"+14 d exactly", v.Add(14 * day), nil, false, true, 14},
		{"+15 d", v.Add(15 * day), nil, false, false, 15},
		{"period passed 10 d ago, checked today", v, tp(v.Add(-10 * day)), false, true, 0},
		{"period passed 15 d ago", v, tp(v.Add(-15 * day)), false, false, 0},
		{"live build, test token", v, nil, true, false, 0},
	}
	for _, c := range cases {
		x := tok
		if c.period != nil {
			x.PeriodEnd = ptr(c.period.Format(TimeLayout))
		}
		s := Evaluate(x, c.now, c.live)
		if s.Active != c.active || s.DaysSince != c.days {
			t.Errorf("%s: active %v days %d, want %v %d", c.name, s.Active, s.DaysSince, c.active, c.days)
		}
	}
	if !Evaluate(tok, v.Add(25*time.Hour), false).DueRefresh || Evaluate(tok, v.Add(23*time.Hour), false).DueRefresh {
		t.Error("refresh is due after 24 h, not before")
	}
}

func tp(t time.Time) *time.Time { return &t }

func TestFiles(t *testing.T) {
	priv, _ := testSigner(t)
	tok, _ := Sign(testKeyID, priv, bodies()[0])
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "licence.json")
	if _, err := LoadToken(path); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("missing: %v", err)
	}
	if err := SaveToken(path, tok); err != nil {
		t.Fatal(err)
	}
	if err := SaveKey(path, "DG-7Q2M-TEST-0001"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, KeyFileFor(path)} {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", p, fi.Mode().Perm())
		}
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
	if k, err := LoadKey(path); err != nil || k != "DG-7Q2M-TEST-0001" {
		t.Fatalf("key %q %v", k, err)
	}
	os.Chmod(path, 0o644)
	var ue *UnsafeFileError
	if _, err := LoadToken(path); !errors.As(err, &ue) {
		t.Fatalf("0644 accepted: %v", err)
	}
	os.Chmod(path, 0o600)
	link := filepath.Join(dir, "link.json")
	os.Symlink(path, link)
	if _, err := LoadToken(link); !errors.As(err, &ue) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(KeyFileFor(path)); !os.IsNotExist(err) {
		t.Fatal("the key file survived Remove")
	}
}

// fakeServices is Dodo's licence routes and our token route on one httptest server.
type fakeServices struct {
	priv       ed25519.PrivateKey
	valid      atomic.Bool
	activateSt int
	calls      atomic.Int32
	now        time.Time
}

func (f *fakeServices) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /licenses/activate", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.activateSt != 0 {
			w.WriteHeader(f.activateSt)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "lki_fake_01"})
	})
	mux.HandleFunc("POST /licenses/validate", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		json.NewEncoder(w).Encode(map[string]bool{"valid": f.valid.Load()})
	})
	mux.HandleFunc("POST /licenses/deactivate", func(w http.ResponseWriter, r *http.Request) { f.calls.Add(1) })
	mux.HandleFunc("POST /api/licence/token", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var in struct {
			License_key, Instance string
			FeaturesKnown         *[]string `json:"features_known"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.FeaturesKnown == nil || !slices.Equal(*in.FeaturesKnown, KnownFeatures()) {
			w.WriteHeader(http.StatusBadRequest) // S50: every request names the features this build knows
			return
		}
		if !f.valid.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		b := bodies()[0]
		b.KeySHA256, b.Instance, b.ValidatedAt = KeyHash(in.License_key), in.Instance, f.now.Format(TimeLayout)
		b.PeriodEnd = ptr(f.now.Add(30 * 24 * time.Hour).Format(TimeLayout))
		tok, _ := Sign(testKeyID, f.priv, b)
		raw, _ := tok.Marshal()
		w.Write(raw)
	})
	return mux
}

func newManager(t *testing.T) (*Manager, *fakeServices, *time.Time) {
	priv, ks := testSigner(t)
	now := time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC)
	f := &fakeServices{priv: priv, now: now}
	f.valid.Store(true)
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	m := &Manager{Path: filepath.Join(t.TempDir(), "licence.json"), Keys: ks, Now: func() time.Time { return now },
		Client: &Client{HTTP: srv.Client(), DodoBase: srv.URL, Server: srv.URL}}
	return m, f, &now
}

func TestManagerFlow(t *testing.T) {
	m, f, now := newManager(t)
	ctx := context.Background()
	l, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "my-laptop")
	if err != nil {
		t.Fatal(err)
	}
	if l.Token.Plan != PlanReport || !l.State.Active || l.Token.Instance != "lki_fake_01" {
		t.Fatalf("activate: %+v", l)
	}
	raw, _ := os.ReadFile(m.Path)
	if bytes.Contains(raw, []byte("DG-7Q2M")) {
		t.Fatal("licence.json holds the key")
	}
	// Offline show: no request.
	m.Client.NoNetwork = true
	before := f.calls.Load()
	if _, err := m.Load(); err != nil || f.calls.Load() != before {
		t.Fatalf("offline load: %v, %d requests", err, f.calls.Load()-before)
	}
	// Require at +2 d with the network off: allowed, no refresh.
	*now = now.Add(48 * time.Hour)
	if d := m.Require(ctx, FeatureScanFull); !d.Allowed || d.Refreshed {
		t.Fatalf("require offline: %+v", d)
	}
	// Twice within 24 h with the network on: one refresh.
	m.Client.NoNetwork = false
	f.now = *now
	if d := m.Require(ctx, FeatureScanFull); !d.Allowed || !d.Refreshed {
		t.Fatalf("require refresh: %+v", d)
	}
	before = f.calls.Load()
	if d := m.Require(ctx, FeatureScanFull); !d.Allowed || d.Refreshed || f.calls.Load() != before {
		t.Fatal("refreshed twice within 24 h")
	}
	// +15 d with no refresh possible: paused.
	m.Client.NoNetwork = true
	*now = now.Add(15 * 24 * time.Hour)
	if d := m.Require(ctx, FeatureScanFull); d.Allowed || d.Local.State.DaysSince != 15 {
		t.Fatalf("+15 d: %+v", d)
	}
	// A refund: Dodo says invalid, Pro off at once and the files go.
	m.Client.NoNetwork = false
	*now = now.Add(-14 * 24 * time.Hour)
	f.valid.Store(false)
	if _, err := m.Refresh(ctx); !errors.Is(err, ErrKeyInactive) {
		t.Fatalf("refresh after refund: %v", err)
	}
	if _, err := m.Load(); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("licence kept after refund: %v", err)
	}
	if d := m.Require(ctx, FeatureScanFull); d.Allowed {
		t.Fatal("Pro on after refund")
	}
}

func TestActivateErrors(t *testing.T) {
	for st, want := range map[int]error{404: ErrKeyUnknown, 403: ErrKeyInactive, 422: ErrKeyLimit, 503: ErrUnreachable} {
		m, f, _ := newManager(t)
		f.activateSt = st
		if _, err := m.Activate(context.Background(), "DG-7Q2M-TEST-0001", "h"); !errors.Is(err, want) {
			t.Errorf("%d: %v", st, err)
		}
		if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
			t.Errorf("%d: a token file was written", st)
		}
	}
	m, _, _ := newManager(t)
	m.Client.NoNetwork = true
	if _, err := m.Activate(context.Background(), "DG-7Q2M-TEST-0001", "h"); !errors.Is(err, ErrNetworkOff) {
		t.Errorf("no network: %v", err)
	}
	if _, err := m.Activate(context.Background(), "bad key!", "h"); !errors.Is(err, ErrKeyUnknown) {
		t.Errorf("bad shape: %v", err)
	}
}

func TestModeMismatch(t *testing.T) {
	m, _, _ := newManager(t)
	m.Live = true // the fake signs test-mode tokens
	if _, err := m.Activate(context.Background(), "DG-7Q2M-TEST-0001", "h"); !errors.Is(err, ErrMode) {
		t.Fatalf("live build took a test token: %v", err)
	}
}

func TestEmbeddedKeySetParses(t *testing.T) {
	if _, err := Embedded(); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceNameHidesHostname(t *testing.T) {
	n := InstanceName("arjuns-macbook.local")
	if len(n) != 8 || strings.Contains(n, "arjun") {
		t.Fatalf("instance name %q", n)
	}
}

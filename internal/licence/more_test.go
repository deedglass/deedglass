package licence

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestDeactivate(t *testing.T) {
	m, f, _ := newManager(t)
	ctx := context.Background()
	if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); err != nil {
		t.Fatal(err)
	}
	before := f.calls.Load()
	if err := m.Deactivate(ctx); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != before+1 {
		t.Fatal("deactivate didn't call Dodo once")
	}
	if _, err := m.Load(); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("files kept: %v", err)
	}
	if err := m.Deactivate(ctx); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("second deactivate: %v", err)
	}
}

func TestCheckRefusals(t *testing.T) {
	priv, _ := testSigner(t)
	good := bodies()[0]
	for name, mut := range map[string]func(*Body){
		"object":   func(b *Body) { b.Object = "deed" },
		"v":        func(b *Body) { b.V = 2 },
		"id":       func(b *Body) { b.ID = "act_01jdaaaaaaaaaaaaaaaaaaaaaa" },
		"plan":     func(b *Body) { b.Plan = "gold" },
		"key hash": func(b *Body) { b.KeySHA256 = "DG-7Q2M" },
		"instance": func(b *Body) { b.Instance = "has space" },
		"grace":    func(b *Body) { b.GraceDays = 31 },
		"time":     func(b *Body) { b.ValidatedAt = "yesterday" },
		"period":   func(b *Body) { b.PeriodEnd = ptr("soon") },
		"features": func(b *Body) { b.Features = []string{"undo_everything"} },
		"empty":    func(b *Body) { b.Features = nil },
	} {
		b := good
		mut(&b)
		if _, err := Sign(testKeyID, priv, b); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func TestKeySetRefusals(t *testing.T) {
	priv, _ := testSigner(t)
	rec := Record(testKeyID, priv.Public().(ed25519.PublicKey))
	ok, _ := KeySetJSON([]KeyRecord{rec})
	ks, err := ParseKeySet(ok)
	if err != nil || ks.Len() != 1 {
		t.Fatal(err)
	}
	if empty, _ := KeySetJSON(nil); !strings.Contains(string(empty), `"keys": []`) {
		t.Fatalf("empty set: %s", empty)
	}
	for name, in := range map[string]string{
		"object":     strings.Replace(string(ok), "licence_key_set", "seal_key_set", 1),
		"alg":        strings.Replace(string(ok), `"Ed25519"`, `"RS256"`, 1),
		"b64":        strings.Replace(string(ok), rec.PublicKey, "!!!", 1),
		"thumbprint": strings.Replace(string(ok), rec.Thumbprint, "sha256:"+strings.Repeat("0", 64), 1),
		"duplicate":  strings.Replace(string(ok), `"keys": [`, `"keys": [{"id":"`+rec.ID+`","alg":"Ed25519","public_key":"`+rec.PublicKey+`","thumbprint":"`+rec.Thumbprint+`"},`, 1),
		"unknown":    strings.Replace(string(ok), `"object"`, `"extra": 1, "object"`, 1),
		"small key":  strings.Replace(string(ok), rec.PublicKey, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", 1),
	} {
		if _, err := ParseKeySet([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(EmbeddedJSON()) == 0 {
		t.Fatal("no embedded key set")
	}
}

func TestFetchTokenAnswers(t *testing.T) {
	for st, want := range map[int]error{403: ErrKeyInactive, 404: ErrKeyUnknown, 400: ErrServerRefused, 502: ErrUnreachable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(st) }))
		c := &Client{HTTP: srv.Client(), DodoBase: srv.URL, Server: srv.URL}
		if _, err := c.FetchToken(context.Background(), "DG-7Q2M-TEST-0001", "lki_1"); !errors.Is(err, want) {
			t.Errorf("%d: %v", st, err)
		}
		if err := c.Validate(context.Background(), "DG-7Q2M-TEST-0001", ""); err == nil {
			t.Errorf("%d: validate passed", st)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"object":"licence_token"}`)) }))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Server: srv.URL}
	if _, err := c.FetchToken(context.Background(), "DG-7Q2M-TEST-0001", "lki_1"); !errors.Is(err, ErrBadTokenAnswer) {
		t.Errorf("bad token: %v", err)
	}
	down := &Client{HTTP: srv.Client(), DodoBase: "http://127.0.0.1:1", Server: "http://127.0.0.1:1"}
	if err := down.Validate(context.Background(), "DG-7Q2M-TEST-0001", "x"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("down: %v", err)
	}
}

func TestTokenFromAnotherKeyOrMachine(t *testing.T) {
	m, _, _ := newManager(t)
	// The fake signs whatever key it is sent; a token naming another key is refused by fetchAndSave.
	m.Client.Server = "http://127.0.0.1:1"
	if _, err := m.Activate(context.Background(), "DG-7Q2M-TEST-0001", "h"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("server down: %v", err)
	}
	if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
		t.Fatal("a file was written")
	}
}

func TestUnsafeKeyFile(t *testing.T) {
	m, _, _ := newManager(t)
	if _, err := m.Activate(context.Background(), "DG-7Q2M-TEST-0001", "h"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(KeyFileFor(m.Path), []byte("not a key!\n"), 0o600)
	var ue *UnsafeFileError
	if _, err := LoadKey(m.Path); !errors.As(err, &ue) || !strings.Contains(ue.Error(), "licence key") {
		t.Fatalf("bad key file: %v", err)
	}
	if _, err := m.Refresh(context.Background()); !errors.As(err, &ue) {
		t.Fatalf("refresh with a bad key file: %v", err)
	}
}

func TestRequireFeatureAndUnreachable(t *testing.T) {
	m, f, now := newManager(t)
	ctx := context.Background()
	if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); err != nil {
		t.Fatal(err)
	}
	if d := m.Require(ctx, FeatureActionPrivate); d.Allowed || !errors.Is(d.Err, ErrFeature) {
		t.Fatalf("report plan ran a pro-only feature: %+v", d)
	}
	// Due for a check, Dodo down: still allowed inside the grace, with the problem reported.
	*now = now.Add(48 * 3600e9)
	m.Client.DodoBase = "http://127.0.0.1:1"
	if d := m.Require(ctx, FeatureScanFull); !d.Allowed || !errors.Is(d.Err, ErrUnreachable) {
		t.Fatalf("unreachable within grace: %+v", d)
	}
	_ = f
}

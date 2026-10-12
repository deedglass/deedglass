package licence

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// S47B: the edges the main flow doesn't reach (files, Dodo answers, refresh and deactivate
// failures), so internal/licence stays above 95% coverage.

func TestPrivateFileEdges(t *testing.T) {
	dir := t.TempDir()
	// A directory where the file should be.
	sub := filepath.Join(dir, "adir")
	os.Mkdir(sub, 0o700)
	var ue *UnsafeFileError
	if _, err := readPrivate(sub); !errors.As(err, &ue) || !strings.Contains(ue.Why, "not a regular file") {
		t.Fatalf("directory: %v", err)
	}
	// Too large.
	big := filepath.Join(dir, "big.json")
	os.WriteFile(big, make([]byte, 64<<10+1), 0o600)
	if _, err := readPrivate(big); !errors.As(err, &ue) || !strings.Contains(ue.Why, "64 KiB") {
		t.Fatalf("large: %v", err)
	}
	// A path through a regular file: a plain error, not ErrNoLicence.
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0o600)
	if _, err := readPrivate(filepath.Join(file, "licence.json")); err == nil || errors.Is(err, ErrNoLicence) {
		t.Fatalf("through a file: %v", err)
	}
	// writePrivate: can't create the directory, refuses a symlink, can't replace a directory.
	if err := writePrivate(filepath.Join(file, "x", "licence.json"), []byte("{}")); err == nil {
		t.Fatal("wrote under a regular file")
	}
	link := filepath.Join(dir, "link.json")
	os.Symlink(big, link)
	if err := writePrivate(link, []byte("{}")); !errors.As(err, &ue) || ue.Why != "is a symlink" {
		t.Fatalf("symlink: %v", err)
	}
	if err := writePrivate(sub, []byte("{}")); err == nil {
		t.Fatal("replaced a directory")
	}
	ro := filepath.Join(dir, "ro")
	os.Mkdir(ro, 0o500)
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	if err := writePrivate(filepath.Join(ro, "licence.json"), []byte("{}")); err == nil && os.Geteuid() != 0 {
		t.Fatal("wrote into a read-only directory")
	}
	// SaveToken passes write errors up; Remove reports what it couldn't delete.
	if err := SaveToken(filepath.Join(file, "licence.json"), Token{}); err == nil {
		t.Fatal("SaveToken under a file")
	}
	nonEmpty := filepath.Join(dir, "full.json")
	os.Mkdir(nonEmpty, 0o700)
	os.WriteFile(filepath.Join(nonEmpty, "x"), nil, 0o600)
	if err := Remove(nonEmpty); err == nil {
		t.Fatal("Remove hid an error")
	}
	// LoadKey: missing, and a key file that doesn't hold a key.
	tok := filepath.Join(dir, "k", "licence.json")
	if _, err := LoadKey(tok); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("missing key: %v", err)
	}
	os.MkdirAll(filepath.Dir(tok), 0o700)
	os.WriteFile(KeyFileFor(tok), []byte("not a key!\n"), 0o600)
	if _, err := LoadKey(tok); !errors.As(err, &ue) {
		t.Fatalf("bad key: %v", err)
	}
	for k, want := range map[string]bool{"short": false, strings.Repeat("A", 129): false, "DG-7Q2M_TEST": false, "DG-7Q2M-TEST-0001": true} {
		if ValidKey(k) != want {
			t.Errorf("ValidKey(%q) = %v", k, !want)
		}
	}
}

func TestKeyHelpers(t *testing.T) {
	_, ks := testSigner(t)
	if _, ok := ks.lookup("sk_01jdtestkey00000000000000a"); ok {
		t.Fatal("a seal key id was looked up")
	}
	if _, ok := ks.lookup("lk_01jdotherkey0000000000000"); ok {
		t.Fatal("an unknown key was found")
	}
	b, err := KeySetJSON(nil)
	if err != nil || !strings.Contains(string(b), `"keys": []`) {
		t.Fatalf("empty set: %s %v", b, err)
	}
	if len(EmbeddedJSON()) == 0 {
		t.Fatal("no embedded key set")
	}
	if _, err := BodyHash(Body{GraceDays: -1}); err == nil {
		t.Fatal("a negative number hashed")
	}
}

func TestVerifyRefusalsEdges(t *testing.T) {
	priv, ks := testSigner(t)
	tok, err := Sign(testKeyID, priv, bodies()[0])
	if err != nil {
		t.Fatal(err)
	}
	bad := tok
	bad.BodyHash = "sha256:" + strings.Repeat("0", 64)
	if err := bad.Verify(ks); err == nil {
		t.Error("a wrong body hash verified")
	}
	bad = tok
	bad.Sig = "ed25519:short"
	if err := bad.Verify(ks); !errors.Is(err, ErrSignature) {
		t.Errorf("bad sig: %v", err)
	}
	other := ed25519.NewKeyFromSeed(make([]byte, 32))
	if t2, err := Sign(testKeyID, other, bodies()[0]); err == nil {
		if err := t2.Verify(ks); !errors.Is(err, ErrSignature) {
			t.Errorf("wrong key: %v", err)
		}
	}
	if err := tok.Verify(KeySet{}); !errors.Is(err, ErrSignature) {
		t.Errorf("empty set: %v", err)
	}
}

// dodo is a scripted stand-in for Dodo and the token route.
type dodo struct {
	validate, token, deactivate int // status codes; 0 means 200 with a good answer
	valid                       bool
	body                        string // a raw body for every route, when set
	priv                        ed25519.PrivateKey
	keyID                       string
	livemode                    bool
	now                         time.Time
}

func (d *dodo) server(t *testing.T) *httptest.Server {
	write := func(w http.ResponseWriter, st int, v any) bool {
		if d.body != "" {
			w.Write([]byte(d.body))
			return true
		}
		if st != 0 {
			w.WriteHeader(st)
			return true
		}
		if v != nil {
			json.NewEncoder(w).Encode(v)
		}
		return false
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /licenses/activate", func(w http.ResponseWriter, r *http.Request) {
		write(w, 0, map[string]string{"id": "lki_fake_01"})
	})
	mux.HandleFunc("POST /licenses/validate", func(w http.ResponseWriter, r *http.Request) {
		write(w, d.validate, map[string]bool{"valid": d.valid})
	})
	mux.HandleFunc("POST /licenses/deactivate", func(w http.ResponseWriter, r *http.Request) { write(w, d.deactivate, nil) })
	mux.HandleFunc("POST /api/licence/token", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ License_key, Instance string }
		json.NewDecoder(r.Body).Decode(&in)
		if d.body != "" || d.token != 0 {
			write(w, d.token, nil)
			return
		}
		b := bodies()[0]
		b.KeySHA256, b.Instance, b.ValidatedAt, b.Livemode = KeyHash(in.License_key), in.Instance, d.now.Format(TimeLayout), d.livemode
		b.PeriodEnd = ptr(d.now.Add(30 * 24 * time.Hour).Format(TimeLayout))
		id := d.keyID
		if id == "" {
			id = testKeyID
		}
		tok, _ := Sign(id, d.priv, b)
		raw, _ := tok.Marshal()
		w.Write(raw)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func dodoManager(t *testing.T, d *dodo) *Manager {
	priv, ks := testSigner(t)
	d.priv, d.valid = priv, true
	d.now = time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC)
	srv := d.server(t)
	return &Manager{Path: filepath.Join(t.TempDir(), "licence.json"), Keys: ks, Now: func() time.Time { return d.now },
		Client: &Client{HTTP: srv.Client(), DodoBase: srv.URL, Server: srv.URL}}
}

func TestClientAnswers(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		d    dodo
		call func(c *Client) error
		want error
	}{
		"deactivate 404 is fine":    {dodo{deactivate: 404}, func(c *Client) error { return c.Deactivate(ctx, "DG-7Q2M-TEST-0001", "lki_1") }, nil},
		"deactivate 403":            {dodo{deactivate: 403}, func(c *Client) error { return c.Deactivate(ctx, "DG-7Q2M-TEST-0001", "lki_1") }, ErrKeyInactive},
		"deactivate 500":            {dodo{deactivate: 500}, func(c *Client) error { return c.Deactivate(ctx, "DG-7Q2M-TEST-0001", "lki_1") }, ErrUnreachable},
		"deactivate 409":            {dodo{deactivate: 409}, func(c *Client) error { return c.Deactivate(ctx, "DG-7Q2M-TEST-0001", "lki_1") }, ErrUnreachable},
		"validate 422":              {dodo{validate: 422}, func(c *Client) error { return c.Validate(ctx, "DG-7Q2M-TEST-0001", "") }, ErrKeyLimit},
		"an answer that isn't JSON": {dodo{body: "<html>"}, func(c *Client) error { return c.Validate(ctx, "DG-7Q2M-TEST-0001", "") }, ErrUnreachable},
		"a huge answer": {dodo{body: `{"valid":true,"pad":"` + strings.Repeat("x", maxResponseBody) + `"}`},
			func(c *Client) error { return c.Validate(ctx, "DG-7Q2M-TEST-0001", "") }, ErrUnreachable},
	} {
		t.Run(name, func(t *testing.T) {
			d := tc.d
			m := dodoManager(t, &d)
			d.valid = true
			err := tc.call(m.Client)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	off := &Client{NoNetwork: true}
	if err := off.Deactivate(ctx, "k", "i"); !errors.Is(err, ErrNetworkOff) {
		t.Fatalf("network off: %v", err)
	}
	down := &Client{HTTP: http.DefaultClient, DodoBase: "http://127.0.0.1:1"}
	if err := down.Deactivate(ctx, "k", "i"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("down: %v", err)
	}
	bad := &Client{HTTP: http.DefaultClient, DodoBase: "http://[::1"}
	if err := bad.Validate(ctx, "k", ""); err == nil {
		t.Fatal("a bad URL was requested")
	}
}

func TestRefreshEdges(t *testing.T) {
	ctx := context.Background()
	activate := func(t *testing.T, d *dodo) *Manager {
		m := dodoManager(t, d)
		if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); err != nil {
			t.Fatal(err)
		}
		return m
	}
	// No key file: the old licence comes back with the error.
	d := &dodo{}
	m := activate(t, d)
	os.Remove(KeyFileFor(m.Path))
	if old, err := m.Refresh(ctx); !errors.Is(err, ErrNoLicence) || old.Token.ID == "" {
		t.Fatalf("no key: %v", err)
	}
	// Dodo says the key is inactive at validate: the licence is removed.
	d = &dodo{}
	m = activate(t, d)
	d.valid = false
	if _, err := m.Refresh(ctx); !errors.Is(err, ErrKeyInactive) {
		t.Fatalf("inactive: %v", err)
	}
	if _, err := m.Load(); !errors.Is(err, ErrNoLicence) {
		t.Fatal("an inactive key's licence was kept")
	}
	// Validate passes, the token route says unknown: removed too.
	d = &dodo{}
	m = activate(t, d)
	d.token = http.StatusNotFound
	if _, err := m.Refresh(ctx); !errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("token 404: %v", err)
	}
	if _, err := m.Load(); !errors.Is(err, ErrNoLicence) {
		t.Fatal("an unknown key's licence was kept")
	}
	// The token route is down: the old token stays.
	d = &dodo{}
	m = activate(t, d)
	d.token = http.StatusBadGateway
	if _, err := m.Refresh(ctx); err == nil {
		t.Fatal("a refused token refreshed")
	}
	if _, err := m.Load(); err != nil {
		t.Fatalf("the old token was lost: %v", err)
	}
	// Refresh with no licence at all.
	if _, err := dodoManager(t, &dodo{}).Refresh(ctx); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("no licence: %v", err)
	}
}

func TestFetchAndSaveEdges(t *testing.T) {
	ctx := context.Background()
	// A token signed by a key this build doesn't trust.
	d := &dodo{}
	m := dodoManager(t, d)
	d.priv = ed25519.NewKeyFromSeed(make([]byte, 32))
	if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); !errors.Is(err, ErrBadTokenAnswer) {
		t.Fatalf("untrusted token: %v", err)
	}
	// A live token on a test build.
	d = &dodo{livemode: true}
	m = dodoManager(t, d)
	if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); !errors.Is(err, ErrMode) {
		t.Fatalf("mode: %v", err)
	}
	// Somewhere it can't write.
	d = &dodo{}
	m = dodoManager(t, d)
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	m.Path = filepath.Join(file, "licence.json")
	if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); err == nil {
		t.Fatal("saved under a regular file")
	}
	// A key that isn't a key never reaches Dodo.
	if _, err := m.Activate(ctx, "no", "h"); !errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("bad key: %v", err)
	}
}

func TestLoadEdges(t *testing.T) {
	ctx := context.Background()
	d := &dodo{}
	m := dodoManager(t, d)
	if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); err != nil {
		t.Fatal(err)
	}
	// The same file on a live build: the mode doesn't match.
	live := *m
	live.Live = true
	if l, err := live.Load(); !errors.Is(err, ErrMode) || !l.State.ModeMismatch {
		t.Fatalf("live build: %v", err)
	}
	// A key set without the signing key: the token doesn't verify.
	other := *m
	other.Keys = KeySet{}
	if _, err := other.Load(); !errors.Is(err, ErrSignature) {
		t.Fatalf("no keys: %v", err)
	}
}

func TestDeactivateEdges(t *testing.T) {
	ctx := context.Background()
	activate := func(d *dodo) *Manager {
		m := dodoManager(t, d)
		if _, err := m.Activate(ctx, "DG-7Q2M-TEST-0001", "h"); err != nil {
			t.Fatal(err)
		}
		return m
	}
	// No key file.
	m := activate(&dodo{})
	os.Remove(KeyFileFor(m.Path))
	if err := m.Deactivate(ctx); !errors.Is(err, ErrNoLicence) {
		t.Fatalf("no key: %v", err)
	}
	// Dodo unreachable: the files stay so the person can try again.
	m = activate(&dodo{})
	m.Client.NoNetwork = true
	if err := m.Deactivate(ctx); !errors.Is(err, ErrNetworkOff) {
		t.Fatalf("offline: %v", err)
	}
	if _, err := m.Load(); err != nil {
		t.Fatal("offline deactivate removed the licence")
	}
	// Dodo says the key is already inactive: the files go.
	d := &dodo{}
	m = activate(d)
	d.deactivate = http.StatusForbidden
	if err := m.Deactivate(ctx); err != nil {
		t.Fatalf("inactive: %v", err)
	}
	if _, err := m.Load(); !errors.Is(err, ErrNoLicence) {
		t.Fatal("the licence was kept")
	}
}

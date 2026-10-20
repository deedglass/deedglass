package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store"
)

// fakeKeys is the database side of keys rotate and keys list.
type fakeKeys struct {
	rows       []store.SealKeyRow
	rotateErr  error
	gotOverlap time.Duration
}

func (f *fakeKeys) SealKeys(context.Context) ([]store.SealKeyRow, error) { return f.rows, nil }
func (f *fakeKeys) Close()                                               {}
func (f *fakeKeys) RotateSealKey(_ context.Context, id string, pub []byte, overlap time.Duration) (store.SealKeyRow, []store.SealKeyRow, error) {
	if f.rotateErr != nil {
		return store.SealKeyRow{}, nil, f.rotateErr
	}
	f.gotOverlap = overlap
	now := keysNow().UTC().Truncate(time.Millisecond)
	retireAt := now.Add(overlap)
	var retired []store.SealKeyRow
	for i := range f.rows {
		if f.rows[i].Status == deed.KeyActive {
			f.rows[i].Status, f.rows[i].RetiredAt = deed.KeyRetired, &retireAt
			retired = append(retired, f.rows[i])
		}
	}
	n := store.SealKeyRow{ID: id, PublicKey: pub, Thumbprint: deed.Thumbprint(pub), Status: deed.KeyActive, ActivatedAt: now}
	f.rows = append(f.rows, n)
	return n, retired, nil
}

func (f *fakeKeys) AddPQSealKey(_ context.Context, id string, pub []byte, overlap time.Duration) (store.SealKeyRow, []store.SealKeyRow, error) {
	if f.rotateErr != nil {
		return store.SealKeyRow{}, nil, f.rotateErr
	}
	f.gotOverlap = overlap
	now := keysNow().UTC().Truncate(time.Millisecond)
	n := store.SealKeyRow{ID: id, Alg: deed.AlgMLDSA65, PublicKey: pub, Thumbprint: deed.Digest(pub), Status: deed.KeyActive, ActivatedAt: now.Add(overlap)}
	f.rows = append(f.rows, n)
	return n, nil, nil
}

// keysSeams fixes the clock, randomness and ids, and the database.
func keysSeams(t *testing.T, fk *fakeKeys) {
	t.Helper()
	savedNow, savedRand, savedID, savedOpen := keysNow, keysRand, newSealKeyID, openKeyStore
	n := 0
	keysNow = func() time.Time { return time.Date(2026, 10, 2, 9, 17, 14, 0, time.UTC) }
	keysRand = bytes.NewReader(bytes.Repeat([]byte{0x42}, 32*8))
	newSealKeyID = func() string { n++; return deedtest.ID("sk", 40+n) }
	openKeyStore = func(context.Context, config.Secret) (keyStore, error) {
		if fk == nil {
			return nil, errors.New("dial tcp 127.0.0.1:55432: connect: connection refused")
		}
		return fk, nil
	}
	t.Cleanup(func() { keysNow, keysRand, newSealKeyID, openKeyStore = savedNow, savedRand, savedID, savedOpen })
}

func twoKeys() *fakeKeys {
	a, b := deedtest.TestKey("a"), deedtest.TestKey("b")
	ar := time.Date(2026, 10, 2, 9, 22, 14, 0, time.UTC)
	return &fakeKeys{rows: []store.SealKeyRow{
		{ID: a.ID, PublicKey: a.Signer.PublicKey(), Thumbprint: deed.Thumbprint(a.Signer.PublicKey()), Status: deed.KeyRetired,
			ActivatedAt: time.Date(2026, 10, 1, 18, 40, 2, 0, time.UTC), RetiredAt: &ar},
		{ID: b.ID, PublicKey: b.Signer.PublicKey(), Thumbprint: deed.Thumbprint(b.Signer.PublicKey()), Status: deed.KeyActive,
			ActivatedAt: time.Date(2026, 10, 2, 9, 17, 14, 0, time.UTC)},
	}}
}

var dbEnv = map[string]string{"NO_COLOR": "1", "DEEDGLASS_DATABASE_URL": "postgres://deedglass_app:local-dev-password@127.0.0.1:55432/deedglass"}

func TestKeysGoldens(t *testing.T) {
	cases := []struct {
		name string
		fk   *fakeKeys
		env  map[string]string
		args []string
		code int
	}{
		{"keys_gen", nil, map[string]string{"NO_COLOR": "1"}, []string{"keys", "gen"}, 0},
		{"keys_gen_json", nil, nil, []string{"keys", "gen", "--json"}, 0},
		{"keys_list", twoKeys(), dbEnv, []string{"keys", "list"}, 0},
		{"keys_list_json", twoKeys(), dbEnv, []string{"keys", "list", "--json"}, 0},
		{"keys_rotate", &fakeKeys{rows: twoKeys().rows[1:]}, dbEnv, []string{"keys", "rotate"}, 0},
		{"keys_rotate_json", &fakeKeys{rows: twoKeys().rows[1:]}, dbEnv, []string{"keys", "rotate", "--overlap", "1m", "--json"}, 0},
		{"keys_rotate_failed", &fakeKeys{rows: twoKeys().rows[1:], rotateErr: errors.New("store: ERROR: permission denied for table seal_keys")}, dbEnv, []string{"keys", "rotate"}, 1},
		{"keys_rotate_pq", &fakeKeys{rows: twoKeys().rows[1:]}, dbEnv, []string{"keys", "rotate", "--alg", "ML-DSA-65"}, 0},
		{"keys_rotate_pq_json", &fakeKeys{rows: twoKeys().rows[1:]}, dbEnv, []string{"keys", "rotate", "--alg", "ML-DSA-65", "--json"}, 0},
		{"keys_rotate_bad_alg", twoKeys(), dbEnv, []string{"keys", "rotate", "--alg", "RSA"}, 2},
		{"keys_rotate_bad_overlap", twoKeys(), dbEnv, []string{"keys", "rotate", "--overlap", "200h"}, 2},
		{"keys_rotate_no_database", nil, map[string]string{"NO_COLOR": "1"}, []string{"keys", "rotate"}, 1},
		{"keys_list_unreachable", nil, dbEnv, []string{"keys", "list"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keysSeams(t, c.fk)
			r := cli(t, c.env, false, c.args...)
			if r.code != c.code {
				t.Fatalf("exit %d, want %d\n%s%s", r.code, c.code, r.stdout, r.stderr)
			}
			golden(t, c.name, r)
		})
	}
}

func TestKeysGenWritesA0600File(t *testing.T) {
	keysSeams(t, nil)
	dir := t.TempDir()
	var out bytes.Buffer
	e := &Env{Stdout: &out, Stderr: io.Discard, Getenv: func(string) string { return "" },
		LoadOptions: func() config.LoadOptions {
			return config.LoadOptions{Env: map[string]string{}, FS: fstest.MapFS{}, Dir: dir}
		}}
	if code := run(context.Background(), e, []string{"keys", "gen"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	p := filepath.Join(dir, ".deedglass/keys", deedtest.ID("sk", 41)+".json")
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, fi)
	}
	if di, _ := os.Stat(filepath.Dir(p)); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode())
	}
	s, err := deed.LoadKeyFile(p, deed.LoadOptions{Prod: true})
	if err != nil || s.KeyID() != deedtest.ID("sk", 41) {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	var kf map[string]any
	_ = json.Unmarshal(raw, &kf)
	if strings.Contains(out.String(), kf["seed"].(string)) {
		t.Fatal("the seed was printed")
	}
}

func TestKeysRotateRemovesFileWhenTheDatabaseRefuses(t *testing.T) {
	fk := &fakeKeys{rotateErr: errors.New("refused")}
	keysSeams(t, fk)
	r := cli(t, dbEnv, false, "keys", "rotate")
	if r.code != 1 {
		t.Fatalf("exit %d", r.code)
	}
	// cli() uses a fresh temp dir per run; the rotate must not leave a file there. Run it again
	// with a known directory to look.
	dir := t.TempDir()
	e := &Env{Stdout: io.Discard, Stderr: io.Discard, Getenv: func(k string) string { return dbEnv[k] },
		LoadOptions: func() config.LoadOptions { return config.LoadOptions{Env: dbEnv, FS: fstest.MapFS{}, Dir: dir} }}
	_ = run(context.Background(), e, []string{"keys", "rotate"})
	if m, _ := filepath.Glob(filepath.Join(dir, ".deedglass/keys/*.json")); len(m) != 0 {
		t.Fatalf("left %v", m)
	}
}

func TestKeysListFromServer(t *testing.T) {
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(api.WellKnownRoutes(func(context.Context) (*deed.KeySet, error) { return deedtest.KeySet(), nil }, nil)...)
	ts := httptest.NewServer(api)
	defer ts.Close()
	r := cli(t, map[string]string{"NO_COLOR": "1"}, false, "keys", "list", "--server", ts.URL)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	r.stdout = strings.ReplaceAll(r.stdout, ts.URL, "http://SERVER")
	golden(t, "keys_list_server", r)

	// A key set with a small-order key is refused, never listed.
	bad := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	bad.Mount(bad.WellKnownRoutes(func(context.Context) (*deed.KeySet, error) {
		ks := deedtest.KeySet()
		raw, _ := json.Marshal(ks)
		x := deedtest.SmallOrderKeys[0]
		raw = bytes.ReplaceAll(raw, []byte(ks.Keys[0].PublicKey), []byte(base64URL.EncodeToString(x.PublicKey())))
		raw = bytes.Replace(raw, []byte(ks.Keys[0].Thumbprint), []byte(deed.Thumbprint(x.PublicKey())), 1)
		var forged deed.KeySet
		_ = json.Unmarshal(raw, &forged)
		return &forged, nil
	}, nil)...)
	ts2 := httptest.NewServer(bad)
	defer ts2.Close()
	r = cli(t, map[string]string{"NO_COLOR": "1"}, false, "keys", "list", "--server", ts2.URL)
	if r.code != 1 || !strings.Contains(r.stderr, "The key set contains an unsafe key") {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if r := cli(t, nil, false, "keys", "list", "--server", "ftp://x"); r.code != 2 {
		t.Fatalf("bad url: %d", r.code)
	}
}

// Seal key refusals that happen before the database is touched.
func TestServeSealKeyRefusals(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, kf deed.KeyFile, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		b, _ := json.Marshal(kf)
		_ = os.WriteFile(p, b, mode)
		_ = os.Chmod(p, mode)
		return p
	}
	a := deedtest.TestKey("a")
	tk, _, _ := deed.NewKeyFile(a.ID, a.Seed, deedtest.T0)
	own, _, _ := deed.NewKeyFile(deedtest.ID("sk", 50), bytes.Repeat([]byte{5}, 32), deedtest.T0)
	small := own
	small.ID = deedtest.ID("sk", 51)
	small.PublicKey = base64URL.EncodeToString(deedtest.SmallOrderKeys[0].PublicKey())
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"serve_prod_no_seal_key", map[string]string{"DEEDGLASS_ENV": "prod"}},
		{"serve_prod_test_key", map[string]string{"DEEDGLASS_ENV": "prod", "DEEDGLASS_SEAL_KEY_FILE": write("test.json", tk, 0o600)}},
		{"serve_key_file_0644", map[string]string{"DEEDGLASS_SEAL_KEY_FILE": write("open.json", own, 0o644)}},
		{"serve_small_order_key", map[string]string{"DEEDGLASS_SEAL_KEY_FILE": write("small.json", small, 0o600)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{Env: config.Dev, SealKeyFile: c.env["DEEDGLASS_SEAL_KEY_FILE"]}
			if c.env["DEEDGLASS_ENV"] == "prod" {
				cfg.Env = config.Prod
			}
			var out, errb bytes.Buffer
			e := &Env{Stdout: &out, Stderr: &errb, Getenv: func(string) string { return "" }, Style: plainStyle(),
				LoadOptions: func() config.LoadOptions { return config.LoadOptions{Dir: dir} }}
			_, err := loadSealKey(context.Background(), e, cfg, nil, slog.New(slog.DiscardHandler))
			code := report(e, err)
			r := result{code, out.String(), strings.ReplaceAll(errb.String(), dir, "DIR")}
			if code != 1 {
				t.Fatalf("exit %d", code)
			}
			golden(t, c.name, r)
		})
	}
}

func plainStyle() ui.Style { return ui.Style{} }

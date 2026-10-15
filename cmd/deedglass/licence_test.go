package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/licence"
)

const cliLicKeyID = "lk_01jdtestkey00000000000000a"

// licFake is Dodo's licence routes and our token route for the CLI tests.
type licFake struct {
	priv     ed25519.PrivateKey
	valid    atomic.Bool
	activate int
	plan     string
	sku      string // S52: agency_monthly signs plan pro plus agency
	calls    atomic.Int32
	now      time.Time
}

func (f *licFake) serve(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /licenses/activate", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.activate != 0 {
			w.WriteHeader(f.activate)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": "lki_cli_01"})
	})
	mux.HandleFunc("POST /licenses/validate", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		json.NewEncoder(w).Encode(map[string]bool{"valid": f.valid.Load()})
	})
	mux.HandleFunc("POST /licenses/deactivate", func(w http.ResponseWriter, r *http.Request) { f.calls.Add(1) })
	mux.HandleFunc("POST /api/licence/token", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		var in struct {
			Key      string `json:"license_key"`
			Instance string `json:"instance"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if !f.valid.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		pe := f.now.Add(30 * 24 * time.Hour).Format(licence.TimeLayout)
		b := licence.Body{Object: licence.ObjectToken, V: 1, ID: "lic_01jdcccccccccccccccccccccc", Plan: f.plan, Features: licence.NegotiateSkuFeatures(f.sku, f.plan, licence.KnownFeatures(), true),
			KeySHA256: licence.KeyHash(in.Key), Instance: in.Instance, ValidatedAt: f.now.Format(licence.TimeLayout), GraceDays: 14, PeriodEnd: &pe}
		tok, err := licence.Sign(cliLicKeyID, f.priv, b)
		if err != nil {
			t.Error(err)
		}
		raw, _ := tok.Marshal()
		w.Write(raw)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// licenceSeams points the licence commands at the fake, with a fixed clock and the test key set.
func licenceSeams(t *testing.T) (*licFake, map[string]string, *time.Time) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	set, _ := licence.KeySetJSON([]licence.KeyRecord{licence.Record(cliLicKeyID, priv.Public().(ed25519.PublicKey))})
	now := time.Date(2026, 10, 21, 4, 12, 9, 0, time.UTC)
	f := &licFake{priv: priv, plan: licence.PlanReport, now: now}
	f.valid.Store(true)
	srv := f.serve(t)
	sk, sh, sn, shost := licenceKeys, licenceHTTP, licenceNow, licenceHostname
	licenceKeys = func(bool) (licence.KeySet, error) { return licence.ParseKeySet(set) }
	licenceHTTP = func() *http.Client { return srv.Client() }
	licenceNow = func() time.Time { return now }
	licenceHostname = func() (string, error) { return "arjuns-laptop.local", nil }
	t.Cleanup(func() { licenceKeys, licenceHTTP, licenceNow, licenceHostname = sk, sh, sn, shost })
	path := filepath.Join(t.TempDir(), "state", "licence.json")
	env := map[string]string{"NO_COLOR": "1", "DEEDGLASS_LICENCE_FILE": path, "DEEDGLASS_LICENCE_MODE": "test", "DEEDGLASS_LICENCE_SERVER": srv.URL}
	// The fake serves Dodo's routes too: point the Dodo base at it.
	saved, savedTM := licDodoOverride, licenceTestModeBuild
	licDodoOverride, licenceTestModeBuild = srv.URL, true // these tests stand in for a -tags licencetest build
	t.Cleanup(func() { licDodoOverride, licenceTestModeBuild = saved, savedTM })
	return f, env, &now
}

// TestLicenceTestModeReleaseBuild: a release binary (no licencetest tag) refuses
// DEEDGLASS_LICENCE_MODE=test, and a test-mode token signed by the real lk_ key (a free Dodo
// test-card purchase) never turns Pro on. Security pre-launch review.
func TestLicenceTestModeReleaseBuild(t *testing.T) {
	_, env, _ := licenceSeams(t)
	if r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001"); r.code != 0 {
		t.Fatalf("activate in a licencetest build: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	licenceTestModeBuild = false // from here on: the release binary
	for _, args := range [][]string{{"licence", "show"}, {"licence", "activate", "DG-7Q2M-TEST-0001"}, {"licence", "refresh"}} {
		r := cli(t, env, false, args...)
		if r.code != ExitUsage || !strings.Contains(r.stderr, "works only in a development build") {
			t.Fatalf("%v with DEEDGLASS_LICENCE_MODE=test in a release build: %d\n%s%s", args, r.code, r.stdout, r.stderr)
		}
	}
	delete(env, "DEEDGLASS_LICENCE_MODE")
	env["DEEDGLASS_NO_NETWORK"] = "1"
	r := cli(t, env, false, "licence", "show", "--json")
	if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "licence_mode") {
		t.Fatalf("a test-mode token on a release build must be refused: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	m, err := licenceManager(&Env{Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	if d := m.Require(context.Background(), licence.FeatureScanFull); d.Allowed {
		t.Fatal("a test-mode token unlocked scan_full on a release build")
	}
}

func TestLicenceCommands(t *testing.T) {
	f, env, now := licenceSeams(t)
	r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001")
	if r.code != 0 || !strings.Contains(r.stdout, "◐ Checking the key with Dodo Payments") ||
		!strings.Contains(r.stdout, "✓ Full scan report active · Pro features for 30 days · until 20 Nov 2026 · lic_01jdcccccccccccccccccccccc") {
		t.Fatalf("activate: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	raw, _ := os.ReadFile(env["DEEDGLASS_LICENCE_FILE"])
	if bytes.Contains(raw, []byte("DG-7Q2M")) {
		t.Fatal("licence.json holds the key")
	}
	if fi, _ := os.Stat(env["DEEDGLASS_LICENCE_FILE"]); fi.Mode().Perm() != 0o600 {
		t.Fatal("licence.json isn't 0600")
	}

	// Offline show makes no request.
	env["DEEDGLASS_NO_NETWORK"] = "1"
	before := f.calls.Load()
	r = cli(t, env, false, "licence", "show")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Full scan report · checked 0 days ago · offline until 4 Nov 2026") || f.calls.Load() != before {
		t.Fatalf("show: %d %d requests\n%s%s", r.code, f.calls.Load()-before, r.stdout, r.stderr)
	}
	r = cli(t, env, false, "licence", "show", "--json")
	var js map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &js); err != nil || js["active"] != true || js["plan"] != "report" {
		t.Fatalf("show --json: %s", r.stdout)
	}

	// Day +15 without a refresh: paused, with the exact line.
	*now = now.Add(15 * 24 * time.Hour)
	r = cli(t, env, false, "licence", "show")
	if !strings.Contains(r.stdout, "○ Pro paused: your licence was last checked 15 days ago. Run deedglass licence refresh. Everything free still works.") {
		t.Fatalf("paused: %s%s", r.stdout, r.stderr)
	}
	// Refresh with the network off says so.
	r = cli(t, env, false, "licence", "refresh")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "Network is off (DEEDGLASS_NO_NETWORK)") {
		t.Fatalf("refresh offline: %d %s%s", r.code, r.stdout, r.stderr)
	}

	// A refund: Dodo says invalid; refresh refuses and the licence is gone.
	delete(env, "DEEDGLASS_NO_NETWORK")
	*now = now.Add(-14 * 24 * time.Hour)
	f.valid.Store(false)
	r = cli(t, env, false, "licence", "refresh")
	if r.code != 1 || !strings.Contains(r.stderr, "This key is no longer active. It may have been refunded or cancelled") {
		t.Fatalf("refresh after refund: %d %s%s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, env, false, "licence", "show")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "No licence on this machine") {
		t.Fatalf("show after refund: %d %s%s", r.code, r.stdout, r.stderr)
	}
}

func TestLicenceActivateErrors(t *testing.T) {
	for st, want := range map[int]string{
		404: "Dodo Payments doesn't know this key",
		403: "This key is no longer active",
		422: "This key is active on 3 machines already",
	} {
		f, env, _ := licenceSeams(t)
		f.activate = st
		r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001")
		if r.code != 1 || !strings.Contains(r.stderr, want) {
			t.Errorf("%d: %d %s%s", st, r.code, r.stdout, r.stderr)
		}
	}
	_, env, _ := licenceSeams(t)
	if r := cli(t, env, false, "licence", "activate"); r.code != 2 {
		t.Errorf("no key: exit %d", r.code)
	}
}

func TestLicenceTamperedFile(t *testing.T) {
	_, env, _ := licenceSeams(t)
	if r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	p := env["DEEDGLASS_LICENCE_FILE"]
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, bytes.Replace(raw, []byte(`"grace_days":14`), []byte(`"grace_days":30`), 1), 0o600)
	r := cli(t, env, false, "licence", "show")
	if r.code != 1 || !strings.Contains(r.stderr, "✕ The licence file was changed after it was signed") {
		t.Fatalf("tampered: %d %s%s", r.code, r.stdout, r.stderr)
	}
}

func TestScanFullNeedsALicence(t *testing.T) {
	_, env, _ := licenceSeams(t)
	dir := t.TempDir()
	r := cli(t, env, false, "scan", "--dir", dir, "--out-dir", dir, "--full")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "The full report is part of Pro or the $29 full scan report. Get one at deedglass.com/pro. Everything in deedglass scan stays free") {
		t.Fatalf("no licence: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "deedglass-scan.md")); err != nil {
		t.Fatal("the free report wasn't written before the Pro refusal")
	}
	if _, err := os.Stat(filepath.Join(dir, "deedglass-full-report.html")); err == nil {
		t.Fatal("the full report was written without a licence")
	}
}

func TestScanFullWithALicence(t *testing.T) {
	_, env, now := licenceSeams(t)
	if r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	dir := t.TempDir()
	env["DEEDGLASS_NO_NETWORK"] = "1"
	r := cli(t, env, false, "scan", "--dir", dir, "--out-dir", dir, "--full")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ deedglass-full-report.html") || !strings.Contains(r.stdout, "Not sealed: no Deedglass server is configured") {
		t.Fatalf("full: %d %s%s", r.code, r.stdout, r.stderr)
	}
	html, err := os.ReadFile(filepath.Join(dir, "deedglass-full-report.html"))
	if err != nil || !bytes.Contains(html, []byte("Full scan report")) || bytes.Contains(html, []byte("<script")) {
		t.Fatalf("report: %v", err)
	}
	// +15 d with no refresh possible: read-only, the exact line, exit 1.
	*now = now.Add(15 * 24 * time.Hour)
	r = cli(t, env, false, "scan", "--dir", dir, "--out-dir", dir, "--full")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "Pro paused: your licence was last checked 15 days ago. Run deedglass licence refresh. Everything free still works") {
		t.Fatalf("+15 d: %d %s%s", r.code, r.stdout, r.stderr)
	}
}

// Free commands never open the licence: a corrupt licence file changes nothing for scan.
func TestFreeScanIgnoresACorruptLicence(t *testing.T) {
	_, env, _ := licenceSeams(t)
	p := env["DEEDGLASS_LICENCE_FILE"]
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("corrupt"), 0o644)
	dir := t.TempDir()
	if r := cli(t, env, false, "scan", "--dir", dir, "--out-dir", dir); r.code != 0 {
		t.Fatalf("free scan with a corrupt licence: %d %s", r.code, r.stderr)
	}
}

func TestKeysGenLicence(t *testing.T) {
	keysSeams(t, nil)
	dir := t.TempDir()
	r := cli(t, map[string]string{"NO_COLOR": "1"}, false, "keys", "gen", "--purpose", "licence", "--dir", dir, "--json")
	if r.code != 0 {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	var out struct {
		ID         string `json:"id"`
		PrivateJWK string `json:"private_jwk"`
		PublicSet  string `json:"public_set"`
	}
	json.Unmarshal([]byte(r.stdout), &out)
	if !strings.HasPrefix(out.ID, "lk_") {
		t.Fatalf("id %q", out.ID)
	}
	fi, err := os.Stat(out.PrivateJWK)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("private jwk: %v", err)
	}
	set, _ := os.ReadFile(out.PublicSet)
	ks, err := licence.ParseKeySet(set)
	if err != nil || ks.Len() != 1 || bytes.Contains(set, []byte(`"d"`)) {
		t.Fatalf("public set: %v %s", err, set)
	}
	if r := cli(t, nil, false, "keys", "gen", "--purpose", "nope"); r.code != 2 {
		t.Fatalf("bad purpose: exit %d", r.code)
	}
}

// TestLicenceClockBehind: a clock set back after use pauses Pro offline, and both show and the
// Pro refusal say why (security pre-launch M1).
func TestLicenceClockBehind(t *testing.T) {
	_, env, now := licenceSeams(t)
	if r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001"); r.code != 0 {
		t.Fatalf("activate: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	*now = now.Add(48 * time.Hour)
	if r := cli(t, env, false, "licence", "show"); r.code != 0 || !strings.Contains(r.stdout, "checked 2 days ago") {
		t.Fatalf("show two days later: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	*now = now.Add(-36 * time.Hour)
	env["DEEDGLASS_NO_NETWORK"] = "1"
	r := cli(t, env, false, "licence", "show")
	if !strings.Contains(r.stdout, "this computer's clock is behind the last time the licence was checked") {
		t.Fatalf("show with the clock behind: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	m, err := licenceManager(&Env{Getenv: func(k string) string { return env[k] }})
	if err != nil {
		t.Fatal(err)
	}
	d := m.Require(context.Background(), licence.FeatureScanFull)
	if d.Allowed {
		t.Fatal("Pro ran offline with the clock set back")
	}
	var ee *ExitError
	if err := proRefusal(d, licFullNeedsPro, ""); !errors.As(err, &ee) || ee.ErrCode != "licence_paused" || !strings.Contains(ee.Err.Error(), "Run deedglass licence refresh") {
		t.Fatalf("refusal: %v", err)
	}
}

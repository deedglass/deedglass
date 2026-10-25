package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/licence"
)

// DL-131, free while we grow: every formerly gated feature runs with no licence on the machine,
// and nothing opens the licence or calls Dodo Payments or deedglass.com.

// noLicenceCalls fails the test if anything builds the licence HTTP client, and points the licence
// file at a corrupt one so any read would fail loudly.
func noLicenceCalls(t *testing.T) map[string]string {
	t.Helper()
	if !licencePolicy.AllFree {
		t.Fatal("DL-131: this build must run every feature without a key")
	}
	saved := licenceHTTP
	licenceHTTP = func() *http.Client {
		t.Error("a free feature built the licence HTTP client")
		return &http.Client{}
	}
	t.Cleanup(func() { licenceHTTP = saved })
	p := filepath.Join(t.TempDir(), "state", "licence.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"NO_COLOR": "1", "DEEDGLASS_LICENCE_FILE": p}
}

func TestScanFullFreeWithoutAKey(t *testing.T) {
	env := noLicenceCalls(t)
	dir := t.TempDir()
	r := cli(t, env, false, "scan", "--dir", dir, "--out-dir", dir, "--full")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ deedglass-full-report.html") {
		t.Fatalf("scan --full with no licence: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "deedglass-full-report.html")); err != nil {
		t.Fatal("the full report wasn't written")
	}
}

func TestProPackFreeWithoutAKey(t *testing.T) {
	env := noLicenceCalls(t)
	dir := filepath.Join(t.TempDir(), "policies")
	r := cli(t, env, false, "policy", "install", "money-movement", "--dir", dir, "--yes")
	if r.code != 0 {
		t.Fatalf("a formerly Pro pack with no licence: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "money-movement.yaml")); err != nil {
		t.Fatalf("the pack wasn't written: %v", err)
	}
}

// The serve-side features (session undo, the security review pack, the Clients view) and the CLI's
// agency check all answer yes with no licence, and serve hands out the real engines.
func TestProFeaturesFreeWithoutAKey(t *testing.T) {
	env := noLicenceCalls(t)
	e := &Env{Getenv: func(k string) string { return env[k] }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, f := range licence.KnownFeatures() {
		if !licenceAllows(ctx, e, f) {
			t.Errorf("%s needs a key", f)
		}
	}
	if !undoSessionPro(ctx, e, nil).Allowed() {
		t.Error("session undo is off without a key")
	}
	if securityPackBuilder(ctx, e, nil)() == nil {
		t.Error("the security review pack's builder is off without a key")
	}
	if !agencyPro(ctx, e, nil).Allowed() {
		t.Error("the Clients view is off without a key")
	}
	if err := agencyAllowed(ctx, e); err != nil {
		t.Errorf("agency CLI check: %v", err)
	}
	if err := packsProCheck(ctx, e); err != nil {
		t.Errorf("packs check: %v", err)
	}
}

// The key path stays in place: KeyRequired refuses with no licence, without a price or a buy link.
func TestKeyRequiredStillRefuses(t *testing.T) {
	keyRequired(t)
	e := &Env{Getenv: func(k string) string {
		return map[string]string{"DEEDGLASS_NO_NETWORK": "1", "DEEDGLASS_LICENCE_FILE": filepath.Join(t.TempDir(), "licence.json")}[k]
	}}
	err := agencyAllowed(context.Background(), e)
	if err == nil || strings.Contains(err.Error(), "$") {
		t.Fatalf("KeyRequired agency: %v", err)
	}
	for _, s := range []string{licFullNeedsPro, licPacksNeedPro, agencyNeedsPro} {
		if strings.Contains(s, "$") || strings.Contains(s, "deedglass.com/pro") {
			t.Errorf("a refusal names a price or a buy link: %q", s)
		}
	}
}

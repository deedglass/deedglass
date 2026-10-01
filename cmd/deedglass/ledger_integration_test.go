//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// normalize replaces what changes between runs (ids, hashes, times, durations, the database
// address, cursors) so the Ledger commands' output can be compared with goldens.
var normalizers = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{20,}`), "CURSOR"},
	{regexp.MustCompile(`\b(org|agt|act|deed|sk|evt|adm|req|usr)_[0-7][0-9a-hjkmnp-tv-z]{25}\b`), "${1}_ID"},
	{regexp.MustCompile(`\b(org|agt|act|deed|sk|evt|adm)_[0-9a-z]{8}…`), "${1}_…"},
	{regexp.MustCompile(`sha256:[0-9a-f]{64}`), "sha256:HASH"},
	{regexp.MustCompile(`sha256:[0-9a-f]{4}…[0-9a-f]{4}`), "sha256:…"},
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z`), "TIME"},
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}(:\d{2}(\.\d{3})?)? UTC`), "TIME UTC"},
	{regexp.MustCompile(`\d{4}-\d{2}-\d{2}`), "DATE"},
	{regexp.MustCompile(`\b\d+[smhd] ago`), "AGO"},
	{regexp.MustCompile(`\b\d+\.\d\d s\b`), "N s"},
	{regexp.MustCompile(`"(duration_ms|bytes)": \d+`), `"$1": N`},
	{regexp.MustCompile(`127\.0\.0\.1:\d+|localhost:\d+`), "HOST"},
	{regexp.MustCompile(`deedglass_it_[0-9a-f]{8}`), "DB"},
	{regexp.MustCompile(`\d+(\.\d)? (kB|MB)`), "SIZE"},
	{regexp.MustCompile(`/[^ \n]*/(out\.(tar\.gz|jsonl))`), "DIR/$1"},
}

func normalize(s string) string {
	for _, n := range normalizers {
		s = n.re.ReplaceAllString(s, n.with)
	}
	return s
}

func ledgerGolden(t *testing.T, name string, r result) {
	t.Helper()
	got := normalize("exit " + strconv.Itoa(r.code) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr)
	path := filepath.Join("testdata", "ledger", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -tags integration ./cmd/deedglass -run TestLedgerCommandsLive -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s\n--- got\n%s--- want\n%s", name, path, got, want)
	}
}

// The Ledger commands against a throwaway database, with NO_COLOR, in text and --json.
func TestLedgerCommandsLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{})
	keyDir := t.TempDir()
	seed := make([]byte, 32)
	_, _ = rand.Read(seed)
	kf, _, err := deed.NewKeyFile(ids.New[ids.SealKeyKind]().String(), seed, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keyPath, err := deed.WriteKeyFile(keyDir, kf)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_ENV": "dev", "NO_COLOR": "1",
		"DEEDGLASS_SEAL_KEY_FILE": keyPath}
	out := t.TempDir()

	// Before any org exists.
	ledgerGolden(t, "actions_list_no_orgs", cli(t, env, false, "actions", "list"))
	// The guard.
	ledgerGolden(t, "fake_action_prod", cli(t, map[string]string{"DEEDGLASS_ENV": "prod"}, false, "dev", "fake-action"))

	r := cli(t, env, false, "dev", "fake-action", "--n", "50", "--seed", "42")
	ledgerGolden(t, "fake_action_50", r)
	if r.code != 0 || !strings.Contains(r.stdout, "chain intact") {
		t.Fatalf("%+v", r)
	}
	ledgerGolden(t, "actions_list", cli(t, env, false, "actions", "list", "--limit", "5"))
	ledgerGolden(t, "actions_list_mismatch", cli(t, env, false, "actions", "list", "--verdict", "MISMATCH", "--limit", "2"))
	ledgerGolden(t, "actions_list_bad_verdict", cli(t, env, false, "actions", "list", "--verdict", "FAILED"))
	r = cli(t, env, false, "--json", "actions", "list", "--verdict", "MISMATCH", "--limit", "1")
	ledgerGolden(t, "actions_list_json", r)
	var list struct {
		Data []struct {
			ID   string  `json:"id"`
			Deed *string `json:"deed"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list.Data) != 1 {
		t.Fatalf("%v %s", err, r.stdout)
	}
	ledgerGolden(t, "actions_show_mismatch", cli(t, env, false, "actions", "show", list.Data[0].ID))
	ledgerGolden(t, "actions_show_json", cli(t, env, false, "actions", "show", list.Data[0].ID, "--json"))
	ledgerGolden(t, "actions_show_unknown", cli(t, env, false, "actions", "show", "act_01j9zt3a5c7e9g1j3m5p7r9t1v"))
	ledgerGolden(t, "deeds_list", cli(t, env, false, "deeds", "list", "--limit", "3"))
	ledgerGolden(t, "deeds_show", cli(t, env, false, "deeds", "show", "3"))
	ledgerGolden(t, "ledger_check", cli(t, env, false, "ledger", "check"))
	ledgerGolden(t, "ledger_check_json", cli(t, env, false, "--json", "ledger", "check"))
	ledgerGolden(t, "deeds_export", cli(t, env, false, "deeds", "export", "--out", filepath.Join(out, "out.tar.gz")))
	ledgerGolden(t, "deeds_export_jsonl", cli(t, env, false, "deeds", "export", "--format", "jsonl", "--out", filepath.Join(out, "out.jsonl")))
	ledgerGolden(t, "deeds_export_empty", cli(t, env, false, "deeds", "export", "--from-seq", "5000", "--out", filepath.Join(out, "none.tar.gz")))
	if _, err := os.Stat(filepath.Join(out, "none.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("an empty export left a file")
	}
	// The exported bundle verifies with the CLI.
	keys := filepath.Join(out, "keys.json")
	ksJSON := liveKeySet(t, e)
	if err := os.WriteFile(keys, ksJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	r = cli(t, nil, false, "verify", filepath.Join(out, "out.tar.gz"), "--keys", keys)
	if r.code != 0 || !strings.Contains(r.stdout, "deeds · chain intact") {
		t.Fatalf("verify: %+v", r)
	}
	ledgerGolden(t, "ledger_prune_dry_run", cli(t, env, false, "ledger", "prune", "--older-than", "0s", "--dry-run"))
	ledgerGolden(t, "ledger_prune_needs_yes", cli(t, env, false, "ledger", "prune", "--older-than", "0s"))
	ledgerGolden(t, "ledger_prune", cli(t, env, false, "ledger", "prune", "--older-than", "0s", "--yes"))
	ledgerGolden(t, "ledger_check_after_prune", cli(t, env, false, "ledger", "check"))
	// Typed confirmation on a terminal.
	r = cliIn(t, []byte("org_wrong\n"), env, true, "ledger", "prune", "--older-than", "0s")
	ledgerGolden(t, "ledger_prune_wrong_org", r)

	// A second org: --org is now required.
	r = cli(t, env, false, "dev", "fake-action", "--n", "3", "--seed", "1", "--org-slug", "second")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	ledgerGolden(t, "actions_list_two_orgs", cli(t, env, false, "actions", "list"))
	if r := cli(t, env, false, "--org", "second", "ledger", "check"); r.code != 0 || !strings.Contains(r.stdout, "Chain intact") {
		t.Fatalf("%+v", r)
	}
}

func liveKeySet(t *testing.T, e *storetest.Env) []byte {
	rows, err := e.DB.SealKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var recs []deed.KeyRecord
	for _, r := range rows {
		recs = append(recs, r.Record())
	}
	ks, err := deed.NewKeySet("http://127.0.0.1:8080", recs)
	if err != nil {
		t.Fatal(err)
	}
	b, err := deed.StrictCanonical(ks)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

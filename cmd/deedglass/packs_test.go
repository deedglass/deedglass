package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/packs"
)

var packEnv = map[string]string{"NO_COLOR": "1"}

// exportPacks writes the embedded snapshot to a new directory, like a clone of policy-packs.
func exportPacks(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for p, b := range packs.Embedded().Files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// signedPacks generates a pack key, signs a copy of the snapshot with it and makes this build
// trust that key. It returns the signed copy's directory.
func signedPacks(t *testing.T) string {
	t.Helper()
	keysSeams(t, nil)
	keyDir := t.TempDir()
	r := cli(t, packEnv, false, "keys", "gen", "--purpose", "packs", "--dir", keyDir, "--json")
	if r.code != 0 {
		t.Fatalf("keys gen: %d %s", r.code, r.stderr)
	}
	var out struct {
		ID, KeyFile, PublicSet string
	}
	var raw map[string]string
	json.Unmarshal([]byte(r.stdout), &raw)
	out.ID, out.KeyFile, out.PublicSet = raw["id"], raw["key_file"], raw["public_set"]
	if !strings.HasPrefix(out.ID, "sk_") {
		t.Fatalf("id %q", out.ID)
	}
	if fi, err := os.Stat(out.KeyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v", err)
	}
	set, _ := os.ReadFile(out.PublicSet)
	if bytes.Contains(set, []byte("seed")) {
		t.Fatal("the public set holds the seed")
	}
	saved, savedNow := packKeys, packsNow
	packKeys = func() (packs.KeySet, error) { return packs.ParseKeySet(set) }
	packsNow = func() time.Time { return time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { packKeys, packsNow = saved, savedNow })
	dir := exportPacks(t)
	r = cli(t, packEnv, false, "policy", "packs", "sign", "--key", out.KeyFile, "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Signed 5 packs · "+out.ID) {
		t.Fatalf("sign: %d %s%s", r.code, r.stdout, r.stderr)
	}
	return dir
}

func TestPolicyPacksListAndVerify(t *testing.T) {
	r := cli(t, packEnv, false, "policy", "packs", "list")
	for _, want := range []string{"postgres-safe ", "supabase-mcp ", "stripe-refunds ", "github-agent ", "money-movement ", " Pro ",
		"From the copy built into deedglass · checked against its index (not sealed yet).", "Install one: deedglass policy install <name>"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("list lacks %q:\n%s", want, r.stdout)
		}
	}
	r = cli(t, packEnv, false, "policy", "packs", "list", "--json")
	var js struct {
		Object string        `json:"object"`
		Packs  []packs.Entry `json:"packs"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &js); err != nil || js.Object != "pack_list" || len(js.Packs) != 5 {
		t.Fatalf("list --json: %v %s", err, r.stdout)
	}
	r = cli(t, packEnv, false, "policy", "packs", "verify")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ 5 packs · every file matches · every case passes") {
		t.Fatalf("verify: %d %s%s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, packEnv, false, "policy", "packs", "verify", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"ok":true`) {
		t.Fatalf("verify --json: %d %s", r.code, r.stdout)
	}
	for _, args := range [][]string{
		{"policy", "packs", "list", "extra"}, {"policy", "packs", "verify", "extra"},
		{"policy", "packs", "list", "--from", "x", "--latest"}, {"policy", "install"}, {"policy", "install", "a", "b"},
		{"policy", "packs", "sign"},
	} {
		if r := cli(t, packEnv, false, args...); r.code != 2 {
			t.Errorf("%v: exit %d %s", args, r.code, r.stderr)
		}
	}
}

func TestPolicyInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "policies")
	r := cliIn(t, []byte("n\n"), packEnv, false, "policy", "install", "postgres-safe", "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stderr, "Write 2 files? [y/N] Nothing was written.") ||
		!strings.Contains(r.stdout, "✓ postgres-safe 1.0.0 · free · 29 cases passed · every rule covered") ||
		!strings.Contains(r.stdout, "  new     "+filepath.ToSlash(dir)+"/postgres-safe.yaml") {
		t.Fatalf("no: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("answering no wrote something")
	}
	// An empty answer is no.
	if r := cliIn(t, []byte("\n"), packEnv, false, "policy", "install", "postgres-safe", "--dir", dir); r.code != 0 {
		t.Fatal(r.stderr)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("an empty answer wrote something")
	}
	r = cliIn(t, []byte("y\n"), packEnv, false, "policy", "install", "postgres-safe", "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Wrote 2 files. Nothing was pushed or activated.") ||
		!strings.Contains(r.stdout, "next  deedglass policy test "+filepath.ToSlash(dir)+"/postgres-safe.yaml") {
		t.Fatalf("yes: %d %s%s", r.code, r.stdout, r.stderr)
	}
	pol := filepath.Join(dir, "postgres-safe.yaml")
	want, _ := packs.Embedded().Files["postgres-safe/postgres-safe.yaml"]
	got, err := os.ReadFile(pol)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("the written policy isn't the pack's bytes")
	}
	if fi, _ := os.Stat(pol); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	r = cli(t, packEnv, false, "policy", "install", "postgres-safe", "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Both files already match postgres-safe 1.0.0. Nothing to write.") {
		t.Fatalf("again: %d %s%s", r.code, r.stdout, r.stderr)
	}
	// A local edit: the diff is shown and one file is written.
	os.WriteFile(pol, append(bytes.Clone(want), []byte("# my note\n")...), 0o644)
	r = cliIn(t, []byte("yes\n"), packEnv, false, "policy", "install", "postgres-safe@1.0.0", "--dir", dir)
	if r.code != 0 || !strings.Contains(r.stdout, "  change  "+filepath.ToSlash(pol)+" +0, −1") || !strings.Contains(r.stdout, "− # my note") ||
		!strings.Contains(r.stdout, "  same    ") || !strings.Contains(r.stderr, "Write 1 file? [y/N]") {
		t.Fatalf("edit: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if got, _ := os.ReadFile(pol); !bytes.Equal(got, want) {
		t.Fatal("the edit wasn't replaced")
	}
	// --json without --yes plans only.
	dir2 := filepath.Join(t.TempDir(), "p")
	r = cli(t, packEnv, false, "policy", "install", "github-agent", "--dir", dir2, "--json")
	var js map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &js); err != nil || js["written"] != false || js["object"] != "pack_install" {
		t.Fatalf("json plan: %v %s", err, r.stdout)
	}
	if _, err := os.Stat(dir2); err == nil {
		t.Fatal("--json without --yes wrote")
	}
	r = cli(t, packEnv, false, "policy", "install", "github-agent", "--dir", dir2, "--json", "--yes")
	if err := json.Unmarshal([]byte(r.stdout), &js); err != nil || js["written"] != true {
		t.Fatalf("json write: %v %s", err, r.stdout)
	}
	// A symlink where a file would go is never replaced.
	dir3 := t.TempDir()
	os.Symlink("/etc/hosts", filepath.Join(dir3, "supabase-mcp.yaml"))
	r = cli(t, packEnv, false, "policy", "install", "supabase-mcp", "--dir", dir3, "--yes")
	if r.code != 1 || !strings.Contains(r.stderr, "isn't a regular file, so it won't be replaced. Nothing was written.") {
		t.Fatalf("symlink: %d %s%s", r.code, r.stdout, r.stderr)
	}
	// Unknown packs and versions.
	if r := cli(t, packEnv, false, "policy", "install", "nope"); r.code != 1 || !strings.Contains(r.stderr, "✕ No pack named nope.") {
		t.Fatalf("unknown: %s", r.stderr)
	}
	if r := cli(t, packEnv, false, "policy", "install", "postgres-safe@9.0.0"); r.code != 1 || !strings.Contains(r.stderr, "No version 9.0.0 of postgres-safe here (this copy has 1.0.0).") {
		t.Fatalf("version: %s", r.stderr)
	}
}

func TestPolicyInstallProPack(t *testing.T) {
	_, env, now := licenceSeams(t)
	dir := filepath.Join(t.TempDir(), "policies")
	r := cli(t, env, false, "policy", "install", "money-movement", "--dir", dir, "--yes")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "Pro packs need a Pro licence: deedglass.com/pro. Nothing was written.") {
		t.Fatalf("no licence: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a Pro pack was written without a licence")
	}
	// Free packs never open the licence, even a corrupt one.
	p := env["DEEDGLASS_LICENCE_FILE"]
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("corrupt"), 0o600)
	if r := cli(t, env, false, "policy", "install", "postgres-safe", "--dir", dir, "--yes"); r.code != 0 {
		t.Fatalf("free pack with a corrupt licence: %d %s", r.code, r.stderr)
	}
	os.Remove(p)
	if r := cli(t, env, false, "licence", "activate", "DG-7Q2M-TEST-0001"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	env["DEEDGLASS_NO_NETWORK"] = "1"
	r = cli(t, env, false, "policy", "install", "money-movement", "--dir", dir, "--yes")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ money-movement 1.0.0 · Pro · 18 cases passed") {
		t.Fatalf("with a licence: %d %s%s", r.code, r.stdout, r.stderr)
	}
	// Lapsed: the paused line, nothing written, and the installed pack stays as it is.
	*now = now.Add(15 * 24 * time.Hour)
	os.Remove(filepath.Join(dir, "money-movement.cases.yaml"))
	r = cli(t, env, false, "policy", "install", "money-movement", "--dir", dir, "--yes")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, "Pro paused: your licence was last checked 15 days ago. Run deedglass licence refresh. Everything free still works. Nothing was written.") {
		t.Fatalf("lapsed: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "money-movement.yaml")); err != nil {
		t.Fatal("a lapse touched an installed pack")
	}
}

func TestPolicyInstallFromACopy(t *testing.T) {
	src := signedPacks(t)
	dir := filepath.Join(t.TempDir(), "policies")
	r := cli(t, packEnv, false, "policy", "packs", "verify", "--from", src)
	if r.code != 0 || !strings.Contains(r.stdout, "index sealed by sk_") {
		t.Fatalf("verify --from: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if r := cli(t, packEnv, false, "policy", "install", "stripe-refunds", "--from", src, "--dir", dir, "--yes"); r.code != 0 {
		t.Fatalf("install --from: %d %s%s", r.code, r.stdout, r.stderr)
	}

	// The same copy as a release archive with a top directory.
	tgz := filepath.Join(t.TempDir(), "policy-packs.tar.gz")
	writeTarGz(t, src, "policy-packs-1.0.0/", tgz)
	if r := cli(t, packEnv, false, "policy", "packs", "list", "--from", tgz); r.code != 0 || !strings.Contains(r.stdout, tgz+" · index sealed by sk_") {
		t.Fatalf("list --from tar.gz: %d %s%s", r.code, r.stdout, r.stderr)
	}

	// The newest release over HTTPS (a stand-in server).
	archive, _ := os.ReadFile(tgz)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	defer srv.Close()
	savedURL, savedHTTP := packLatestURL, packsHTTP
	packLatestURL, packsHTTP = srv.URL+"/policy-packs.tar.gz", func() *http.Client { return srv.Client() }
	t.Cleanup(func() { packLatestURL, packsHTTP = savedURL, savedHTTP })
	if r := cli(t, packEnv, false, "policy", "packs", "verify", "--latest"); r.code != 0 {
		t.Fatalf("--latest: %d %s%s", r.code, r.stdout, r.stderr)
	}
	packLatestURL = "http://127.0.0.1:1/x.tar.gz"
	if r := cli(t, packEnv, false, "policy", "packs", "verify", "--latest"); r.code != 1 || !strings.Contains(r.stderr, "Couldn't download the newest policy packs. Nothing was written.") {
		t.Fatalf("--latest down: %d %s", r.code, r.stderr)
	}

	// One changed byte in a policy: refused, named, nothing written.
	p := filepath.Join(src, "stripe-refunds", "stripe-refunds.yaml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, '\n'), 0o644)
	dir2 := filepath.Join(t.TempDir(), "policies")
	r = cli(t, packEnv, false, "policy", "install", "stripe-refunds", "--from", src, "--dir", dir2, "--yes")
	if r.code != 1 || !strings.Contains(r.stderr, "✕ This pack doesn't match its signed index (stripe-refunds.yaml). Nothing was written.") {
		t.Fatalf("tampered: %d %s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(dir2); err == nil {
		t.Fatal("a tampered pack was written")
	}
	// A changed index: the seal no longer matches.
	ix := filepath.Join(src, "index.json")
	raw, _ := os.ReadFile(ix)
	os.WriteFile(ix, bytes.Replace(raw, []byte(`"cases":27`), []byte(`"cases":28`), 1), 0o644)
	if r := cli(t, packEnv, false, "policy", "packs", "verify", "--from", src); r.code != 1 || !strings.Contains(r.stderr, "This copy's index doesn't match its seal. Nothing was written.") {
		t.Fatalf("index edited: %d %s", r.code, r.stderr)
	}
	os.Remove(filepath.Join(src, "index.seal.json"))
	if r := cli(t, packEnv, false, "policy", "packs", "verify", "--from", src); r.code != 1 || !strings.Contains(r.stderr, "isn't signed by a key this build knows") {
		t.Fatalf("seal removed: %d %s", r.code, r.stderr)
	}
	// A copy with no index, and a path that isn't there.
	if r := cli(t, packEnv, false, "policy", "packs", "list", "--from", t.TempDir()); r.code != 1 || !strings.Contains(r.stderr, "has no index.json") {
		t.Fatalf("no index: %d %s", r.code, r.stderr)
	}
	if r := cli(t, packEnv, false, "policy", "packs", "list", "--from", filepath.Join(src, "missing")); r.code != 1 || !strings.Contains(r.stderr, "can't be read. Nothing was written.") {
		t.Fatalf("missing path: %d %s", r.code, r.stderr)
	}
}

// With no pack key in the build, a copy from outside the binary is refused with the reason.
func TestPolicyInstallUnsignedCopy(t *testing.T) {
	src := exportPacks(t)
	r := cli(t, packEnv, false, "policy", "install", "postgres-safe", "--from", src, "--yes", "--dir", t.TempDir())
	if r.code != 1 || !strings.Contains(r.stderr, "This build doesn't have the pack signing key yet. Use the copy built in: deedglass policy install postgres-safe") {
		t.Fatalf("%d %s%s", r.code, r.stdout, r.stderr)
	}
}

func TestPolicyPacksSignRefusals(t *testing.T) {
	keysSeams(t, nil)
	bad := filepath.Join(t.TempDir(), "key.json")
	os.WriteFile(bad, []byte("{}"), 0o644)
	if r := cli(t, packEnv, false, "policy", "packs", "sign", "--key", bad, "--dir", exportPacks(t)); r.code != 1 || !strings.Contains(r.stderr, "Can't use the pack key") {
		t.Fatalf("bad key: %d %s", r.code, r.stderr)
	}
}

func writeTarGz(t *testing.T, dir, prefix, out string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		tw.WriteHeader(&tar.Header{Name: prefix + filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg})
		tw.Write(b)
		return nil
	})
	tw.Close()
	zw.Close()
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

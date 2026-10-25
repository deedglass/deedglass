//go:build integration

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/licence"
	"github.com/deedglass/deedglass/internal/securitypack"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// securityPackPolicy denies destructive SQL, holds refunds for two people, allows the fake tools.
const securityPackPolicy = `deedglass: policy/v1
name: acme-guardrails
defaults:
  decision: deny
  reason: No rule allows this action.
budgets:
  - id: calls-per-agent
    match: { tool: "fake.*" }
    per: agent
    window: minute
    max_calls: 600
rules:
  - id: block-destructive-sql
    match: { connector_type: postgres }
    when: sql.kind in ["drop", "truncate"] || sql.unbounded_write
    decision: deny
    reason: Destructive SQL never runs.
  - id: refunds-need-two
    match: { tool: "*.create_refund" }
    decision: require_approval
    reason: Refunds need two people.
    approval: { approvers: [role:admin], required: 2, expires_in: 30m }
  - id: allow-fake
    match: { tool: "fake.*" }
    decision: allow
`

// The canaries: an argument and result value, a member's email and a vault plaintext. None may
// appear in any file of a pack, inside records.tar.gz included.
const (
	packCanaryArg   = "canary-s51-argument-5512"
	packCanaryEmail = "canary-s51-mail@acme.test"
	packCanaryVault = "canary-s51-vault-secret-9931"
)

// TestSecurityPackLive is S51's acceptance on a real server: readiness (24 rows with fixes),
// pro_required without a licence and agent keys refused, then with a test Pro licence a pack built
// and checked, one changed byte in each sealed file caught and named, pinned keys (and another
// server's keys refused), the aliases build with no real name outside aliases.private.json, and the
// canary sweep over every file, records included.
func TestSecurityPackLive(t *testing.T) {
	keyRequired(t) // steps 1–3 cover the dormant key path; 2b runs DL-131's free policy
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 51)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}
	var outputs []string
	start := func(extra map[string]string, migrate bool) (*served, string) {
		t.Helper()
		se := map[string]string{}
		for k, v := range env {
			se[k] = v
		}
		for k, v := range extra {
			se[k] = v
		}
		args := []string{}
		if migrate {
			args = append(args, "--migrate")
		}
		s := startServe(t, se, args...)
		select {
		case a := <-s.addr:
			return s, "http://" + a.String()
		case c := <-s.code:
			t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
		case <-time.After(30 * time.Second):
			t.Fatal("serve never listened")
		}
		return nil, ""
	}
	s, base := start(nil, true)
	running := true
	stop := func() {
		if running {
			s.sigs <- syscall.SIGTERM
			s.wait(t)
			outputs = append(outputs, s.out.String(), s.err.String())
			running = false
		}
	}
	defer stop()
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(args ...string) result {
		t.Helper()
		r := cli(t, env, false, args...)
		outputs = append(outputs, r.stdout, r.stderr)
		return r
	}
	r := run("org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		Org      struct{ ID string } `json:"org"`
		AdminKey struct {
			Secret string `json:"secret"`
		} `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	org := created.Org.ID
	adminKey := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = adminKey
	{ // A second admin whose email is a canary.
		c := e.Admin(t)
		u := ids.New[ids.UserKind]()
		if _, err := c.Exec(context.Background(), `INSERT INTO deedglass.users (id, email, display_name, status) VALUES ($1, $2, 'Omar', 'active')`, u, packCanaryEmail); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'admin', 'active', now())`, ids.New[ids.MembershipKind](), org, u); err != nil {
			t.Fatal(err)
		}
	}
	pf := filepath.Join(t.TempDir(), "acme-guardrails.yaml")
	_ = os.WriteFile(pf, []byte(securityPackPolicy), 0o600)
	if r = run("policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run("policy", "activate", "acme-guardrails", "--version", "1", "--mode", "enforce", "--reason", "S51 acceptance", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	call := func(key, method, path, body string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := call(adminKey, "POST", "/v1/agents", `{"name":"orders-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(adminKey, "POST", "/v1/agents/"+agent.ID+"/keys", `{"scopes":["actions:execute","actions:read","deeds:read"]}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	// A REST connector whose credential is a canary.
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "connectors", "orders.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cf := filepath.Join(t.TempDir(), "orders.yaml")
	_ = os.WriteFile(cf, bytes.Replace(src, []byte("name: orders\n"), []byte("name: orders-api\n"), 1), 0o600)
	if r = run("connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("connector: %+v", r)
	}
	env["S51_TOKEN"] = packCanaryVault
	if r = run("connector", "credentials", "set", "orders-api", "--from-env", "S51_TOKEN"); r.code != 0 {
		t.Fatalf("credentials: %+v", r)
	}
	delete(env, "S51_TOKEN")
	for i := 0; i < 5; i++ {
		if c, b := call(ak.Secret, "POST", "/v1/actions", `{"tool":"fake.echo","args":{"message":"`+packCanaryArg+`"}}`); c != 200 {
			t.Fatalf("act: %d %s", c, b)
		}
	}

	// 1. Readiness (free): 24 rows; the gaps with fixes.
	r = run("security-pack", "readiness", "--json")
	var rd securitypack.Readiness
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &rd) != nil || len(rd.Rows) != 24 || rd.Org != org {
		t.Fatalf("readiness: %+v", r)
	}
	st := map[string]securitypack.Status{}
	for _, row := range rd.Rows {
		st[row.ID] = row.Status
		if (row.Status == securitypack.Partly || row.Status == securitypack.NotSetUp) && row.Fix == "" {
			t.Errorf("%s: %s without a fix", row.ID, row.Status)
		}
	}
	for id, want := range map[string]securitypack.Status{"rules_active": securitypack.InPlace, "default_deny": securitypack.InPlace,
		"four_eyes": securitypack.InPlace, "stop_all": securitypack.InPlace, "stop_used": securitypack.NotSetUp,
		"records_sealed": securitypack.InPlace, "agents_listed": securitypack.Partly, "limits_set": securitypack.InPlace,
		"undo_available": securitypack.NotSetUp, "egress_limited": securitypack.Partly} {
		if st[id] != want {
			t.Errorf("readiness %s: %s, want %s", id, st[id], want)
		}
	}
	r = run("security-pack", "readiness")
	if r.code != 0 || !strings.Contains(r.stdout, "Security review readiness · Acme Robotics · last 90 days") ||
		!strings.Contains(r.stdout, "Not set up") || !strings.Contains(r.stdout, "stop_used") ||
		!strings.Contains(r.stdout, "Build the document for a reviewer: deedglass security-pack build") {
		t.Fatalf("readiness text: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	// The budget: readiness p99 over 20 calls.
	var lat []time.Duration
	for i := 0; i < 20; i++ {
		t0 := time.Now()
		if c, b := call(adminKey, "GET", "/v1/security_pack/readiness", ""); c != 200 {
			t.Fatalf("%d %s", c, b)
		}
		lat = append(lat, time.Since(t0))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	t.Logf("readiness: p50 %s, max %s over 20 calls (budget p99 300ms)", lat[10], lat[19])

	// 2. Agent keys are refused on both routes; without Pro the build answers pro_required.
	for _, m := range [][2]string{{"GET", "/v1/security_pack/readiness"}, {"POST", "/v1/security_pack"}} {
		if c, b := call(ak.Secret, m[0], m[1], ""); c != 403 || !strings.Contains(b, "Agents can't read or build the security review pack") {
			t.Fatalf("agent key %s %s: %d %s", m[0], m[1], c, b)
		}
	}
	if c, b := call(adminKey, "POST", "/v1/security_pack", `{}`); c != 403 || !strings.Contains(b, `"pro_required"`) || !strings.Contains(b, "readiness") {
		t.Fatalf("no Pro (API): %d %s", c, b)
	}
	out := t.TempDir()
	r = run("security-pack", "build", "--out", out)
	if r.code != 1 || !strings.Contains(r.stderr, "part of Pro") || !strings.Contains(r.stderr, "deedglass security-pack readiness") {
		t.Fatalf("no Pro: %+v", r)
	}
	if c, b := call(adminKey, "GET", "/v1/security_pack/readiness?period_days=3", ""); c != 400 || !strings.Contains(b, "period_days") {
		t.Fatalf("period: %d %s", c, b)
	}

	// 2b. DL-131, free while we grow: no licence at all, and serve turns the build on.
	stop()
	licencePolicy = licence.FreeWhileWeGrow
	s, base = start(nil, false)
	running = true
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	freeOut := t.TempDir()
	r = run("security-pack", "build", "--period", "90d", "--out", freeOut)
	if r.code != 0 || !strings.Contains(r.stdout, "sealed (5 files)") {
		t.Fatalf("free while we grow, no licence: %+v", r)
	}
	licencePolicy = licence.KeyRequired

	// 3. With a test Pro licence (restart serve so it asks at start).
	stop()
	f, lenv, _ := licenceSeams(t)
	f.plan = licence.PlanPro
	if r := cli(t, lenv, false, "licence", "activate", "DG-PRO-S51-0001"); r.code != 0 {
		t.Fatalf("activate: %+v", r)
	}
	s, base = start(lenv, false)
	running = true
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	t0 := time.Now()
	r = run("security-pack", "build", "--period", "90d", "--out", out)
	t.Logf("build: %s\n%s%s", time.Since(t0), r.stdout, r.stderr)
	if r.code != 0 || !strings.Contains(r.stdout, "24 answers") || !strings.Contains(r.stdout, "sealed (5 files)") ||
		!strings.Contains(r.stdout, "✓ Checked: every file matches its sealed record") {
		t.Fatalf("build: %+v", r)
	}
	folders, _ := filepath.Glob(filepath.Join(out, "security-pack-acme-robotics-*"))
	if len(folders) != 1 {
		t.Fatalf("folders %v", folders)
	}
	pack := folders[0]
	for _, name := range []string{securitypack.FileIndex, securitypack.FileMarkdown, securitypack.FileAnswers, securitypack.FileRecords, securitypack.FileSeal, securitypack.FileReadme} {
		fi, err := os.Stat(filepath.Join(pack, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%-16s %8d bytes", name, fi.Size())
	}
	r = run("security-pack", "verify", pack)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Checked: every file matches its sealed record;") || !strings.Contains(r.stdout, "chain intact") {
		t.Fatalf("verify: %+v", r)
	}
	t.Logf("verify ✓\n%s", r.stdout)
	// Another build of the same folder name refuses to overwrite.
	if r = run("security-pack", "build", "--out", out); r.code != 1 || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("overwrite: %+v", r)
	}
	// One changed byte in each sealed file fails and names the file.
	for _, name := range []string{securitypack.FileIndex, securitypack.FileAnswers, securitypack.FileRecords} {
		path := filepath.Join(pack, name)
		orig, _ := os.ReadFile(path)
		b := append([]byte(nil), orig...)
		b[len(b)/2] ^= 0x01
		_ = os.WriteFile(path, b, 0o644)
		r = run("security-pack", "verify", pack)
		if r.code != 1 || !strings.Contains(r.stdout, name+": it was changed after it was sealed") {
			t.Fatalf("%s changed: %+v", name, r)
		}
		if name == securitypack.FileIndex {
			t.Logf("verify ✕\n%s", r.stdout)
		}
		_ = os.WriteFile(path, orig, 0o644)
	}
	// Pinned keys: this server's pass; another server's keys fail.
	kc, kb := call(adminKey, "GET", "/.well-known/deedglass/keys.json", "")
	if kc != 200 {
		t.Fatalf("keys: %d", kc)
	}
	keys := filepath.Join(t.TempDir(), "keys.json")
	_ = os.WriteFile(keys, []byte(kb), 0o600)
	if r = run("security-pack", "verify", pack, "--keys", keys); r.code != 0 || strings.Contains(r.stdout, "Checked with the keys inside the pack") {
		t.Fatalf("pinned: %+v", r)
	}
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{99}, 32))
	oks, _ := deed.NewKeySet("https://other.example", []deed.KeyRecord{{ID: ids.New[ids.SealKeyKind]().String(), PublicKey: other.Public().(ed25519.PublicKey),
		Status: deed.KeyActive, ActivatedAt: time.Now().Add(-time.Hour)}})
	ob, _ := json.Marshal(oks)
	otherKeys := filepath.Join(t.TempDir(), "other.json")
	_ = os.WriteFile(otherKeys, ob, 0o600)
	if r = run("security-pack", "verify", pack, "--keys", otherKeys); r.code != 1 || !strings.Contains(r.stdout, "its sealed record doesn't check") {
		t.Fatalf("another server's keys: %+v", r)
	}
	// verify --json and the schema.
	r = run("security-pack", "verify", pack, "--json")
	var pc packCheck
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &pc) != nil || !pc.OK || pc.Records == nil || pc.Records.Intact == 0 {
		t.Fatalf("verify --json: %+v", r)
	}

	// 4. --aliases: no real agent, connector or rule name outside aliases.private.json.
	out2 := t.TempDir()
	if r = run("security-pack", "build", "--aliases", "--out", out2); r.code != 0 || !strings.Contains(r.stdout, "records.tar.gz left out") ||
		!strings.Contains(r.stdout, "except aliases.private.json") {
		t.Fatalf("aliases: %+v", r)
	}
	folders2, _ := filepath.Glob(filepath.Join(out2, "security-pack-*"))
	real := []string{"orders-bot", "orders-api", "acme-guardrails"}
	sawAlias := false
	for _, path := range allFiles(t, folders2[0]) {
		b, _ := os.ReadFile(path)
		if filepath.Base(path) == securitypack.FileAliases {
			for _, n := range real {
				if !bytes.Contains(b, []byte(n)) {
					t.Errorf("aliases.private.json misses %s", n)
				}
			}
			continue
		}
		for _, n := range real {
			if bytes.Contains(b, []byte(n)) {
				t.Errorf("%s carries the real name %s", filepath.Base(path), n)
			}
		}
		sawAlias = sawAlias || bytes.Contains(b, []byte("connector-1"))
	}
	if !sawAlias {
		t.Error("no alias in the aliased pack")
	}
	if r = run("security-pack", "verify", folders2[0]); r.code != 0 || !strings.Contains(r.stdout, "don't send it") {
		t.Fatalf("aliases verify: %+v", r)
	}

	// 5. The canary sweep: every file of both packs, records.tar.gz unpacked too.
	for _, d := range []string{pack, folders2[0]} {
		for _, path := range allFiles(t, d) {
			for name, data := range expand(t, path) {
				for _, c := range []string{packCanaryArg, packCanaryEmail, packCanaryVault} {
					if bytes.Contains(data, []byte(c)) {
						t.Errorf("%s carries the canary %q", name, c)
					}
				}
			}
		}
	}
	// The canary did reach the server (so the sweep means something): the action's result holds it.
	if c, b := call(adminKey, "GET", "/v1/actions?limit=1", ""); c != 200 || !strings.Contains(b, "fake.echo") {
		t.Fatalf("actions: %d", c)
	}
	if t.Failed() {
		t.Logf("outputs:\n%s", strings.Join(outputs, "\n"))
	}
}

func allFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// expand returns a file's bytes, and for a .tar.gz each member's bytes too.
func expand(t *testing.T, path string) map[string][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{filepath.Base(path): b}
	if !strings.HasSuffix(path, ".tar.gz") {
		return out
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		m, _ := io.ReadAll(tr)
		out[filepath.Base(path)+"/"+h.Name] = m
	}
	return out
}

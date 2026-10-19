//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/store/storetest"
)

// fhirProvenPolicy: FHIR reads run; creates, updates and patches must be witnessed at L2 (the FHIR
// server's own change log, ADR-108); deletes at L1 (a Medplum delete reports no version to bind).
const fhirProvenPolicy = `deedglass: policy/v1
name: fhir-proven
description: Proven checks on FHIR servers that store resources in Postgres (HC-P05).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: fhir-reads
    match: { connector_type: fhir, side_effect: read }
    when: request.fhir.enriched
    decision: allow
  - id: fhir-writes
    match: { connector_type: fhir, tool: ["*.create", "*.update", "*.patch"] }
    when: request.fhir.enriched
    decision: allow
    verify: L2
  - id: fhir-deletes
    match: { connector_type: fhir, tool: ["*.delete"] }
    when: request.fhir.enriched
    decision: allow
    verify: L1
`

// TestFHIRProvenLive is HC-P05 against real FHIR servers (scripts/fhir_proven.sh starts them in
// Docker and sets DEEDGLASS_IT_HAPI_* and DEEDGLASS_IT_MEDPLUM_*): HAPI FHIR JPA v8.12.0-2 and
// Medplum 5.2.6, each on PostgreSQL 18 with a witness role and deedglass_pub, behind two fhir
// connectors with witness.enabled, a real serve and its decoder on a throwaway Postgres.
//
// HAPI: a create and an update are VERIFIED at L2 (l2.fhir.hapi.8, version_matched); a create whose
// subject is an unknown Patient commits a placeholder Patient too and is MISMATCH at L2
// (fhir_extra_versions, I07); a write straight to HAPI with no Deedglass action is stored
// unattributed and flagged out_of_band; a write carrying an action's X-Request-ID token is
// request_id_reused, never bound (I06); a delete is bound by the version HAPI names in
// Content-Location. Medplum: a create and an update are VERIFIED at L2 (l2.fhir.medplum.5) with
// meta.author checked; a delete stays Checked (L1); a write by another client is out_of_band.
// No resource value reaches Deedglass's frames; the export verifies.
func TestFHIRProvenLive(t *testing.T) {
	hapiURL, hapiDSN := os.Getenv("DEEDGLASS_IT_HAPI_URL"), os.Getenv("DEEDGLASS_IT_HAPI_WITNESS_DSN")
	medURL, medDSN := os.Getenv("DEEDGLASS_IT_MEDPLUM_URL"), os.Getenv("DEEDGLASS_IT_MEDPLUM_WITNESS_DSN")
	if hapiURL == "" || hapiDSN == "" || medURL == "" || medDSN == "" {
		t.Skip("DEEDGLASS_IT_HAPI_* and DEEDGLASS_IT_MEDPLUM_* aren't set (scripts/fhir_proven.sh starts the servers and sets them)")
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	// direct calls a FHIR server as another client would: no Deedglass in between.
	direct := func(base, token, method, p, body string, hdr ...string) (int, http.Header, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, base+p, rd)
		req.Header.Set("Accept", "application/fhir+json")
		if body != "" {
			req.Header.Set("Content-Type", "application/fhir+json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, res.Header, out
	}
	// Medplum: the made-up super admin logs in, creates a client application, and the connector
	// uses that client's access token (its meta.author is the ClientApplication).
	form := func(p string, v url.Values, token string) map[string]any {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), "POST", medURL+p, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode != 200 {
			t.Fatalf("medplum %s: %d %v", p, res.StatusCode, out)
		}
		return out
	}
	postJSON := func(p string, body any, token string) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(context.Background(), "POST", medURL+p, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode >= 300 {
			t.Fatalf("medplum %s: %d %v", p, res.StatusCode, out)
		}
		return out
	}
	login := postJSON("/auth/login", map[string]any{"email": os.Getenv("DEEDGLASS_IT_MEDPLUM_ADMIN"), "password": os.Getenv("DEEDGLASS_IT_MEDPLUM_PASSWORD"),
		"scope": "openid", "codeChallenge": "deedglass-it", "codeChallengeMethod": "plain"}, "")
	tok := form("/oauth2/token", url.Values{"grant_type": {"authorization_code"}, "code": {fmt.Sprint(login["code"])}, "code_verifier": {"deedglass-it"}}, "")
	adminTok := fmt.Sprint(tok["access_token"])
	project := strings.TrimPrefix(fmt.Sprint(tok["project"].(map[string]any)["reference"]), "Project/")
	client := postJSON("/admin/projects/"+project+"/client", map[string]any{"name": "deedglass-it"}, adminTok)
	clientID := fmt.Sprint(client["id"])
	cc := form("/oauth2/token", url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {fmt.Sprint(client["secret"])}}, "")
	clientTok := fmt.Sprint(cc["access_token"])
	medFHIR := medURL + "/fhir/R4"
	if code, _, out := direct(hapiURL, "", "PUT", "/Patient/dg-proven-1", `{"resourceType":"Patient","id":"dg-proven-1","name":[{"family":"Synthetic"}]}`); code != 200 && code != 201 {
		t.Fatalf("seed patient: %d %v", code, out)
	}

	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 13)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}
	s := startServe(t, env, "--migrate")
	var server string
	select {
	case a := <-s.addr:
		server = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = server, server
	run := func(env map[string]string, args ...string) result {
		t.Helper()
		return cli(t, env, false, args...)
	}
	r := run(env, "org", "create", "--name", "Synthetic Clinic", "--slug", "clinic", "--owner-email", "owner@clinic.test", "--admin-key", "--json")
	var created struct {
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	admin := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = admin
	pf := filepath.Join(t.TempDir(), "fhir-proven.yaml")
	_ = os.WriteFile(pf, []byte(fhirProvenPolicy), 0o600)
	if r = run(env, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, "policy", "activate", "fhir-proven", "--version", "1", "--mode", "enforce", "--reason", "FHIR Proven", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	call := func(key, method, p string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, server+p, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	interactions := []string{"read", "vread", "search", "history", "create", "update", "patch", "delete"}
	connectors := map[string]string{}
	mk := func(name string, cfg map[string]any, kind string, secret map[string]string) {
		t.Helper()
		code, out := call(admin, "POST", "/v1/connectors", map[string]any{"type": "fhir", "name": name, "description": "HC-P05 live run.", "config": cfg})
		if code != 201 {
			t.Fatalf("create %s: %d %v", name, code, out)
		}
		connectors[name] = fmt.Sprint(out["id"])
		if code, out = call(admin, "PUT", "/v1/connectors/"+connectors[name]+"/credentials", map[string]any{"kind": kind, "secret": secret}); code != 200 && code != 201 {
			t.Fatalf("credentials %s: %d %v", name, code, out)
		}
	}
	mk("hapi", map[string]any{"base_url": hapiURL, "auth": map[string]any{"kind": "none"}, "allowed_resource_types": []string{"Observation", "Patient"},
		"allowed_interactions": interactions, "witness": map[string]any{"enabled": true, "server": "hapi"}}, "fields", map[string]string{"witness_dsn": hapiDSN})
	mk("medplum", map[string]any{"base_url": medFHIR, "auth": map[string]any{"kind": "bearer"}, "allowed_resource_types": []string{"Observation", "Patient"},
		"allowed_interactions": interactions, "witness": map[string]any{"enabled": true, "server": "medplum", "author": "ClientApplication/" + clientID}},
		"bearer", map[string]string{"token": clientTok, "witness_dsn": medDSN})
	for name, id := range connectors {
		deadline := time.Now().Add(60 * time.Second)
		for {
			_, v := call(admin, "GET", "/v1/connectors/"+id+"/decoder", nil)
			if v["status"] == "running" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the %s decoder isn't running: %v\n%s", name, v, s.err)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	if r = run(env, "decoder", "status"); r.code != 0 {
		t.Fatalf("decoder status: %+v", r)
	}
	t.Logf("\n$ deedglass decoder status\n%s", r.stdout)

	code, out := call(admin, "POST", "/v1/agents", map[string]any{"name": "charting-agent"})
	if code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	code, out = call(admin, "POST", "/v1/agents/"+fmt.Sprint(out["id"])+"/keys", map[string]any{})
	if code != 201 {
		t.Fatalf("%d %v", code, out)
	}
	agentKey := fmt.Sprint(out["secret"])
	type action struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Result struct {
			Status int `json:"status"`
			Body   struct {
				ID string `json:"id"`
			} `json:"body"`
		} `json:"result"`
		Witness struct {
			Level, Verdict, Method string
			Detail                 struct {
				Reasons []struct{ Code, Level, Message string } `json:"reasons"`
			} `json:"detail"`
		} `json:"witness"`
	}
	act := func(tool string, args map[string]any) action {
		t.Helper()
		code, out := call(agentKey, "POST", "/v1/actions", map[string]any{"tool": tool, "args": args, "wait": 20})
		if code >= 300 && code != 403 {
			t.Fatalf("%s: %d %v", tool, code, out)
		}
		id := fmt.Sprint(out["id"])
		if out["id"] == nil {
			t.Fatalf("%s: %d %v", tool, code, out)
		}
		var a action
		deadline := time.Now().Add(45 * time.Second)
		for {
			_, got := call(admin, "GET", "/v1/actions/"+id+"?expand[]=witness.detail&wait=5", nil)
			b, _ := json.Marshal(got)
			a = action{}
			_ = json.Unmarshal(b, &a)
			if a.State == "sealed" || time.Now().After(deadline) {
				break
			}
		}
		return a
	}
	l2codes := func(a action) []string {
		var cs []string
		for _, r := range a.Witness.Detail.Reasons {
			if r.Level == "L2" {
				cs = append(cs, r.Code)
			}
		}
		return cs
	}
	has := func(cs []string, c string) bool {
		for _, x := range cs {
			if x == c {
				return true
			}
		}
		return false
	}
	message := func(a action, code string) string {
		for _, r := range a.Witness.Detail.Reasons {
			if r.Code == code {
				return r.Message
			}
		}
		return ""
	}
	obs := func(status string, v int, subject string) map[string]any {
		return map[string]any{"resourceType": "Observation", "status": status,
			"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}}},
			"subject":       map[string]any{"reference": subject},
			"valueQuantity": map[string]any{"value": v, "unit": "beats/minute", "system": "http://unitsofmeasure.org", "code": "/min"}}
	}
	frames := func(connector string) [][]string {
		t.Helper()
		c := e.Admin(t)
		defer c.Close(context.Background())
		rows, err := c.Query(context.Background(), `SELECT class, COALESCE(action_id, ''), array_to_string(flags, ',') FROM deedglass.decoded_txs
		    WHERE connector_id = $1 ORDER BY commit_lsn`, connectors[connector])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][]string
		for rows.Next() {
			var a, b, f string
			_ = rows.Scan(&a, &b, &f)
			out = append(out, []string{a, b, f})
		}
		return out
	}
	waitFrame := func(connector string, ok func(f []string) bool) []string {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			for _, f := range frames(connector) {
				if ok(f) {
					return f
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("no such frame on %s: %v", connector, frames(connector))
		return nil
	}

	// --- HAPI FHIR JPA ---
	// 1. A create: HAPI's transaction is bound by the version it reported, and matches.
	a := act("hapi.create", map[string]any{"resource_type": "Observation", "resource": obs("preliminary", 72, "Patient/dg-proven-1")})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L2" || a.Witness.Method != "l2.fhir.hapi.8" || !has(l2codes(a), "version_matched") {
		t.Fatalf("hapi create: %+v", a)
	}
	first, id := a.ID, a.Result.Body.ID
	t.Logf("HAPI create: %s", message(a, "version_matched"))
	// 2. An update.
	a = act("hapi.update", map[string]any{"resource_type": "Observation", "id": id, "resource": func() map[string]any {
		m := obs("final", 74, "Patient/dg-proven-1")
		m["id"] = id
		return m
	}()})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L2" || !has(l2codes(a), "version_matched") {
		t.Fatalf("hapi update: %+v", a)
	}
	// 3. I07 on the real server: one create, a placeholder Patient committed with it.
	ghost := fmt.Sprintf("ghost-%d", time.Now().UnixNano()%1_000_000_000)
	a = act("hapi.create", map[string]any{"resource_type": "Observation", "resource": obs("final", 75, "Patient/"+ghost)})
	if a.Witness.Verdict != "MISMATCH" || a.Witness.Level != "L2" || !has(l2codes(a), "fhir_extra_versions") ||
		!strings.Contains(message(a, "fhir_extra_versions"), "Patient/"+ghost) {
		t.Fatalf("hapi placeholder: %+v", a)
	}
	t.Logf("I07 on HAPI: %s", message(a, "fhir_extra_versions"))
	// 4. An out-of-band write: straight to HAPI, no Deedglass action. Stored unattributed, flagged.
	before := len(frames("hapi"))
	if code, _, out := direct(hapiURL, "", "PUT", "/Observation/"+id, func() string {
		m := obs("amended", 76, "Patient/dg-proven-1")
		m["id"] = id
		b, _ := json.Marshal(m)
		return string(b)
	}()); code != 200 {
		t.Fatalf("out-of-band write: %d %v", code, out)
	}
	waitFrame("hapi", func(f []string) bool {
		return len(frames("hapi")) > before && f[0] == "unattributed" && strings.Contains(f[2], "out_of_band")
	})
	// 5. I06 on the real server: another client sends the first action's token as X-Request-ID.
	token := first[len(first)-16:]
	if code, _, out := direct(hapiURL, "", "POST", "/Observation", func() string {
		b, _ := json.Marshal(obs("final", 99, "Patient/dg-proven-1"))
		return string(b)
	}(), "X-Request-ID", token); code != 201 {
		t.Fatalf("forged token write: %d %v", code, out)
	}
	waitFrame("hapi", func(f []string) bool { return f[0] == "request_id_reused" && f[1] == first })
	// 6. A delete, bound by the version HAPI names in Content-Location; checked at L1 by the policy,
	// witnessed at L2 too.
	a = act("hapi.delete", map[string]any{"resource_type": "Observation", "id": id})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L2" || !has(l2codes(a), "version_matched") {
		t.Fatalf("hapi delete: %+v", a)
	}

	// The plain output docs/connectors/fhir.md shows: a Proven create, and the placeholder trap.
	agentEnv := map[string]string{}
	for k, v := range env {
		agentEnv[k] = v
	}
	agentEnv["DEEDGLASS_API_KEY"] = agentKey
	b1, _ := json.Marshal(map[string]any{"resource_type": "Observation", "resource": obs("final", 77, "Patient/dg-proven-1")})
	r = run(agentEnv, "act", "hapi.create", "--args", string(b1), "--wait", "20s")
	if !strings.Contains(r.stdout+r.stderr, "Proven") {
		t.Fatalf("plain Proven create: %+v", r)
	}
	t.Logf("\n$ deedglass act hapi.create --args '{\"resource_type\":\"Observation\",\"resource\":{…}}'\n%s%s", r.stdout, r.stderr)
	ghost2 := fmt.Sprintf("ghost-%d", time.Now().UnixNano()%1_000_000_000)
	b2, _ := json.Marshal(map[string]any{"resource_type": "Observation", "resource": obs("final", 78, "Patient/"+ghost2)})
	r = run(agentEnv, "act", "hapi.create", "--args", string(b2), "--wait", "20s")
	if !strings.Contains(r.stdout+r.stderr, "fhir_extra_versions") && !strings.Contains(r.stdout+r.stderr, "2 resource versions") {
		t.Fatalf("plain placeholder create: %+v", r)
	}
	t.Logf("\n$ deedglass act hapi.create --args '{… \"subject\":{\"reference\":\"Patient/%s\"} …}'\n%s%s", ghost2, r.stdout, r.stderr)

	// --- Medplum ---
	// 7. A create and an update: bound by the versionId UUID Medplum reported, meta.author checked.
	a = act("medplum.create", map[string]any{"resource_type": "Observation", "resource": obs("preliminary", 72, "Patient/dg-proven-1")})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L2" || a.Witness.Method != "l2.fhir.medplum.5" || !has(l2codes(a), "version_matched") {
		t.Fatalf("medplum create: %+v", a)
	}
	mid := a.Result.Body.ID
	t.Logf("Medplum create: %s", message(a, "version_matched"))
	a = act("medplum.update", map[string]any{"resource_type": "Observation", "id": mid, "resource": func() map[string]any {
		m := obs("final", 74, "Patient/dg-proven-1")
		m["id"] = mid
		return m
	}()})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L2" || !has(l2codes(a), "version_matched") {
		t.Fatalf("medplum update: %+v", a)
	}
	// 8. Another client writes the same Observation: out of band.
	before = len(frames("medplum"))
	if code, _, out := direct(medFHIR, adminTok, "PUT", "/Observation/"+mid, func() string {
		m := obs("amended", 76, "Patient/dg-proven-1")
		m["id"] = mid
		b, _ := json.Marshal(m)
		return string(b)
	}()); code != 200 {
		t.Fatalf("medplum out-of-band write: %d %v", code, out)
	}
	waitFrame("medplum", func(f []string) bool {
		return len(frames("medplum")) > before && f[0] == "unattributed" && strings.Contains(f[2], "out_of_band")
	})
	// 9. A Medplum delete reports no version: Checked (L1) only.
	a = act("medplum.delete", map[string]any{"resource_type": "Observation", "id": mid})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L1" || len(l2codes(a)) != 0 {
		t.Fatalf("medplum delete: %+v", a)
	}

	// No resource value is in Deedglass's frames: content is kept as keyed leaf digests.
	c := e.Admin(t)
	var leaked int
	if err := c.QueryRow(context.Background(), `SELECT count(*) FROM deedglass.decoded_txs WHERE changes::text LIKE '%8867-4%' OR changes::text LIKE '%beats/minute%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("frames holding a resource value: %d %v", leaked, err)
	}
	c.Close(context.Background())
	if r = run(env, "decoder", "status"); r.code != 0 {
		t.Fatalf("decoder status: %+v", r)
	}
	t.Logf("\n$ deedglass decoder status\n%s", r.stdout)
	bundle := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if br := run(env, "deeds", "export", "--out", bundle); br.code != 0 {
		t.Fatalf("export bundle: %+v", br)
	}
	if vr := run(env, "verify", bundle); vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
}

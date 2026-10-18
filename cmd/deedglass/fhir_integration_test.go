//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/store/storetest"
)

// fhirPolicy gates the Observation tools of examples/connectors/fhir-hapi.yaml (HC-P00): reads are
// allowed, entered-in-error is refused, other writes run and must be checked by a read-back (L1).
const fhirPolicy = `deedglass: policy/v1
name: fhir-observations
description: Observations on a FHIR server through the REST connector (HC-P00).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: observation-reads
    match: { tool: ["fhir.get_observation"] }
    decision: allow
  - id: no-entered-in-error
    match: { tool: ["fhir.update_observation"] }
    when: args.status == "entered-in-error"
    decision: deny
    reason: Agents don't mark an Observation entered-in-error. Ask a clinician to correct it in the record system.
  - id: observation-writes
    match: { tool: ["fhir.create_observation", "fhir.update_observation"] }
    decision: allow
    verify: L1
`

// TestFHIRRestLive is HC-P00 on a real FHIR server: HAPI FHIR JPA (make fhir-it starts it in
// Docker and sets DEEDGLASS_IT_FHIR_URL) behind the REST connector, a real serve on a throwaway
// Postgres, and the policy above. A create and an update are allowed and checked by reading the
// Observation back (Checked · Matches); an update to entered-in-error is denied and never reaches
// the server; an update whose body id differs from the path is refused by the server (failed). The
// FHIR server's own history shows exactly the versions Deedglass let through.
func TestFHIRRestLive(t *testing.T) {
	fhirURL := strings.TrimSuffix(os.Getenv("DEEDGLASS_IT_FHIR_URL"), "/")
	if fhirURL == "" {
		t.Skip("DEEDGLASS_IT_FHIR_URL is not set (make fhir-it starts HAPI FHIR JPA and sets it)")
	}
	base, path, ok := strings.Cut(strings.TrimPrefix(fhirURL, "http://"), "/")
	if !ok || path != "fhir" {
		t.Fatalf("DEEDGLASS_IT_FHIR_URL must look like http://127.0.0.1:PORT/fhir, got %q", fhirURL)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	direct := func(method, p, body string) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, fhirURL+p, rd)
		req.Header.Set("Accept", "application/fhir+json")
		if body != "" {
			req.Header.Set("Content-Type", "application/fhir+json")
		}
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	// A synthetic patient for the Observations to point at (HAPI checks references on write).
	if code, out := direct("PUT", "/Patient/dg-synthetic-1", `{"resourceType":"Patient","id":"dg-synthetic-1","name":[{"family":"Synthetic","given":["Test"]}]}`); code != 200 && code != 201 {
		t.Fatalf("seed patient: %d %v", code, out)
	}

	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 11)
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
	pf := filepath.Join(t.TempDir(), "fhir-observations.yaml")
	_ = os.WriteFile(pf, []byte(fhirPolicy), 0o600)
	if r = run(env, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, "policy", "activate", "fhir-observations", "--version", "1", "--mode", "enforce", "--reason", "FHIR Observations", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "connectors", "fhir-hapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cf := filepath.Join(t.TempDir(), "fhir-hapi.yaml")
	_ = os.WriteFile(cf, bytes.Replace(src, []byte("http://127.0.0.1:18093"), []byte("http://"+base), 1), 0o600)
	if r = run(env, "connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	if r = run(env, "connector", "test", "fhir"); r.code != 0 {
		t.Fatalf("connector test: %+v", r)
	}
	t.Logf("\n$ deedglass connector test fhir\n%s", r.stdout)
	call := func(key, method, p, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), method, server+p, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := call(admin, "POST", "/v1/agents", `{"name":"charting-agent"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(admin, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	agentEnv := map[string]string{}
	for k, v := range env {
		agentEnv[k] = v
	}
	agentEnv["DEEDGLASS_API_KEY"] = ak.Secret
	const coding = `"code":{"coding":[{"system":"http://loinc.org","code":"8867-4","display":"Heart rate"}]},"subject":{"reference":"Patient/dg-synthetic-1"}`
	vq := func(v int) string {
		return fmt.Sprintf(`"value_quantity":{"value":%d,"unit":"beats/minute","system":"http://unitsofmeasure.org","code":"/min"}`, v)
	}

	// 1. Create: allowed, run, read back. Checked · Matches.
	r = run(agentEnv, "act", "fhir.create_observation", "--args", `{"resource_type":"Observation","status":"preliminary",`+coding+`,`+vq(72)+`}`, "--json")
	var act struct {
		ID        string `json:"id"`
		Execution struct {
			Result struct {
				Body struct {
					ID string `json:"id"`
				} `json:"body"`
			} `json:"result"`
		} `json:"execution"`
		Witness struct {
			Level, Verdict, Method string
		} `json:"witness"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &act) != nil || act.Execution.Result.Body.ID == "" ||
		act.Witness.Level != "L1" || act.Witness.Verdict != "VERIFIED" || act.Witness.Method != "readback.rest.get_observation" {
		t.Fatalf("create: %+v", r)
	}
	id := act.Execution.Result.Body.ID
	if code, out := direct("GET", "/Observation/"+id, ""); code != 200 || out["status"] != "preliminary" || out["meta"].(map[string]any)["versionId"] != "1" {
		t.Fatalf("HAPI after create: %d %v", code, out)
	}

	// 2. Update: allowed, run, read back. The plain output is what docs/guides/fhir-server.md shows.
	upd := func(status string, v int, bodyID string) string {
		return fmt.Sprintf(`{"id":%q,"resource_id":%q,"resource_type":"Observation","status":%q,%s,%s}`, id, bodyID, status, coding, vq(v))
	}
	r = run(agentEnv, "act", "fhir.update_observation", "--args", upd("final", 74, id))
	if r.code != 0 || !strings.Contains(r.stdout, "✓ executed · fhir.update_observation") ||
		!strings.Contains(r.stdout, "check    Checked · Matches · Read back fhir.get_observation") {
		t.Fatalf("update: %+v", r)
	}
	t.Logf("\n$ deedglass act fhir.update_observation --args '{… \"status\":\"final\" …}'\n%s", r.stdout)
	if code, out := direct("GET", "/Observation/"+id, ""); code != 200 || out["status"] != "final" || out["meta"].(map[string]any)["versionId"] != "2" {
		t.Fatalf("HAPI after update: %d %v", code, out)
	}

	// 3. entered-in-error: denied by the policy before any request leaves.
	r = run(agentEnv, "act", "fhir.update_observation", "--args", upd("entered-in-error", 74, id))
	if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "Ask a clinician to correct it") {
		t.Fatalf("deny: %+v", r)
	}
	t.Logf("\n$ deedglass act fhir.update_observation --args '{… \"status\":\"entered-in-error\" …}'\n%s%s", r.stdout, r.stderr)

	// 4. The body id differs from the path: HAPI refuses it (400), so the action failed and nothing changed.
	r = run(agentEnv, "act", "fhir.update_observation", "--args", upd("amended", 80, "not-"+id))
	if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "upstream_error") {
		t.Fatalf("id mismatch: %+v", r)
	}
	t.Logf("\n$ deedglass act fhir.update_observation --args '{\"id\":\"%s\",\"resource_id\":\"not-%s\", …}'\n%s%s", id, id, r.stdout, r.stderr)
	if code, out := direct("GET", "/Observation/"+id, ""); code != 200 || out["status"] != "final" || out["meta"].(map[string]any)["versionId"] != "2" {
		t.Fatalf("HAPI after the refused calls: %d %v", code, out)
	}
	// HAPI's own history has the two versions Deedglass let through, and no others.
	if code, out := direct("GET", "/Observation/"+id+"/_history", ""); code != 200 || fmt.Sprint(out["total"]) != "2" {
		t.Fatalf("history: %d %v", code, out)
	}

	// 5. Every action is sealed; the record shows the decisions and the checks.
	r = run(env, "actions", "list", "--json")
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &list) != nil || len(list.Data) != 4 {
		t.Fatalf("list: %+v", r)
	}
	bundle := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if br := run(env, "deeds", "export", "--out", bundle); br.code != 0 {
		t.Fatalf("export bundle: %+v", br)
	}
	if vr := run(env, "verify", bundle); vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
}

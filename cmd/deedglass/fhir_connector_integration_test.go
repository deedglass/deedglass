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

// fhirConnectorPolicy gates the native fhir connector (HC-P02): reads that build a request are
// allowed, writes to vital-sign Observations must be checked (L1), anything that builds no request
// (a type outside allowed_resource_types) falls to the default deny.
const fhirConnectorPolicy = `deedglass: policy/v1
name: fhir-native
description: The native FHIR connector on HAPI FHIR JPA (HC-P02).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: fhir-reads
    match: { connector_type: fhir, side_effect: read }
    when: request.fhir.enriched
    decision: allow
  - id: fhir-writes
    match: { tool: ["fhir.create", "fhir.update", "fhir.patch"] }
    when: request.fhir.enriched && request.fhir.resource_type == "Observation"
    decision: allow
    verify: L1
`

// TestFHIRConnectorLive is HC-P02 on a real FHIR server: HAPI FHIR JPA (make fhir-it starts it in
// Docker and sets DEEDGLASS_IT_FHIR_URL) behind the native fhir connector, a real serve on a
// throwaway Postgres. A create, an update and a patch are checked at the version HAPI reported
// (Checked · Matches); a conditional create HAPI answers 200 is Doesn't match
// (fhir_conditional_matched, I01); an update that changes nothing keeps HAPI's version and carries
// the note fhir_no_new_version (I02); a stale If-Match is HAPI's 409, failed as
// fhir_version_conflict and never retried (I09); a type outside allowed_resource_types is denied
// and never reaches HAPI (I10). HAPI's own history shows exactly the versions Deedglass let through.
func TestFHIRConnectorLive(t *testing.T) {
	fhirURL := strings.TrimSuffix(os.Getenv("DEEDGLASS_IT_FHIR_URL"), "/")
	if fhirURL == "" {
		t.Skip("DEEDGLASS_IT_FHIR_URL is not set (make fhir-it starts HAPI FHIR JPA and sets it)")
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
	if code, out := direct("PUT", "/Patient/dg-synthetic-2", `{"resourceType":"Patient","id":"dg-synthetic-2","name":[{"family":"Synthetic","given":["Native"]}]}`); code != 200 && code != 201 {
		t.Fatalf("seed patient: %d %v", code, out)
	}
	_, enc := direct("GET", "/Encounter?_summary=count", "")

	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 12)
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
	pf := filepath.Join(t.TempDir(), "fhir-native.yaml")
	_ = os.WriteFile(pf, []byte(fhirConnectorPolicy), 0o600)
	if r = run(env, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, "policy", "activate", "fhir-native", "--version", "1", "--mode", "enforce", "--reason", "FHIR", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "connectors", "fhir.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cf := filepath.Join(t.TempDir(), "fhir.yaml")
	_ = os.WriteFile(cf, bytes.Replace(src, []byte("http://127.0.0.1:18093/fhir"), []byte(fhirURL), 1), 0o600)
	if r = run(env, "connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	if r = run(env, "connector", "test", "fhir"); r.code != 0 || !strings.Contains(r.stdout, "FHIR 4.0.1") {
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
	type action struct {
		ID        string `json:"id"`
		Status    string `json:"status"`
		Execution struct {
			ErrorCode string `json:"error_code"`
			Result    struct {
				Status int `json:"status"`
				Body   struct {
					ID string `json:"id"`
				} `json:"body"`
			} `json:"result"`
		} `json:"execution"`
		Witness struct {
			Level, Verdict, Method string
			Reasons                []struct{ Code, Message string }
			Detail                 json.RawMessage
		} `json:"witness"`
	}
	act := func(tool string, args map[string]any) (action, result) {
		t.Helper()
		b, _ := json.Marshal(args)
		r := run(agentEnv, "act", "fhir."+tool, "--args", string(b), "--json")
		var a action
		_ = json.Unmarshal([]byte(r.stdout), &a)
		if a.ID != "" {
			// The full record, with the witness reasons.
			if code, body := call(admin, "GET", "/v1/actions/"+a.ID+"?expand[]=witness.detail", ""); code == 200 {
				_ = json.Unmarshal([]byte(body), &a)
			}
		}
		return a, r
	}
	codes := func(a action) string { return string(a.Witness.Detail) } // every reason code, notes included
	ident := fmt.Sprintf("hcp02-%d", time.Now().UnixNano())
	obs := func(status string, v int) map[string]any {
		return map[string]any{"resourceType": "Observation", "status": status,
			"identifier":    []any{map[string]any{"system": "urn:deedglass:test", "value": ident}},
			"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}}},
			"subject":       map[string]any{"reference": "Patient/dg-synthetic-2"},
			"valueQuantity": map[string]any{"value": v, "unit": "beats/minute", "system": "http://unitsofmeasure.org", "code": "/min"}}
	}

	// 1. Create: checked at the version HAPI reported.
	a, rr := act("create", map[string]any{"resource_type": "Observation", "resource": obs("preliminary", 72)})
	if a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L1" || a.Witness.Method != "readback.fhir.create" || a.Execution.Result.Body.ID == "" {
		t.Fatalf("create: %+v\n%+v", a, rr)
	}
	id := a.Execution.Result.Body.ID
	history := func() int {
		_, out := direct("GET", "/Observation/"+id+"/_history", "")
		var n int
		fmt.Sscan(fmt.Sprint(out["total"]), &n)
		return n
	}
	// 2. I01: the same create, conditional on the identifier: HAPI answers 200 with the existing
	// resource. Nothing was created, so the action doesn't match.
	a, rr = act("create", map[string]any{"resource_type": "Observation", "resource": obs("preliminary", 72), "if_none_exist": "identifier=urn:deedglass:test|" + ident})
	if a.Witness.Verdict != "MISMATCH" || !strings.Contains(codes(a), "fhir_conditional_matched") || a.Execution.Result.Status != 200 || history() != 1 {
		t.Fatalf("conditional create: %+v (history %d)\n%+v", a, history(), rr)
	}
	t.Logf("I01: %s", a.Witness.Reasons[0].Message)
	// 3. Update with the version read: a new version, checked.
	a, rr = act("update", map[string]any{"resource_type": "Observation", "id": id, "resource": obs("final", 74), "if_match": "1"})
	if a.Witness.Verdict != "VERIFIED" || history() != 2 {
		t.Fatalf("update: %+v\n%+v", a, rr)
	}
	// 4. I02: the same update again: HAPI keeps version 2; the note names it.
	a, rr = act("update", map[string]any{"resource_type": "Observation", "id": id, "resource": obs("final", 74)})
	if a.Witness.Verdict != "VERIFIED" || !strings.Contains(codes(a), "fhir_no_new_version") || history() != 2 {
		t.Fatalf("no-change update: %+v\n%+v", a, rr)
	}
	// 5. I09: a stale If-Match: HAPI's 409 is fhir_version_conflict; failed, nothing written.
	a, rr = act("update", map[string]any{"resource_type": "Observation", "id": id, "resource": obs("amended", 80), "if_match": "1"})
	if a.Status != "failed" || a.Execution.ErrorCode != "fhir_version_conflict" || history() != 2 {
		t.Fatalf("stale If-Match: %+v\n%+v", a, rr)
	}
	// 6. A JSON Patch: checked at the path it replaces.
	a, rr = act("patch", map[string]any{"resource_type": "Observation", "id": id, "json_patch": []any{map[string]any{"op": "replace", "path": "/status", "value": "amended"}}})
	if a.Witness.Verdict != "VERIFIED" || history() != 3 {
		t.Fatalf("patch: %+v\n%+v", a, rr)
	}
	// 7. I10: a type outside allowed_resource_types builds no request: denied, nothing sent.
	a, rr = act("create", map[string]any{"resource_type": "Encounter", "resource": map[string]any{"resourceType": "Encounter", "status": "planned"}})
	if a.Status != "denied" {
		t.Fatalf("Encounter: %+v\n%+v", a, rr)
	}
	if _, enc2 := direct("GET", "/Encounter?_summary=count", ""); fmt.Sprint(enc2["total"]) != fmt.Sprint(enc["total"]) {
		t.Fatalf("an Encounter reached HAPI: %v → %v", enc["total"], enc2["total"])
	}
	// 8. A read is allowed and never read back.
	if a, rr = act("read", map[string]any{"resource_type": "Observation", "id": id}); a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L0" {
		t.Fatalf("read: %+v\n%+v", a, rr)
	}

	// The plain output docs/guides/fhir-server.md shows: the conditional create and the stale If-Match.
	cond, _ := json.Marshal(map[string]any{"resource_type": "Observation", "resource": obs("preliminary", 72), "if_none_exist": "identifier=urn:deedglass:test|" + ident})
	r = run(agentEnv, "act", "fhir.create", "--args", string(cond))
	if !strings.Contains(r.stdout+r.stderr, "conditional create matched an existing resource") {
		t.Fatalf("plain conditional create: %+v", r)
	}
	t.Logf("\n$ deedglass act fhir.create --args '{… \"if_none_exist\":\"identifier=urn:deedglass:test|…\" …}'\n%s%s", r.stdout, r.stderr)
	stale, _ := json.Marshal(map[string]any{"resource_type": "Observation", "id": id, "resource": obs("amended", 81), "if_match": "1"})
	r = run(agentEnv, "act", "fhir.update", "--args", string(stale))
	if !strings.Contains(r.stdout+r.stderr, "fhir_version_conflict") {
		t.Fatalf("plain stale If-Match: %+v", r)
	}
	t.Logf("\n$ deedglass act fhir.update --args '{… \"if_match\":\"1\" …}'\n%s%s", r.stdout, r.stderr)

	// Every action is sealed; the export verifies.
	bundle := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if br := run(env, "deeds", "export", "--out", bundle); br.code != 0 {
		t.Fatalf("export bundle: %+v", br)
	}
	if vr := run(env, "verify", bundle); vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
}

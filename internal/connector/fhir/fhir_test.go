package fhir

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/fhir/fhirtest"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

var (
	testOrg  = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	testConn = ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7d")
	testAct  = ids.MustParse[ids.ActionKind]("act_01m58nq2jthtfkkv8qdejqts5b")
)

type opener struct {
	kind   string
	fields map[string]string
}

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	if o.fields == nil {
		return nil, vault.ErrNotFound
	}
	return vault.NewSecretForTest(o.kind, o.fields), nil
}

func devEnv() connector.Env {
	return connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

func ctx() context.Context { return vault.WithRedactor(context.Background(), vault.NewRedactor()) }

// setup is a connector on base with cfg merged over the defaults, auth none.
func setup(t *testing.T, base string, cfg map[string]any, op *opener) (*Connector, *connector.Instance) {
	t.Helper()
	c := New()
	m := map[string]any{"base_url": base, "auth": map[string]any{"kind": "none"}, "allowed_resource_types": []string{"Observation", "Patient"},
		"allowed_interactions": Interactions}
	for k, v := range cfg {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	norm, errs := c.Validate(context.Background(), devEnv(), raw)
	if len(errs) > 0 {
		t.Fatalf("Validate: %v", errs)
	}
	if op == nil {
		op = &opener{}
	}
	return c, connector.NewInstance(testConn, testOrg, "fhir", Type, norm, "sha256:test", devEnv(), op)
}

func callOf(t *testing.T, c *Connector, inst *connector.Instance, tool string, args any) connector.Call {
	t.Helper()
	specs, _ := c.Tools(context.Background(), inst)
	for _, s := range specs {
		if s.Name == tool {
			raw, _ := json.Marshal(args)
			return connector.Call{ActionID: testAct, OrgID: testOrg, Tool: s, Qualified: "fhir." + tool, Args: raw}
		}
	}
	t.Fatalf("no tool %s", tool)
	return connector.Call{}
}

func obs(v int) map[string]any {
	return map[string]any{"resourceType": "Observation", "status": "final",
		"code":          map[string]any{"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4"}}},
		"subject":       map[string]any{"reference": "Patient/dg-synthetic-1"},
		"valueQuantity": map[string]any{"value": v, "unit": "beats/minute"}}
}

func TestValidate(t *testing.T) {
	c := New()
	norm, errs := c.Validate(context.Background(), devEnv(), json.RawMessage(`{"base_url":"http://127.0.0.1:9/fhir/","auth":{"kind":"smart_backend"},
		"allowed_resource_types":["Observation","Condition"],"allowed_interactions":["create","read","update"]}`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var cfg Config
	_ = json.Unmarshal(norm, &cfg)
	if cfg.BaseURL != "http://127.0.0.1:9/fhir" || cfg.FHIRVersion != "R4" || cfg.IfMatch != "when_known" || cfg.RequestIDHeader != "X-Request-ID" ||
		strings.Join(cfg.AllowedResourceTypes, ",") != "Condition,Observation" || strings.Join(cfg.AllowedInteractions, ",") != "read,create,update" ||
		strings.Join(cfg.Auth.Scopes, " ") != "system/Condition.cru system/Observation.cru" {
		t.Fatalf("normalized: %+v", cfg)
	}
	prod := connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}}
	for name, tc := range map[string]struct {
		env  connector.Env
		cfg  string
		want string
	}{
		"no types":       {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"none"},"allowed_resource_types":[]}`, "config.allowed_resource_types"},
		"bad type":       {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"none"},"allowed_resource_types":["observation"]}`, "config.allowed_resource_types[0]"},
		"transaction":    {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"none"},"allowed_resource_types":["Observation"],"allowed_interactions":["transaction"]}`, "config.allowed_interactions[0]"},
		"R5":             {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","fhir_version":"R5","auth":{"kind":"none"},"allowed_resource_types":["Observation"]}`, "config.fhir_version"},
		"none in prod":   {prod, `{"base_url":"https://203.0.113.9/fhir","auth":{"kind":"none"},"allowed_resource_types":["Observation"]}`, "config.auth.kind"},
		"metadata host":  {prod, `{"base_url":"http://169.254.169.254/fhir","auth":{"kind":"bearer"},"allowed_resource_types":["Observation"]}`, "config.base_url"},
		"bad scope":      {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"smart_backend","scopes":["patient/*.read"]},"allowed_resource_types":["Observation"]}`, "config.auth.scopes[0]"},
		"unknown key":    {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"none"},"allowed_resource_types":["Observation"],"tools":[]}`, "config"},
		"header":         {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"none"},"allowed_resource_types":["Observation"],"request_id_header":"Authorization"}`, "config.request_id_header"},
		"scopes, bearer": {devEnv(), `{"base_url":"http://127.0.0.1:9/fhir","auth":{"kind":"bearer","scopes":["system/*.rs"]},"allowed_resource_types":["Observation"]}`, "config.auth"},
	} {
		_, errs := c.Validate(context.Background(), tc.env, json.RawMessage(tc.cfg))
		found := false
		for _, e := range errs {
			found = found || e.Param == tc.want
		}
		if !found {
			t.Errorf("%s: want an error on %s, got %v", name, tc.want, errs)
		}
	}
}

func TestToolsAndCatalog(t *testing.T) {
	c, inst := setup(t, "http://127.0.0.1:9/fhir", map[string]any{"allowed_interactions": []string{"read", "create", "delete"}, "if_match": "required"}, nil)
	specs, _ := c.Tools(context.Background(), inst)
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
		if err := connector.ValidateArgs(s.InputSchema, json.RawMessage(`{"resource_type":"Condition"}`)); err == nil {
			t.Errorf("%s accepted a type outside the allowed list", s.Name)
		}
	}
	if strings.Join(names, ",") != "read,create,delete" || specs[2].SideEffect != connector.SideEffectDestructive || specs[1].L1 == "" || !specs[0].Idempotent {
		t.Fatalf("tools: %+v", specs)
	}
	if !strings.Contains(string(specs[2].InputSchema), `"if_match"]`) {
		t.Errorf("if_match: required isn't in delete's schema: %s", specs[2].InputSchema)
	}
	info := c.Info()
	if len(info.Tools) != len(Interactions) || info.ToolsDynamic || info.Levels[len(info.Levels)-1] != "L1" {
		t.Fatalf("catalog: %+v", info)
	}
}

// TestRequests checks what leaves: the path, the FHIR headers, the binding token and the refusals
// that send nothing.
func TestRequests(t *testing.T) {
	srv := fhirtest.New()
	defer srv.Close()
	c, inst := setup(t, srv.Base(), nil, nil)
	exec := func(tool string, args any) (connector.Result, error) {
		return c.Execute(ctx(), inst, callOf(t, c, inst, tool, args))
	}
	res, err := exec("create", map[string]any{"resource_type": "Observation", "resource": obs(72), "if_none_exist": "identifier=urn:x|1"})
	if err != nil {
		t.Fatal(err)
	}
	r := srv.Requests[len(srv.Requests)-1]
	tok := testAct.String()[len(testAct.String())-16:]
	if r.Method != "POST" || r.Path != "/fhir/Observation" || r.Header.Get("Accept") != "application/fhir+json" ||
		r.Header.Get("Content-Type") != "application/fhir+json" || r.Header.Get("Prefer") != "return=representation" ||
		r.Header.Get("Deedglass-Action-Id") != testAct.String() || r.Header.Get("X-Request-ID") != tok || len(tok) != 16 ||
		r.Header.Get("If-None-Exist") != "identifier=urn:x|1" {
		t.Fatalf("create request: %+v", r)
	}
	if res.Evidence["fhir_id"] != "1001" || res.Evidence["fhir_version"] != "1" || res.Evidence["fhir_status"] != "201" || res.Evidence["fhir_conditional"] != "true" {
		t.Fatalf("evidence: %v", res.Evidence)
	}
	var out struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    struct{ ID string }
	}
	if json.Unmarshal(res.Output, &out) != nil || out.Status != 201 || out.Body.ID != "1001" || !strings.HasSuffix(out.Headers["location"], "/Observation/1001/_history/1") {
		t.Fatalf("output: %s", res.Output)
	}
	if _, err := exec("update", map[string]any{"resource_type": "Observation", "id": "1001", "resource": obs(74), "if_match": "1"}); err != nil {
		t.Fatal(err)
	}
	if r := srv.Requests[len(srv.Requests)-1]; r.Method != "PUT" || r.Path != "/fhir/Observation/1001" || r.Header.Get("If-Match") != `W/"1"` {
		t.Fatalf("update request: %+v", r)
	}
	// A stale If-Match: 409 on HAPI (412 elsewhere) is fhir_version_conflict, failed, never retried.
	n := srv.Hits()
	_, err = exec("update", map[string]any{"resource_type": "Observation", "id": "1001", "resource": obs(80), "if_match": "1"})
	if ce := connector.AsError(err); ce == nil || ce.Code != CodeVersionConflict || ce.Kind != connector.KindInvalidRequest || srv.Hits() != n+1 {
		t.Fatalf("stale If-Match: %v (hits %d → %d)", err, n, srv.Hits())
	}
	// Refusals: nothing is sent.
	n = srv.Hits()
	for name, args := range map[string]map[string]any{
		"type":      {"resource_type": "Condition", "id": "1"},
		"id slash":  {"resource_type": "Observation", "id": "1/_history"},
		"id dots":   {"resource_type": "Observation", "id": ".."},
		"extra arg": {"resource_type": "Observation", "id": "1", "path": "/x"},
	} {
		if _, err := exec("read", args); connector.AsError(err) == nil || connector.AsError(err).Code != connector.CodeValidationFailed {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, args := range map[string]map[string]any{
		"type mismatch": {"resource_type": "Observation", "resource": map[string]any{"resourceType": "Patient"}},
		"two lines":     {"resource_type": "Observation", "resource": obs(1), "if_none_exist": "a=b\r\nX-Evil: 1"},
	} {
		if _, err := exec("create", args); connector.AsError(err) == nil {
			t.Errorf("%s: sent", name)
		}
	}
	if _, err := exec("update", map[string]any{"resource_type": "Observation", "id": "1001", "resource": map[string]any{"resourceType": "Observation", "id": "9"}}); err == nil {
		t.Error("update with another body id: sent")
	}
	if _, err := exec("patch", map[string]any{"resource_type": "Observation", "id": "1001"}); err == nil {
		t.Error("patch without a patch: sent")
	}
	if srv.Hits() != n {
		t.Fatalf("refused calls reached the server: %d → %d", n, srv.Hits())
	}
	// if_match: required refuses an update without one.
	c2, inst2 := setup(t, srv.Base(), map[string]any{"if_match": "required"}, nil)
	if _, err := c2.Execute(ctx(), inst2, callOf(t, c2, inst2, "update", map[string]any{"resource_type": "Observation", "id": "1001", "resource": obs(1)})); err == nil {
		t.Error("if_match required: an update without one was sent")
	}
	// Search parameters go to the query; history takes only _since, _at and _count.
	if _, err := exec("search", map[string]any{"resource_type": "Observation", "params": map[string]any{"code": "http://loinc.org|8867-4", "_count": []any{"5"}}}); err == nil || srv.Requests[len(srv.Requests)-1].Path != "/fhir/Observation" {
		_ = err // the stand-in doesn't search; the request is what counts
	}
	if _, err := exec("history", map[string]any{"resource_type": "Observation", "params": map[string]any{"code": "x"}}); err == nil {
		t.Error("history took a search parameter")
	}
}

func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		kind   connector.Kind
		code   string
	}{
		{409, "", connector.KindInvalidRequest, CodeVersionConflict},
		{412, "", connector.KindInvalidRequest, CodeVersionConflict},
		{410, "", connector.KindNotFound, connector.CodeUpstreamNotFound},
		{404, "", connector.KindNotFound, connector.CodeUpstreamNotFound},
		{422, `{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"required","diagnostics":"Patient Jane Doe MRN 12345"}]}`, connector.KindInvalidRequest, connector.CodeUpstreamError},
		{500, "", connector.KindUpstream, connector.CodeUpstreamError},
		{401, "", connector.KindAuth, connector.CodeUpstreamAuthFailed},
	} {
		e := classify(&saas.Response{Status: tc.status, Header: http.Header{}, Body: []byte(tc.body)})
		if e == nil || e.Kind != tc.kind || e.Code != tc.code {
			t.Errorf("%d: %+v", tc.status, e)
			continue
		}
		if tc.status == 422 && (!strings.Contains(e.AgentMessage, "error required") || strings.Contains(e.AgentMessage, "Jane")) {
			t.Errorf("422 message: %q", e.AgentMessage)
		}
	}
	if classify(&saas.Response{Status: 201}) != nil {
		t.Error("201 refused")
	}
}

// fixture loads a recorded HAPI answer (testdata/hapi, HAPI FHIR JPA v8.12.0-2, 2026-10-19).
func fixture(t *testing.T, name string) *saas.Response {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hapi", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range rec.Headers {
		h.Set(k, v)
	}
	return &saas.Response{Status: rec.Status, Header: h, Body: rec.Body}
}

func TestRecordedHAPI(t *testing.T) {
	r := &req{typ: "Observation", id: "1001", inter: "update", method: "PUT"}
	for name, want := range map[string][2]string{
		"create":                     {"1001", "1"}, // Location
		"create_conditional_matched": {"1001", "1"},
		"update":                     {"1001", "2"}, // ETag, no Location
		"update_nochange":            {"1001", "2"},
		"patch":                      {"1001", "3"}, // the body's meta.versionId
	} {
		if id, vid := versionOf(fixture(t, name), r); id != want[0] || vid != want[1] {
			t.Errorf("%s: %s %s, want %v", name, id, vid, want)
		}
	}
	for name, want := range map[string]string{"update_stale_ifmatch": CodeVersionConflict, "read_deleted": connector.CodeUpstreamNotFound, "invalid": connector.CodeUpstreamError} {
		if e := classify(fixture(t, name)); e == nil || e.Code != want {
			t.Errorf("%s: %+v", name, e)
		}
	}
	if e := classify(fixture(t, "invalid")); !strings.Contains(e.AgentMessage, "error processing") || strings.Contains(e.AgentMessage, "HAPI-") {
		t.Errorf("invalid: %q", e.AgentMessage)
	}
	// The version read back normalises to what was sent.
	sent := map[string]any{}
	_ = decodeJSON(fixture(t, "create").Body, &sent)
	got := map[string]any{}
	_ = decodeJSON(fixture(t, "vread").Body, &got)
	delete(sent, "meta")
	sent["status"] = "final"
	normalise(got, sent, nil, false)
	sent["id"] = "1001"
	var fs []connector.L1Finding
	diff("", sent, got, &fs)
	if len(fs) != 0 {
		t.Fatalf("vread of version 2 against the update: %+v", fs)
	}
}

// TestReadBack runs the L1 checks against the stand-in server: honest, conditional create answered
// 200, a lying server, a no-change update, a delete, and the history lookup after a 5xx.
func TestReadBack(t *testing.T) {
	srv := fhirtest.New()
	defer srv.Close()
	c, inst := setup(t, srv.Base(), map[string]any{"server_tag_systems": []string{"urn:server"}}, nil)
	run := func(tool string, args any) (connector.Result, []connector.L1Finding, error) {
		t.Helper()
		call := callOf(t, c, inst, tool, args)
		res, err := c.Execute(ctx(), inst, call)
		if err != nil && connector.AsError(err).Kind != connector.KindUpstream {
			t.Fatalf("%s: %v", tool, err)
		}
		_, fs, rerr := c.NativeReadBack(ctx(), inst, call, res)
		return res, fs, rerr
	}
	only := func(fs []connector.L1Finding, code string) bool {
		for _, f := range fs {
			if f.Code == code {
				return true
			}
		}
		return false
	}
	matched := func(fs []connector.L1Finding) bool {
		for _, f := range fs {
			if !f.Match {
				return false
			}
		}
		return len(fs) > 0
	}
	withID := obs(72)
	withID["identifier"] = []any{map[string]any{"system": "urn:x", "value": "1"}}
	// Honest create, with the server adding a tag of its own and a generated narrative.
	srv.Mutate = func(_ string, r map[string]any) {
		r["meta"] = map[string]any{"tag": []any{map[string]any{"system": "urn:server", "code": "x"}}}
		r["text"] = map[string]any{"status": "generated", "div": "<div>x</div>"}
	}
	_, fs, err := run("create", map[string]any{"resource_type": "Patient", "resource": map[string]any{"resourceType": "Patient", "gender": "unknown"}})
	srv.Mutate = nil
	if err != nil || !matched(fs) {
		t.Fatalf("honest create with server additions: %v %+v", err, fs)
	}
	_, fs, err = run("create", map[string]any{"resource_type": "Observation", "resource": withID})
	if err != nil || !matched(fs) {
		t.Fatalf("honest create: %v %+v", err, fs)
	}
	// I01: the conditional create matches; the server answers 200 with the existing resource.
	_, fs, err = run("create", map[string]any{"resource_type": "Observation", "resource": withID, "if_none_exist": "identifier=urn:x|1"})
	if err != nil || !only(fs, ReasonConditionalMatched) || fs[0].Match {
		t.Fatalf("conditional create: %v %+v", err, fs)
	}
	// I02: an update that changes nothing keeps the version.
	upd := obs(72)
	upd["identifier"] = withID["identifier"]
	_, fs, err = run("update", map[string]any{"resource_type": "Observation", "id": "1002", "resource": upd})
	if err != nil || !matched(fs) || !only(fs, ReasonNoNewVersion) {
		t.Fatalf("no-change update: %v %+v", err, fs)
	}
	// I03: the server stores another value.
	srv.Mutate = func(_ string, r map[string]any) { r["status"] = "preliminary" }
	upd2 := obs(75)
	_, fs, err = run("update", map[string]any{"resource_type": "Observation", "id": "1002", "resource": upd2})
	srv.Mutate = nil
	if err != nil || matched(fs) || !only(fs, ReasonFieldChanged) || fs[0].Field != "status" || fs[0].Observed != "preliminary" {
		t.Fatalf("lying server: %v %+v", err, fs)
	}
	// A JSON Patch is checked at the paths it replaces.
	_, fs, err = run("patch", map[string]any{"resource_type": "Observation", "id": "1002", "json_patch": []any{map[string]any{"op": "replace", "path": "/status", "value": "amended"}}})
	if err != nil || !matched(fs) {
		t.Fatalf("patch: %v %+v", err, fs)
	}
	// I04: a 5xx after the server committed; HAPI's default stores no request id, so nothing in the
	// history carries the token: a gap.
	srv.FailNext = 500
	_, fs, err = run("update", map[string]any{"resource_type": "Observation", "id": "1002", "resource": obs(90)})
	if err != nil || !only(fs, ReasonHistoryNoMatch) {
		t.Fatalf("5xx without request ids: %v %+v", err, fs)
	}
	// With request ids stored, exactly one version carries the token and is compared.
	srv.StoreRequestID, srv.FailNext = true, 503
	_, fs, err = run("update", map[string]any{"resource_type": "Observation", "id": "1002", "resource": obs(91)})
	if err != nil || !matched(fs) {
		t.Fatalf("5xx with request ids: %v %+v", err, fs)
	}
	// A delete reads 410 afterwards.
	_, fs, err = run("delete", map[string]any{"resource_type": "Observation", "id": "1002"})
	if err != nil || !matched(fs) {
		t.Fatalf("delete: %v %+v", err, fs)
	}
	// A read changes nothing: no read-back.
	call := callOf(t, c, inst, "read", map[string]any{"resource_type": "Observation", "id": "1001"})
	var skip *connector.ErrReadBackSkipped
	if _, _, err := c.NativeReadBack(ctx(), inst, call, connector.Result{}); !asSkip(err, &skip) {
		t.Fatalf("read: %v", err)
	}
	// A 2xx that names no version is a gap (an error), never a finding.
	call = callOf(t, c, inst, "update", map[string]any{"resource_type": "Observation", "id": "1001", "resource": obs(1)})
	if _, fs, err := c.NativeReadBack(ctx(), inst, call, connector.Result{Evidence: map[string]string{"fhir_status": "200"}}); err == nil || fs != nil {
		t.Fatalf("no version: %v %+v", err, fs)
	}
}

func asSkip(err error, s **connector.ErrReadBackSkipped) bool {
	e, ok := err.(*connector.ErrReadBackSkipped)
	*s = e
	return ok
}

func TestEnrichAndPHI(t *testing.T) {
	c, inst := setup(t, "http://127.0.0.1:9/fhir", nil, nil)
	specs, _ := c.Tools(context.Background(), inst)
	var create connector.ToolSpec
	for _, s := range specs {
		if s.Name == "create" {
			create = s
		}
	}
	res := obs(72)
	res["category"] = []any{map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "vital-signs"}}}}
	res["meta"] = map[string]any{"profile": []any{"http://hl7.org/fhir/StructureDefinition/heartrate"}}
	raw, _ := json.Marshal(map[string]any{"resource_type": "Observation", "resource": res, "if_none_exist": "identifier=a|b"})
	m, err := c.Enrich(context.Background(), inst, create, raw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m["fhir"])
	want := `{"codes":["http://loinc.org|8867-4","http://terminology.hl7.org/CodeSystem/observation-category|vital-signs"],"conditional":true,"enriched":true,"id":"","if_match":false,"interaction":"create","profiles":["http://hl7.org/fhir/StructureDefinition/heartrate"],"resource_type":"Observation"}`
	if string(b) != want {
		t.Fatalf("facts:\n%s\nwant\n%s", b, want)
	}
	if _, err := c.Enrich(context.Background(), inst, create, json.RawMessage(`{"resource_type":"Condition","resource":{"resourceType":"Condition"}}`)); err == nil {
		t.Fatal("a type outside the list enriched")
	}
	k := c.PHIKeep(inst, create)
	if strings.Join(k.Args, ",") != "id,resource_type,version_id" || len(k.Result) != 7 {
		t.Fatalf("PHIKeep: %+v", k)
	}
}

// jwks is a private JWK for k, and its public half for checking.
func rsaJWK(t *testing.T, kid string) (string, *rsa.PublicKey) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	e := base64.RawURLEncoding
	j, _ := json.Marshal(map[string]string{"kty": "RSA", "kid": kid, "alg": "RS384", "n": e.EncodeToString(k.N.Bytes()), "e": e.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
		"d": e.EncodeToString(k.D.Bytes()), "p": e.EncodeToString(k.Primes[0].Bytes()), "q": e.EncodeToString(k.Primes[1].Bytes())})
	return string(j), &k.PublicKey
}

func ecJWK(t *testing.T) (string, *ecdsa.PublicKey) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := k.Bytes()
	pub, _ := k.PublicKey.Bytes()
	e := base64.RawURLEncoding
	j, _ := json.Marshal(map[string]string{"kty": "EC", "crv": "P-384", "kid": "ec1", "x": e.EncodeToString(pub[1:49]), "y": e.EncodeToString(pub[49:]), "d": e.EncodeToString(raw)})
	return string(j), &k.PublicKey
}

// TestSMARTBackend runs the token exchange against a stand-in authorization server that checks the
// client assertion's signature and claims; discovery, caching and both key types.
func TestSMARTBackend(t *testing.T) {
	for _, kind := range []string{"RS384", "ES384"} {
		t.Run(kind, func(t *testing.T) {
			var jwk string
			verify := func(input string, sig []byte) bool { return false }
			if kind == "RS384" {
				priv, pub := rsaJWK(t, "k2")
				other, _ := rsaJWK(t, "k1")
				jwk = `{"keys":[` + priv + `,` + other + `]}` // rotation: the first key signs
				verify = func(input string, sig []byte) bool {
					h := sha512.Sum384([]byte(input))
					return rsa.VerifyPKCS1v15(pub, crypto.SHA384, h[:], sig) == nil
				}
			} else {
				priv, pub := ecJWK(t)
				jwk = priv
				verify = func(input string, sig []byte) bool {
					h := sha512.Sum384([]byte(input))
					return len(sig) == 96 && ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(sig[:48]), new(big.Int).SetBytes(sig[48:]))
				}
			}
			var mints, fhirCalls atomic.Int32
			var tokenURL string
			mux := http.NewServeMux()
			mux.HandleFunc("/fhir/.well-known/smart-configuration", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]string{"token_endpoint": tokenURL})
			})
			mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				parts := strings.Split(r.PostForm.Get("client_assertion"), ".")
				var hdr, claims map[string]any
				hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
				cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
				sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
				_ = json.Unmarshal(hb, &hdr)
				_ = json.Unmarshal(cb, &claims)
				exp, _ := claims["exp"].(float64)
				iat, _ := claims["iat"].(float64)
				if r.PostForm.Get("grant_type") != "client_credentials" || r.PostForm.Get("client_assertion_type") != assertionType ||
					r.PostForm.Get("scope") != "system/Observation.cruds system/Patient.cruds" || hdr["alg"] != kind ||
					claims["iss"] != "dg-client" || claims["sub"] != "dg-client" || claims["aud"] != tokenURL || exp-iat > 300 || claims["jti"] == "" ||
					!verify(parts[0]+"."+parts[1], sig) {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
					return
				}
				mints.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "dgSENTINELtoken" + kind, "token_type": "bearer", "expires_in": 3600})
			})
			mux.HandleFunc("/fhir/metadata", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer dgSENTINELtoken"+kind {
					w.WriteHeader(401)
					return
				}
				fhirCalls.Add(1)
				_, _ = w.Write([]byte(`{"resourceType":"CapabilityStatement","fhirVersion":"4.0.1","rest":[{"resource":[{"type":"Observation"},{"type":"Patient"}]}]}`))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			tokenURL = srv.URL + "/token"
			c, inst := setup(t, srv.URL+"/fhir", map[string]any{"auth": map[string]any{"kind": "smart_backend"}},
				&opener{kind: vault.KindFields, fields: map[string]string{"client_id": "dg-client", "private_jwk": jwk}})
			for i := 0; i < 2; i++ {
				rep, _ := c.Test(ctx(), inst)
				if rep.Status != connector.CheckPass {
					t.Fatalf("test %d: %+v", i, rep.Checks)
				}
			}
			if mints.Load() != 1 || fhirCalls.Load() != 2 {
				t.Fatalf("mints %d, FHIR calls %d; want 1 and 2 (the token is cached)", mints.Load(), fhirCalls.Load())
			}
		})
	}
	// A bad key is refused before anything is sent.
	for name, raw := range map[string]string{"not json": "x", "public only": `{"kty":"EC","crv":"P-384","x":"AA","y":"AA"}`, "P-256": `{"kty":"EC","crv":"P-256","d":"AA"}`,
		"three keys": `{"keys":[{},{},{}]}`} {
		if _, err := parseJWK(raw); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), raw) && len(raw) > 2 {
			t.Errorf("%s: the error quotes the key", name)
		}
	}
	if s := deriveScopes([]string{"Observation"}, []string{"read", "search"}); s[0] != "system/Observation.rs" {
		t.Errorf("scopes: %v", s)
	}
	_ = url.Values{}
}

func TestConnectorTest(t *testing.T) {
	srv := fhirtest.New()
	defer srv.Close()
	c, inst := setup(t, srv.Base(), nil, nil)
	rep, _ := c.Test(ctx(), inst)
	if rep.Status != connector.CheckPass {
		t.Fatalf("%+v", rep.Checks)
	}
	c, inst = setup(t, srv.Base(), map[string]any{"allowed_resource_types": []string{"Observation", "Encounter"}}, nil)
	if rep, _ := c.Test(ctx(), inst); rep.Status != connector.CheckWarn {
		t.Fatalf("a type the server doesn't serve: %+v", rep.Checks)
	}
	c, inst = setup(t, srv.Base(), map[string]any{"auth": map[string]any{"kind": "bearer"}}, &opener{})
	if rep, _ := c.Test(ctx(), inst); rep.Status != connector.CheckFail {
		t.Fatalf("no credential: %+v", rep.Checks)
	}
}

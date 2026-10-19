package rest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/rest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness"
)

// HC-P00: a FHIR R4 server behind the REST connector, with examples/connectors/fhir-hapi.yaml as
// written. A tiny FHIR server stands in for HAPI here (honest, lying, a 5xx on the read); the live
// run against HAPI FHIR JPA is cmd/deedglass TestFHIRRestLive (make fhir-it).

// fhirServer keeps Observations in memory, the way a FHIR server answers create, update and read:
// the server assigns the id, keeps meta.versionId and refuses an update whose body id differs.
type fhirServer struct {
	mu       sync.Mutex
	obs      map[string]map[string]any
	next     int
	lying    bool // an update answers 200 with the new resource and saves nothing
	readCode int  // non-zero: a read answers this status
	lastBody map[string]any
	lastCT   string
	lastAct  string
}

func (f *fhirServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/fhir+json;charset=utf-8")
	outcome := func(code int, msg string) {
		w.WriteHeader(code)
		_, _ = fmt.Fprintf(w, `{"resourceType":"OperationOutcome","issue":[{"severity":"error","code":"processing","diagnostics":%q}]}`, msg)
	}
	id, hasID := strings.CutPrefix(r.URL.Path, "/fhir/Observation/")
	if r.Method != http.MethodGet {
		f.lastCT, f.lastAct, f.lastBody = r.Header.Get("Content-Type"), r.Header.Get("Deedglass-Action-Id"), nil
		if err := json.NewDecoder(r.Body).Decode(&f.lastBody); err != nil {
			outcome(400, "The body isn't JSON.")
			return
		}
		if f.lastBody["resourceType"] != "Observation" {
			outcome(400, "HAPI-0450: resourceType is missing or wrong.")
			return
		}
	}
	switch {
	case r.Method == http.MethodGet && hasID:
		if f.readCode != 0 {
			outcome(f.readCode, "down")
			return
		}
		o, ok := f.obs[id]
		if !ok {
			outcome(404, "HAPI-2001: Resource Observation/"+id+" is not known")
			return
		}
		_ = json.NewEncoder(w).Encode(o)
	case r.Method == http.MethodPost && r.URL.Path == "/fhir/Observation":
		f.next++
		id := strconv.Itoa(f.next)
		o := f.lastBody
		o["id"], o["meta"] = id, map[string]any{"versionId": "1", "lastUpdated": "2026-10-19T10:00:00.000+00:00"}
		f.obs[id] = o
		w.Header().Set("Location", "/fhir/Observation/"+id+"/_history/1")
		w.Header().Set("ETag", `W/"1"`)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(o)
	case r.Method == http.MethodPut && hasID:
		if f.lastBody["id"] != id {
			outcome(400, "HAPI-0420: the resource id in the body doesn't match the URL.")
			return
		}
		old, ok := f.obs[id]
		if !ok {
			outcome(404, "not found")
			return
		}
		v, _ := strconv.Atoi(old["meta"].(map[string]any)["versionId"].(string))
		o := f.lastBody
		o["meta"] = map[string]any{"versionId": strconv.Itoa(v + 1), "lastUpdated": "2026-10-19T10:00:01.000+00:00"}
		if !f.lying {
			f.obs[id] = o
		}
		w.Header().Set("ETag", fmt.Sprintf(`W/"%d"`, v+1))
		_ = json.NewEncoder(w).Encode(o)
	default:
		outcome(405, "not supported here")
	}
}

// fhirRig loads examples/connectors/fhir-hapi.yaml, points it at srv and registers it as "fhir".
func fhirRig(t *testing.T, srv *httptest.Server) *rig {
	t.Helper()
	raw, err := os.ReadFile("../../../examples/connectors/fhir-hapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type   string         `yaml:"type"`
		Name   string         `yaml:"name"`
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil || doc.Type != "rest" || doc.Name != "fhir" {
		t.Fatalf("example: %v %+v", err, doc)
	}
	doc.Config["base_url"] = srv.URL
	cfg, err := json.Marshal(doc.Config)
	if err != nil {
		t.Fatal(err)
	}
	c := rest.New()
	norm, errs := c.Validate(context.Background(), devEnv(), cfg)
	if len(errs) > 0 {
		t.Fatalf("the example doesn't validate: %v", errs)
	}
	hash, _ := rest.Hash(norm)
	row := store.Connector{ID: conID, OrgID: org.String(), Type: "rest", Name: "fhir", Config: norm, ConfigHash: hash, Status: "active"}
	r := &rig{opener: &opener{kind: "bearer", fields: map[string]string{"token": sentinel}}}
	r.reg = connector.New(connector.Options{Types: []connector.Connector{c}, Loader: memLoader{map[string]store.Connector{conID: row}},
		Opener: r.opener, Env: devEnv()})
	return r
}

// fhirCall runs one fhir tool and then the Witness with the tool's read-back, as the Gate does.
func fhirCall(t *testing.T, r *rig, tool, args string) (connector.Result, witness.Outcome) {
	t.Helper()
	ex, spec, _, err := r.reg.Resolve(context.Background(), org, "fhir."+tool)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.Execute(context.Background(), connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: org, Tool: spec,
		Qualified: "fhir." + tool, Args: json.RawMessage(args), Deadline: time.Now().Add(5 * time.Second)})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	sub := witness.Subject{Tool: "fhir." + tool, ConnectorType: "rest", Mode: witness.ModeGateway, Args: json.RawMessage(args),
		Required: witness.L1, Result: witness.ResultView{Status: witness.StatusSucceeded, Output: res.Output, OutputBytes: len(res.Output)},
		Spec: witness.ToolWitnessSpec{Connector: "fhir", Tool: tool, ReadBack: rest.WitnessReadBack(spec.ReadBack), Postcondition: spec.Postcondition}}
	ctx, cancel := witness.WithBudget(context.Background(), 5*time.Second)
	defer cancel()
	out, _, err := witness.Run(ctx, witness.Input{Subject: sub, Observer: gateObserver{r}, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	return res, out
}

const (
	obsCode    = `{"coding":[{"system":"http://loinc.org","code":"8867-4","display":"Heart rate"}]}`
	obsSubject = `{"reference":"Patient/example"}`
)

func createArgs(status string, value float64) string {
	return fmt.Sprintf(`{"resource_type":"Observation","status":%q,"code":%s,"subject":%s,"value_quantity":{"value":%v,"unit":"beats/minute","system":"http://unitsofmeasure.org","code":"/min"}}`,
		status, obsCode, obsSubject, value)
}

func updateArgs(id, status string, value float64) string {
	return fmt.Sprintf(`{"id":%q,"resource_id":%q,"resource_type":"Observation","status":%q,"code":%s,"subject":%s,"value_quantity":{"value":%v,"unit":"beats/minute","system":"http://unitsofmeasure.org","code":"/min"}}`,
		id, id, status, obsCode, obsSubject, value)
}

func TestFHIRCreateAndUpdateReadBack(t *testing.T) {
	f := &fhirServer{obs: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	r := fhirRig(t, srv)

	// Create: the body carries FHIR's own keys (resourceType, valueQuantity), not the arg names.
	res, out := fhirCall(t, r, "create_observation", createArgs("preliminary", 72))
	if f.lastBody["resourceType"] != "Observation" || f.lastBody["valueQuantity"] == nil || f.lastBody["resource_type"] != nil || f.lastBody["value_quantity"] != nil {
		t.Fatalf("the server got %v", f.lastBody)
	}
	if f.lastCT != "application/json" || !strings.HasPrefix(f.lastAct, "act_") {
		t.Fatalf("content type %q, action id %q", f.lastCT, f.lastAct)
	}
	var created struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    struct {
			ID string `json:"id"`
		} `json:"body"`
	}
	if err := json.Unmarshal(res.Output, &created); err != nil || created.Status != 201 || created.Body.ID != "1" ||
		created.Headers["location"] != "/fhir/Observation/1/_history/1" || created.Headers["etag"] != `W/"1"` {
		t.Fatalf("output %s", res.Output)
	}
	if out.Decision.Level != witness.L1 || out.Decision.Verdict != witness.Verified || out.Decision.Method != "readback.rest.get_observation" {
		t.Fatalf("create: %+v", out.Detail)
	}

	// Update, honest: Checked · Matches on status and valueQuantity.value.
	_, out = fhirCall(t, r, "update_observation", updateArgs("1", "final", 72))
	if out.Decision.Level != witness.L1 || out.Decision.Verdict != witness.Verified {
		t.Fatalf("honest update: %+v", out.Detail)
	}

	// Update, lying: the server answers 200 with the new resource and keeps the old one.
	f.lying = true
	_, out = fhirCall(t, r, "update_observation", updateArgs("1", "amended", 75))
	if out.Decision.Level != witness.L1 || out.Decision.Verdict != witness.Mismatch {
		t.Fatalf("lying update: %+v", out.Detail)
	}
	got := map[string][2]any{}
	for _, rs := range out.Decision.Reasons {
		for _, d := range rs.Diff {
			got[d.Path] = [2]any{d.Expected, d.Observed}
		}
	}
	if got["body.status"] != [2]any{"amended", "final"} || len(got) != 2 {
		t.Fatalf("diff %v", got)
	}
	if v := got["body.valueQuantity.value"]; fmt.Sprint(v[0]) != "75" || fmt.Sprint(v[1]) != "72" {
		t.Fatalf("diff %v", got)
	}
	t.Logf("lying: %s", out.Detail.Summary)
}

func TestFHIRReadBackDown(t *testing.T) {
	f := &fhirServer{obs: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	r := fhirRig(t, srv)
	if _, out := fhirCall(t, r, "create_observation", createArgs("final", 60)); out.Decision.Verdict != witness.Verified {
		t.Fatalf("create: %+v", out.Detail)
	}
	// A 5xx on the read is a gap: Can't check (UNVERIFIABLE at the required L1), never Doesn't match.
	f.readCode = 503
	_, out := fhirCall(t, r, "update_observation", updateArgs("1", "amended", 61))
	if out.Decision.Verdict != witness.Unverifiable || out.Decision.Level != witness.L0 || !hasCode(out, witness.CodeReadbackFailed) {
		t.Fatalf("read down: %+v", out.Decision)
	}
}

func TestFHIRUpdateIDMismatchRefused(t *testing.T) {
	f := &fhirServer{obs: map[string]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	defer srv.Close()
	r := fhirRig(t, srv)
	fhirCall(t, r, "create_observation", createArgs("final", 60))
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "fhir.update_observation")
	args := strings.Replace(updateArgs("1", "amended", 61), `"resource_id":"1"`, `"resource_id":"2"`, 1)
	_, err := ex.Execute(context.Background(), connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: org, Tool: spec,
		Qualified: "fhir.update_observation", Args: json.RawMessage(args), Deadline: time.Now().Add(5 * time.Second)})
	// The server refuses the mismatch with a 400: failed, nothing changed.
	if ce := connector.AsError(err); ce == nil || ce.Code != "upstream_error" || f.obs["1"]["status"] != "final" {
		t.Fatalf("err %v, stored %v", err, f.obs["1"]["status"])
	}
}

func TestBodyFieldConfig(t *testing.T) {
	c := rest.New()
	cfg := func(args string) json.RawMessage {
		return json.RawMessage(`{"base_url": "http://127.0.0.1:8090", "allowed_methods": ["POST"], "allowed_path_prefixes": ["/fhir"],
		  "tools": [{"name": "create", "method": "POST", "path": "/fhir/Observation", "args": {` + args + `}}]}`)
	}
	for _, tc := range []struct{ name, args, param, msg string }{
		{"field on a query arg", `"id": {"in": "query", "field": "id"}`, "config.tools[0].args.id.field", "field applies only to in: body."},
		{"bad key", `"rt": {"in": "body", "field": "resource-type"}`, "config.tools[0].args.rt.field", "field must be a JSON key"},
		{"two args, one key", `"a": {"in": "body", "field": "status"}, "status": {"in": "body"}`, "config.tools[0].args.status.field",
			"The body key status is already sent by the arg a."},
	} {
		_, errs := c.Validate(context.Background(), devEnv(), cfg(tc.args))
		found := false
		for _, e := range errs {
			found = found || e.Param == tc.param && strings.HasPrefix(e.Message, tc.msg)
		}
		if !found {
			t.Errorf("%s: %v", tc.name, errs)
		}
	}
	// field equal to the name is the default and is normalized away, so the config hash is unchanged.
	a, errs := c.Validate(context.Background(), devEnv(), cfg(`"status": {"in": "body", "field": "status"}`))
	b, errs2 := c.Validate(context.Background(), devEnv(), cfg(`"status": {"in": "body"}`))
	if len(errs)+len(errs2) > 0 || string(a) != string(b) {
		t.Fatalf("%v %v\n%s\n%s", errs, errs2, a, b)
	}
}

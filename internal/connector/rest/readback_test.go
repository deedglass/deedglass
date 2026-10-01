package rest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/rest"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
	"github.com/deedglass/deedglass/internal/witness"
)

// S19 §6, the REST read-back: an orders API in honest or lying mode (examples/mock-orders' rule:
// a lying PATCH answers 200 and saves nothing), the read tool's failures, and the config check.

// orders is a tiny orders API: GET and PATCH /v1/orders/{id}.
type orders struct {
	mu       sync.Mutex
	status   map[string]string
	lying    bool
	getCode  int    // non-zero: GET answers this status
	getEcho  bool   // GET 401 echoes the Authorization header
	lastPath string // the last GET path
}

func (o *orders) handle(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id := strings.TrimPrefix(r.URL.Path, "/v1/orders/")
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		o.lastPath = r.URL.Path
		if o.getEcho {
			w.WriteHeader(401)
			_, _ = fmt.Fprintf(w, `{"error": "bad token", "echo": %q}`, r.Header.Get("Authorization"))
			return
		}
		if o.getCode != 0 {
			w.WriteHeader(o.getCode)
			_, _ = w.Write([]byte(`{"error": "down"}`))
			return
		}
		st, ok := o.status[id]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error": "no such order"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"id": %s, "status": %q}`, id, st)
	case http.MethodPatch:
		var in struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !o.lying {
			o.status[id] = in.Status
		}
		_, _ = fmt.Fprintf(w, `{"id": %s, "status": %q}`, id, in.Status)
	}
}

// gateObserver does what the Gate's observer does: resolve the read tool through the registry and
// call the bound connector's ReadBack, mapping its errors for the Witness.
type gateObserver struct{ r *rig }

func (g gateObserver) ReadBack(ctx context.Context, _ witness.Subject, req witness.ReadRequest, _ int) (witness.Observation, error) {
	ex, spec, _, err := g.r.reg.Resolve(ctx, org, req.Tool)
	if err != nil {
		return witness.Observation{}, &witness.ReadError{Code: "connector_unavailable", Err: err}
	}
	rb := ex.(connector.ReadBacking)
	obs, err := rb.ReadBack(ctx, connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: org, Tool: spec, Qualified: req.Tool, Args: req.Args,
		Deadline: time.Now().Add(5 * time.Second)}, connector.Result{})
	w := witness.Observation{Status: obs.Status}
	if len(obs.Body) > 0 {
		_ = json.Unmarshal(obs.Body, &w.Body)
		w.ResponseDigest, _ = deed.DigestJSON(obs.Body)
	}
	switch ce := connector.AsError(err); {
	case err == nil:
		return w, nil
	case ce != nil && ce.Kind == connector.KindNotFound:
		return w, witness.ErrNotFound
	case ce != nil:
		return w, &witness.ReadError{Code: ce.Code, Err: err}
	}
	return w, &witness.ReadError{Code: "upstream_error", Err: err}
}

// update runs orders.update_status and then the Witness with the rig's read-back.
func update(t *testing.T, r *rig, id, status string, required witness.Level) witness.Outcome {
	t.Helper()
	args := fmt.Sprintf(`{"id":%q,"status":%q}`, id, status)
	ex, spec, _, err := r.reg.Resolve(context.Background(), org, "orders.update_status")
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.Execute(context.Background(), connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: org, Tool: spec,
		Qualified: "orders.update_status", Args: json.RawMessage(args), Deadline: time.Now().Add(5 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	sub := witness.Subject{Tool: "orders.update_status", ConnectorType: "rest", Mode: witness.ModeGateway, Args: json.RawMessage(args),
		Required: required, Result: witness.ResultView{Status: witness.StatusSucceeded, Output: res.Output, OutputBytes: len(res.Output)},
		Spec: witness.ToolWitnessSpec{Connector: "orders", Tool: "update_status", ReadBack: rest.WitnessReadBack(spec.ReadBack),
			Postcondition: spec.Postcondition}}
	ctx, cancel := witness.WithBudget(context.Background(), 5*time.Second)
	defer cancel()
	out, _, err := witness.Run(ctx, witness.Input{Subject: sub, Observer: gateObserver{r}, Final: true})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadBackHonestAndLying(t *testing.T) {
	o := &orders{status: map[string]string{"42": "pending"}}
	r, _ := newRig(t, o.handle, "")
	out := update(t, r, "42", "shipped", witness.L1)
	if out.Decision.Level != witness.L1 || out.Decision.Verdict != witness.Verified || out.Decision.Method != "readback.rest.get" ||
		out.Detail.Summary != `Read back orders.get: status is "shipped".` || o.lastPath != "/v1/orders/42" {
		t.Fatalf("honest: %+v", out.Detail)
	}
	if rb := out.Detail.Levels.L1.Readback; rb == nil || rb.Tool != "orders.get" || rb.Attempts != 1 || !strings.HasPrefix(rb.ResponseDigest, "sha256:") {
		t.Fatalf("readback detail %+v", out.Detail.Levels.L1)
	}
	o.lying = true
	out = update(t, r, "42", "cancelled", witness.L1)
	want := `orders.update_status reported success, but orders.get says status is "shipped", not "cancelled".`
	if out.Decision.Verdict != witness.Mismatch || out.Decision.Level != witness.L1 || out.Detail.Summary != want {
		t.Fatalf("lying: %+v", out.Detail)
	}
	var diff []witness.FieldDiff
	for _, rs := range out.Decision.Reasons {
		if rs.Check == "readback.expect" {
			diff = rs.Diff
		}
	}
	if len(diff) != 1 || diff[0].Path != "body.status" || diff[0].Expected != "cancelled" || diff[0].Observed != "shipped" {
		t.Fatalf("diff %+v", diff)
	}
	t.Logf("witness_detail (JCS, %d bytes): %s", len(out.JSON), out.JSON)
	// The detail keeps the compared field's values and a digest of the body, never the body.
	if strings.Contains(string(out.JSON), `"id":42`) {
		t.Fatalf("the detail holds the read-back body: %s", out.JSON)
	}
}

func TestReadBackFailure(t *testing.T) {
	o := &orders{status: map[string]string{"42": "pending"}, getCode: 500}
	r, _ := newRig(t, o.handle, "")
	// A 500 on the read is a gap: the claim falls to L0 with required L0, UNVERIFIABLE with L1.
	out := update(t, r, "42", "shipped", witness.L0)
	if out.Decision.Level != witness.L0 || out.Decision.Verdict != witness.Verified || !hasCode(out, witness.CodeReadbackFailed) {
		t.Fatalf("required L0: %+v", out.Decision)
	}
	out = update(t, r, "42", "shipped", witness.L1)
	if out.Decision.Level != witness.L0 || out.Decision.Verdict != witness.Unverifiable || !hasCode(out, witness.CodeLevelBelowRequired) ||
		out.Detail.Summary != "The policy asks for L1. The strongest clean evidence was L0." {
		t.Fatalf("required L1: %+v", out.Detail)
	}
	// Not found: a gap by default (a lying PATCH didn't create it).
	o.getCode, o.lying = 0, true
	out = update(t, r, "77", "shipped", witness.L0)
	if !hasCode(out, witness.CodeReadbackNotFound) || out.Decision.Verdict == witness.Mismatch {
		t.Fatalf("not found: %+v", out.Decision)
	}
}

func TestReadBackRedacted(t *testing.T) {
	o := &orders{status: map[string]string{"42": "pending"}, getEcho: true}
	r, _ := newRig(t, o.handle, "")
	out := update(t, r, "42", "shipped", witness.L1)
	if !hasCode(out, witness.CodeReadbackFailed) || strings.Contains(string(out.JSON), sentinel) {
		t.Fatalf("%s", out.JSON)
	}
	// The read-back's own answer, before the Witness: redacted by the bound connector.
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "orders.get")
	obs, err := ex.(connector.ReadBacking).ReadBack(context.Background(), connector.Call{OrgID: org, Tool: spec, Qualified: "orders.get",
		Args: json.RawMessage(`{"id":"42"}`)}, connector.Result{})
	if connector.AsError(err) == nil || strings.Contains(string(obs.Body), sentinel) {
		t.Fatalf("body %s err %v", obs.Body, err)
	}
	if len(obs.Body) > 0 && !strings.Contains(string(obs.Body), vault.RedactedText) {
		t.Fatalf("body %s", obs.Body)
	}
}

func TestReadBackPathRulesAgain(t *testing.T) {
	o := &orders{status: map[string]string{}}
	r, _ := newRig(t, o.handle, "")
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "orders.get")
	_, err := ex.(connector.ReadBacking).ReadBack(context.Background(), connector.Call{OrgID: org, Tool: spec, Qualified: "orders.get",
		Args: json.RawMessage(`{"id":"../admin"}`)}, connector.Result{})
	if ce := connector.AsError(err); ce == nil || ce.Code != "validation_failed" || o.lastPath != "" {
		t.Fatalf("a template must not smuggle a path: %v (upstream saw %q)", err, o.lastPath)
	}
}

func TestReadBackConfigRefusals(t *testing.T) {
	c := rest.New()
	cfg := func(tools string) json.RawMessage {
		return json.RawMessage(`{"base_url": "http://127.0.0.1:8090", "auth": {"kind": "bearer"}, "allowed_methods": ["GET", "PATCH"],
		  "allowed_path_prefixes": ["/v1/orders"], "tools": [` + tools + `]}`)
	}
	get := `{"name": "get", "method": "GET", "path": "/v1/orders/{id}", "args": {"id": {"in": "path", "type": "string", "required": true}}}`
	upd := func(rb string) string {
		return `{"name": "update_status", "method": "PATCH", "path": "/v1/orders/{id}", "args": {"id": {"in": "path", "type": "string", "required": true},
		  "status": {"in": "body", "type": "string", "required": true}}, ` + rb + `}`
	}
	for _, tc := range []struct{ name, tools, param, msg string }{
		{"a read-back that writes", get + "," + upd(`"readback": {"tool": "update_status", "args": {"id": "{{args.id}}"}}`),
			"config.tools[1].readback.tool", `"update_status" writes, so it can't be a read-back. Name a read tool such as "get".`},
		{"request.status", get + "," + upd(`"readback": {"tool": "get", "args": {"id": "{{args.id}}"}}, "postcondition": "observation.body.status == request.status"`),
			"config.tools[1].postcondition", "request.status isn't a field. Did you mean args.status?"},
		{"an undeclared arg", get + "," + upd(`"readback": {"tool": "get", "args": {"id": "{{args.order}}"}}`),
			"config.tools[1].readback.args.id", "doesn't declare"},
	} {
		_, errs := c.Validate(context.Background(), devEnv(), cfg(tc.tools))
		found := false
		for _, e := range errs {
			found = found || e.Param == tc.param && strings.Contains(e.Message, tc.msg)
		}
		if !found {
			t.Errorf("%s: %v", tc.name, errs)
		}
	}
	if _, errs := c.Validate(context.Background(), devEnv(), cfg(get+","+upd(`"readback": {"tool": "get", "args": {"id": "{{args.id}}"}, "expect": {"body.status": "{{args.status}}"}}`))); len(errs) > 0 {
		t.Fatalf("a valid read-back: %v", errs)
	}
}

func hasCode(o witness.Outcome, c witness.Code) bool {
	for _, r := range o.Decision.Reasons {
		if r.Code == c {
			return true
		}
	}
	return false
}

var _ = errors.New

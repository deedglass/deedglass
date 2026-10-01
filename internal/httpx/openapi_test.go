package httpx

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/api"
)

func systemServer() *Server {
	s := New(Options{Logger: NewLogger(io.Discard, true, nil)})
	s.Mount(s.SystemRoutes(SystemDeps{Spec: api.Spec, Health: &fakeHealth{state: MigrationState{Applied: 9, Expected: 9}}})...)
	s.Mount(s.WellKnownRoutes(testKeySet, nil)...)
	s.Mount(elsewhere()...)
	return s
}

// elsewhere stands in for routes other packages declare (internal/ledger, S06), so the spec checks
// here see every documented operation. cmd/deedglass's TestServeRoutesMatchOpenAPI checks the real
// declarations, from the list serve mounts.
func elsewhere() []Route {
	stub := func(w http.ResponseWriter, r *http.Request) error { return E(CodeNotFound) }
	var out []Route
	for _, op := range []struct{ pattern, id, scope string }{
		{"/v1/actions", "listActions", "actions:read"}, {"/v1/actions/{id}", "getAction", "actions:read"},
		{"/v1/deeds", "listDeeds", "deeds:read"}, {"/v1/deeds/{id}", "getDeed", "deeds:read"},
		{"/v1/deeds/export", "exportDeeds", "deeds:read"}, {"/v1/ledger/check", "checkLedger", "deeds:read"},
	} {
		out = append(out, Route{Method: "GET", Pattern: op.pattern, OperationID: op.id, Auth: SessionOrKey,
			MinRole: RoleViewer, Scopes: []string{op.scope}, Handler: stub})
	}
	return out
}

// TestOpenAPI runs checks 1–6: routes ⇄ operations, operationIds, x-deedglass-auth, the code enum,
// the default error response and info.version.
func TestOpenAPI(t *testing.T) {
	if !strings.HasPrefix(string(api.Spec), "openapi: 3.1.1\n") {
		t.Fatal("api/openapi.yaml must start with `openapi: 3.1.1`")
	}
	for _, err := range CheckSpec(api.Spec, systemServer().Routes()) {
		t.Error(err)
	}
}

// TestOpenAPIRequired is check 7: real handler output has every property the spec requires.
func TestOpenAPIRequired(t *testing.T) {
	h := &harness{log: &logBuf{}}
	h.srv = systemServer()
	for path, schema := range map[string]string{"/v1/health": "Health", "/v1/version": "Version", "/v1/nope": "ErrorEnvelope", "/.well-known/deedglass/keys.json": "SealKeySet"} {
		r := h.do(t, "GET", path, "")
		for _, err := range CheckRequired(api.Spec, schema, r.body) {
			t.Errorf("%s: %v", path, err)
		}
	}
}

// TestOpenAPIChecksCatchDrift shows the checks fail when they should.
func TestOpenAPIChecksCatchDrift(t *testing.T) {
	s := systemServer()
	routes := s.Routes()

	drifted := strings.Replace(string(api.Spec), "operationId: getVersion", "operationId: getVersionX", 1)
	drifted = strings.Replace(drifted, "x-deedglass-auth: public\n      x-deedglass-since: \"2026-10-01\"\n      parameters:\n        - $ref: \"#/components/parameters/DeedglassVersion\"\n      responses:\n        \"200\":\n          description: This server's",
		"x-deedglass-auth: session\n      x-deedglass-since: \"2026-10-01\"\n      parameters:\n        - $ref: \"#/components/parameters/DeedglassVersion\"\n      responses:\n        \"200\":\n          description: This server's", 1)
	drifted = strings.Replace(drifted, "                - service_unavailable\n", "", 1)
	drifted = strings.Replace(drifted, `version: "2026-10-01"`, `version: "2026-09-01"`, 1)
	drifted = strings.Replace(drifted, "paths:\n", "paths:\n  /v1/phantom:\n    get:\n      operationId: phantom\n      summary: x\n      tags: [System]\n      x-deedglass-auth: public\n      x-deedglass-since: \"2026-10-01\"\n      responses: {}\n", 1)

	errs := CheckSpec([]byte(drifted), routes)
	all := ""
	for _, e := range errs {
		all += e.Error() + "\n"
	}
	for _, want := range []string{
		`operationId "getVersionX" in the spec`,
		`x-deedglass-auth "session"`,
		"code enum differs",
		"info.version",
		"GET /v1/phantom is in api/openapi.yaml but not mounted",
		"GET /v1/phantom: the default response",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("drift not caught: %q\nerrors:\n%s", want, all)
		}
	}
	extra := append(routes, Route{Method: "GET", Pattern: "/v1/whoami", OperationID: "getWhoami", Auth: AgentKey, Since: "2026-10-01"})
	if errs := CheckSpec(api.Spec, extra); len(errs) != 1 || !strings.Contains(errs[0].Error(), "mounted but not in api/openapi.yaml") {
		t.Errorf("an undocumented route: %v", errs)
	}
	if errs := CheckRequired(api.Spec, "Version", []byte(`{"object":"version"}`)); len(errs) != 6 {
		t.Errorf("CheckRequired found %d missing properties, want 6: %v", len(errs), errs)
	}
	if errs := CheckRequired(api.Spec, "Health", []byte(`{"object":"health","status":"ok","version":"x","api_version":"x","time":"x","checks":{"database":{},"migrations":{"status":"ok"},"seal_key":{"status":"ok"}}}`)); len(errs) != 2 {
		t.Errorf("nested required: %v", errs)
	}
}

package github

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

func TestValidateDefaults(t *testing.T) {
	c := newConn()
	out, errs := c.Validate(context.Background(), connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}},
		json.RawMessage(`{"repos":["arjun/deedglass-sandbox","Arjun/Deedglass-Sandbox","arjun/*"]}`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var cfg Config
	_ = json.Unmarshal(out, &cfg)
	if cfg.Auth != "pat" || !cfg.IssueMarker || cfg.APIBaseURL != "https://api.github.com" || cfg.APIVersion != "2026-03-10" ||
		cfg.MaxFileBytes != 1<<20 || strings.Join(cfg.ProtectedBranches, ",") != "main,master,release/*" || len(cfg.Repos) != 2 {
		t.Fatalf("normalized %s", out)
	}
	// Normalizing twice is stable.
	out2, errs := c.Validate(context.Background(), devEnv(), out)
	if len(errs) > 0 || string(out2) != string(out) {
		t.Fatalf("%s\n%s %v", out, out2, errs)
	}
	// A nil egress policy is prod.
	if _, errs := c.Validate(context.Background(), connector.Env{}, json.RawMessage(`{"repos":["a/b"],"api_base_url":"http://127.0.0.1:9"}`)); len(errs) == 0 {
		t.Fatal("http outside dev was accepted")
	}
}

func TestValidateRefusals(t *testing.T) {
	c := newConn()
	cases := map[string]string{
		`[]`:                               "config",
		`{"repos":["a/b"],"extra":1}`:      "config.extra",
		`{"repos":[]}`:                     "config.repos",
		`{}`:                               "config.repos",
		`{"repos":null}`:                   "config.repos",
		`{"repos":"a/b"}`:                  "config.repos",
		`{"repos":["a"]}`:                  "config.repos[0]",
		`{"repos":["a/.."]}`:               "config.repos[0]",
		`{"repos":["a/b c"]}`:              "config.repos[0]",
		`{"repos":["a/b"],"auth":"oauth"}`: "config.auth",
		`{"repos":["a/b"],"auth":"app"}`:   "config.app",
		`{"repos":["a/b"],"app":{"client_id":"x","app_id":1,"installation_id":1}}`:                    "config.app",
		`{"repos":["a/b"],"auth":"app","app":{"client_id":"x","app_id":0,"installation_id":1}}`:       "config.app.app_id",
		`{"repos":["a/b"],"auth":"app","app":{"client_id":"x y","app_id":1,"installation_id":1}}`:     "config.app.client_id",
		`{"repos":["a/b"],"auth":"app","app":{"client_id":"x","app_id":1}}`:                           "config.app.installation_id",
		`{"repos":["a/b"],"auth":"app","app":{"client_id":"x","app_id":1,"installation_id":1,"z":1}}`: "config.app.z",
		`{"repos":["a/b"],"auth":"app","app":[]}`:                                                     "config.app",
		`{"repos":["a/b"],"protected_branches":["["]}`:                                                "config.protected_branches[0]",
		`{"repos":["a/b"],"protected_branches":[""]}`:                                                 "config.protected_branches[0]",
		`{"repos":["a/b"],"issue_marker":"yes"}`:                                                      "config.issue_marker",
		`{"repos":["a/b"],"api_base_url":"ftp://x"}`:                                                  "config.api_base_url",
		`{"repos":["a/b"],"api_base_url":"https://api.github.com/?x=1"}`:                              "config.api_base_url",
		`{"repos":["a/b"],"api_version":"latest"}`:                                                    "config.api_version",
		`{"repos":["a/b"],"max_file_bytes":2097152}`:                                                  "config.max_file_bytes",
		`{"repos":["a/b"],"max_file_bytes":0}`:                                                        "config.max_file_bytes",
		`{"repos":["a/b"],"api_version":7}`:                                                           "config.api_version",
		`{"repos":["a/b"],"protected_branches":` + `[` + strings.Repeat(`"x",`, 50) + `"x"]}`:         "config.protected_branches",
		`{"repos":[` + strings.Repeat(`"a/b",`, 50) + `"a/c"]}`:                                       "config.repos",
		`{"repos":["a/b"],"pad":"` + strings.Repeat("x", MaxConfigBytes) + `"}`:                       "config",
	}
	for in, param := range cases {
		_, errs := c.Validate(context.Background(), devEnv(), json.RawMessage(in))
		found := false
		for _, e := range errs {
			if e.Param == param {
				found = true
			}
			if strings.Contains(e.Message, "—") || strings.Contains(e.Message, "!") {
				t.Errorf("message style: %q", e.Message)
			}
		}
		if !found {
			t.Errorf("%.80s: errors %v, want one at %s", in, errs, param)
		}
	}
	// A valid App config.
	if _, errs := c.Validate(context.Background(), devEnv(), appCfg("https://api.github.com")); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestAllowedAndProtected(t *testing.T) {
	rules := parseRules([]string{"arjun/deedglass-sandbox", "acme/*"})
	for repo, want := range map[string]bool{"arjun/deedglass-sandbox": true, "ARJUN/Deedglass-Sandbox": true, "acme/anything": true,
		"arjun/payments": false, "acmex/a": false, "noslash": false} {
		if allowed(rules, repo) != want {
			t.Errorf("allowed(%s) != %v", repo, want)
		}
	}
	if !wildcard(rules) || wildcard(parseRules([]string{"a/b"})) {
		t.Fatal("wildcard")
	}
	for b, want := range map[string]bool{"main": true, "release/1.2": true, "release/1/2": false, "feature/main": false} {
		if protected(DefaultProtectedBranches, b) != want {
			t.Errorf("protected(%s) != %v", b, want)
		}
	}
}

func TestStateErrors(t *testing.T) {
	c := newConn()
	for _, cfg := range []string{`{`, `{"api_base_url":"::"}`, `{"auth":"app","api_base_url":"https://x"}`} {
		inst := connector.NewInstance(testConn, testOrg, "github", Type, json.RawMessage(cfg), "h", devEnv(), nil)
		if _, err := c.Tools(context.Background(), inst); err == nil {
			t.Fatalf("%s: no error", cfg)
		}
		if _, err := c.Execute(ctxWithRedactor(), inst, call(t, "get_repo", `{"repo":"a/b"}`)); mustErr(t, err).Code != "connector_misconfigured" {
			t.Fatal(err)
		}
		if _, _, err := c.NativeReadBack(ctxWithRedactor(), inst, call(t, "create_issue", `{"repo":"a/b","title":"t"}`), connector.Result{}); err == nil {
			t.Fatal("read-back without a config")
		}
		if m, _ := c.Enrich(ctxWithRedactor(), inst, toolSpecs[0], json.RawMessage(`{"repo":"a/b"}`)); m != nil {
			t.Fatal("facts without a config")
		}
		rep, _ := c.Test(ctxWithRedactor(), inst)
		if rep.Status != connector.CheckFail || len(rep.Checks) != 7 {
			t.Fatalf("report %+v", rep)
		}
	}
	// Unknown tool.
	inst := instance(t, c, cfgJSON("http://127.0.0.1:1", nil), nil)
	_, err := c.Execute(ctxWithRedactor(), inst, connector.Call{Tool: connector.ToolSpec{Name: "nope"}, Args: json.RawMessage(`{}`)})
	if mustErr(t, err).Code != "tool_unknown" {
		t.Fatal(err)
	}
	if _, _, err := c.NativeReadBack(ctxWithRedactor(), inst, call(t, "get_repo", `{"repo":"arjun/deedglass-sandbox"}`), connector.Result{}); err == nil {
		t.Fatal("read-back of a read tool")
	}
}

// TestExampleConfig: examples/connectors/github.yaml is a valid config.
func TestExampleConfig(t *testing.T) {
	b, err := os.ReadFile("../../../examples/connectors/github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type   string         `yaml:"type"`
		Name   string         `yaml:"name"`
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(doc.Config)
	out, errs := newConn().Validate(context.Background(), connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}}, cfg)
	if doc.Type != Type || doc.Name != "github" || len(errs) > 0 {
		t.Fatalf("%s %s %v", doc.Type, doc.Name, errs)
	}
	if !strings.Contains(string(out), `"repos":["arjun/deedglass-sandbox"]`) {
		t.Fatal(string(out))
	}
}

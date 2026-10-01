package config

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestMCPSettings(t *testing.T) {
	c, err := loadYAML(t, `mcp:
  allowed_origins: [https://console.example.test]
  passthrough: [resources, prompts]
  upstreams:
    zeta:
      url: https://mcp.example.test/mcp
    everything:
      url: http://127.0.0.1:3001/mcp
      headers_from_env: {Authorization: EVERYTHING_MCP_AUTH}
      timeout: 5s
      tools_ttl: 1m
`)
	if err != nil {
		t.Fatal(err)
	}
	m := c.MCP
	if len(m.AllowedOrigins) != 1 || len(m.Passthrough) != 2 || len(m.Upstreams) != 2 || m.Upstreams[0].Slug != "everything" {
		t.Fatalf("%+v", m)
	}
	u := m.Upstreams[0]
	if u.URL.String() != "http://127.0.0.1:3001/mcp" || u.HeadersFromEnv["Authorization"] != "EVERYTHING_MCP_AUTH" || u.Timeout != 5*time.Second || u.ToolsTTL != time.Minute {
		t.Fatalf("%+v", u)
	}
	if z := m.Upstreams[1]; z.Timeout != DefaultUpstreamTimeout || z.ToolsTTL != DefaultUpstreamToolsTTL {
		t.Fatalf("defaults %+v", z)
	}
	if c, err := loadYAML(t, ""); err != nil || len(c.MCP.Upstreams) != 0 {
		t.Fatal("empty")
	}
}

func TestMCPSettingsProblems(t *testing.T) {
	cases := map[string]string{
		"mcp: 5\n":               `"mcp" must be a mapping of settings.`,
		"mcp:\n  upstream: {}\n": `Did you mean mcp.upstreams?`,
		"mcp:\n  allowed_origins: https://x.test\n":                                                                 `"mcp.allowed_origins" must be a list.`,
		"mcp:\n  allowed_origins: [https://x.test/path]\n":                                                          `isn't an origin`,
		"mcp:\n  passthrough: [tools]\n":                                                                            `isn't a method family`,
		"mcp:\n  upstreams:\n    Bad_Name: {url: https://x.test}\n":                                                 `isn't a connector name`,
		"mcp:\n  upstreams:\n    native: {url: https://x.test}\n":                                                   `isn't a connector name`,
		"mcp:\n  upstreams:\n    up: {url: ftp://x.test}\n":                                                         `must be an http or https URL`,
		"mcp:\n  upstreams:\n    up: {url: 'https://u:p@x.test'}\n":                                                 `without credentials`,
		"mcp:\n  upstreams:\n    up: {timeout: 5s}\n":                                                               `needs a url`,
		"mcp:\n  upstreams:\n    up: {url: https://x.test, timeout: 1h}\n":                                          `from 100ms to 10m`,
		"mcp:\n  upstreams:\n    up: {url: https://x.test, region: eu}\n":                                           `"mcp.upstreams.up.region" isn't a Deedglass setting.`,
		"mcp:\n  upstreams:\n    up: {url: https://x.test, headers_from_env: {Authorization: DEEDGLASS_UP_AUTH}}\n": `reserved for Deedglass's own settings`,
		"mcp:\n  upstreams:\n    up: {url: https://x.test, headers_from_env: {'Bad Header': UP_AUTH}}\n":            `isn't a header name`,
		"mcp:\n  upstreams:\n    up: {url: https://x.test, headers_from_env: {Authorization: up_auth}}\n":           `isn't an environment variable name`,
		"mcp:\n  passthrough: []\n  passthrough: []\n":                                                              "appears twice",
	}
	for yml, want := range cases {
		_, err := loadYAML(t, yml)
		var ps *Problems
		if !errors.As(err, &ps) || !strings.Contains(ps.Error(), want) {
			t.Errorf("%q: want %q, got %v", yml, want, err)
		}
	}
	// http upstreams are refused in prod.
	env := map[string]string{"DEEDGLASS_ENV": "prod", "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key48}
	_, _, err := Load(LoadOptions{Env: env, FS: fstest.MapFS{"deedglass.yaml": {Data: []byte("mcp:\n  upstreams:\n    up: {url: http://x.test}\n")}}})
	var ps *Problems
	if !errors.As(err, &ps) || !strings.Contains(ps.Error(), "must use https in prod") {
		t.Fatalf("%v", err)
	}
	// headers_from_env is dev only (S18): prod keeps upstream credentials in the vault (mcp_http).
	_, _, err = Load(LoadOptions{Env: env, FS: fstest.MapFS{"deedglass.yaml": {Data: []byte("mcp:\n  upstreams:\n    up: {url: https://x.test, headers_from_env: {Authorization: UP_AUTH}}\n")}}})
	if !errors.As(err, &ps) || !strings.Contains(ps.Error(), "headers_from_env is dev only") || !strings.Contains(ps.Error(), "mcp_http connector") {
		t.Fatalf("%v", err)
	}
}

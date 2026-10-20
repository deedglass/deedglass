package config

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func loadYAML(t *testing.T, yml string) (*Config, error) {
	t.Helper()
	env := map[string]string{"DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_API_KEY_PEPPER": key48}
	c, _, err := Load(LoadOptions{Env: env, FS: fstest.MapFS{"deedglass.yaml": {Data: []byte(yml)}}, Dir: ""})
	return c, err
}

func TestGateSettingsDefaults(t *testing.T) {
	c, err := loadYAML(t, "")
	if err != nil {
		t.Fatal(err)
	}
	want := GateSettings{WaitTimeout: 10 * time.Second, ExecuteTimeout: 30 * time.Second, EdgeResultTimeout: 10 * time.Minute,
		MaxArgsBytes: 262144, MaxResultBytes: 262144, WorkerConcurrency: 4, WorkerMaxAttempts: 8}
	if c.Gate != want {
		t.Fatalf("%+v", c.Gate)
	}
}

func TestGateSettingsFromYAML(t *testing.T) {
	c, err := loadYAML(t, "listen: 127.0.0.1:9090\ngate:\n  wait_timeout: 2s\n  execute_timeout: 50ms\n  worker:\n    concurrency: 8\n")
	if err == nil {
		t.Fatal("execute_timeout 50ms accepted; the minimum is 100ms")
	}
	c, err = loadYAML(t, "gate:\n  wait_timeout: 2s\n  execute_timeout: 1s\n  worker:\n    concurrency: 8\n    max_attempts: 3\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Gate.WaitTimeout != 2*time.Second || c.Gate.ExecuteTimeout != time.Second || c.Gate.WorkerConcurrency != 8 || c.Gate.WorkerMaxAttempts != 3 {
		t.Fatalf("%+v", c.Gate)
	}
	if c.Gate.EdgeResultTimeout != 10*time.Minute {
		t.Fatalf("default lost: %+v", c.Gate)
	}
}

func TestGateSettingsProblems(t *testing.T) {
	cases := map[string]string{
		"gate:\n  wiat_timeout: 2s\n":                     `"gate.wiat_timeout" isn't a Deedglass setting.`,
		"gate:\n  wait_timeout: 26s\n":                    "gate.wait_timeout",
		"gate:\n  wait_timeout: soon\n":                   "must be a duration",
		"gate:\n  max_args_bytes: 300000\n":               "must be a whole number from 1024 to 262144",
		"gate: 5\n":                                       `"gate" must be a mapping of settings.`,
		"gate:\n  wait_timeout: [1, 2]\n":                 `"gate.wait_timeout" must be a single value.`,
		"gate:\n  wait_timeout: 1s\n  wait_timeout: 2s\n": "appears twice",
	}
	for yml, want := range cases {
		_, err := loadYAML(t, yml)
		var ps *Problems
		if !errors.As(err, &ps) || !strings.Contains(err.Error()+problemsText(ps), want) {
			t.Errorf("%q: %v, want %q", yml, err, want)
		}
	}
}

func problemsText(ps *Problems) string {
	if ps == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range ps.List {
		b.WriteString(p.Key + ": " + p.String() + "\n")
	}
	return b.String()
}

// TestSettingsDocs keeps docs/CONFIG.md's settings table equal to Settings.
func TestSettingsDocs(t *testing.T) {
	const path = "../../docs/CONFIG.md"
	const begin, end = "<!-- settings:begin (generated from internal/config/settings.go; go test ./internal/config -run TestSettingsDocs -update) -->\n", "<!-- settings:end -->"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i, j := strings.Index(doc, begin), strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("docs/CONFIG.md needs the markers %q and %q", begin, end)
	}
	want := SettingsMarkdown()
	if got := doc[i+len(begin) : j]; got != want {
		if *update {
			if err := os.WriteFile(path, []byte(doc[:i+len(begin)]+want+doc[j:]), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal("docs/CONFIG.md settings table drifted from settings.go. Run: go test ./internal/config -run TestSettingsDocs -update")
	}
}

func TestTimestampSettings(t *testing.T) {
	c, err := loadYAML(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Timestamps.Enabled() || c.Timestamps.MinTSA != 2 || !c.Timestamps.AtExport || c.Timestamps.Timeout != 10*time.Second {
		t.Fatalf("defaults: %+v", c.Timestamps) // off by default: no TSA, no service, no outbound call
	}
	c, err = loadYAML(t, "timestamps:\n  tsa_urls: https://a.example/tsr, https://b.example\n  scitt_url: https://ts.example\n  scitt_key_file: k.jwk\n  at_export: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Timestamps.Enabled() || len(c.Timestamps.TSAURLs) != 2 || c.Timestamps.TSAURLs[1] != "https://b.example" || c.Timestamps.AtExport {
		t.Fatalf("%+v", c.Timestamps)
	}
	if _, err := loadYAML(t, "timestamps:\n  tsa_urls: http://127.0.0.1:9999/, http://localhost:1/\n"); err != nil {
		t.Fatalf("loopback http: %v", err)
	}
	cases := map[string]string{
		"timestamps:\n  tsa_urls: http://tsa.example/\n":                                                            "must use https",
		"timestamps:\n  tsa_urls: https://u:p@tsa.example/\n":                                                       "without user info",
		"timestamps:\n  tsa_urls: https://a.example\n":                                                              "timestamps.min_tsa",
		"timestamps:\n  tsa_urls: https://a.example\n  min_tsa: 9\n":                                                "from 1 to 8",
		"timestamps:\n  scitt_url: https://ts.example\n":                                                            "timestamps.scitt_key_file",
		"timestamps:\n  tsa_urls: " + strings.Repeat("https://a.example,", 8) + "https://a.example\n  min_tsa: 1\n": "at most 8",
	}
	for yml, want := range cases {
		_, err := loadYAML(t, yml)
		var ps *Problems
		if !errors.As(err, &ps) || !strings.Contains(err.Error()+problemsText(ps), want) {
			t.Errorf("%q: got %v, want %q", yml, err, want)
		}
	}
}

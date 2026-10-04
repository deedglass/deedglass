package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSlackManifestGoldens(t *testing.T) {
	cases := []struct {
		name string
		tty  bool
		args []string
		code int
	}{
		{"slack/manifest", false, []string{"slack", "manifest", "--base-url", "https://example.test"}, 0},
		{"slack/manifest_tty", true, []string{"slack", "manifest", "--base-url", "https://abc.trycloudflare.com/", "--name", "Acme approvals"}, 0},
		{"slack/manifest_json", false, []string{"--json", "slack", "manifest", "--base-url", "https://example.test"}, 0},
		{"slack/manifest_http", false, []string{"slack", "manifest", "--base-url", "http://127.0.0.1:8080"}, 2},
		{"slack/manifest_missing", false, []string{"slack", "manifest"}, 2},
	}
	for _, c := range cases {
		r := cliIn(t, nil, map[string]string{"NO_COLOR": "1"}, c.tty, c.args...)
		if r.code != c.code {
			t.Errorf("%s: exit %d, want %d\n%s%s", c.name, r.code, c.code, r.stdout, r.stderr)
		}
		golden(t, c.name, r)
	}
	m, err := buildSlackManifest("https://example.test", "Deedglass")
	if err != nil || !strings.Contains(m.Manifest, `request_url: "https://example.test/v1/slack/interact"`) || strings.Contains(m.Manifest, "YOUR-HOST") {
		t.Fatalf("%v\n%s", err, m.Manifest)
	}
	for _, scope := range []string{"chat:write", "channels:read", "groups:read", "channels:history", "groups:history", "reactions:read",
		"reactions:write", "users:read", "users:read.email"} {
		if !strings.Contains(m.Manifest, "- "+scope) {
			t.Errorf("scope %s missing", scope)
		}
	}
	for _, bad := range []string{"https://x.test/path", "https://x.test?q=1", "https://u:p@x.test", "ftp://x.test", "x.test", ""} {
		if _, err := buildSlackManifest(bad, "Deedglass"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := buildSlackManifest("https://x.test", strings.Repeat("n", 36)); err == nil {
		t.Error("a 36-character name accepted")
	}
}

// TestCredentialsSetFields: --field KEY=env:VAR and KEY=file:PATH round-trip into one fields
// credential, the values never reach stdout or stderr, and a key file others can read is named.
func TestCredentialsSetFields(t *testing.T) {
	var (
		mu  sync.Mutex
		put map[string]any
	)
	conn := `{"object":"connector","id":"con_01k6h7kp6msmzrhdzwtcr3jnkp","name":"slack","type":"slack","status":"active","description":"",` +
		`"config":{"team_id":"T0123ABCD","app_id":"A0123ABCD"},"config_hash":"sha256:` + strings.Repeat("a", 64) + `","tools":[],` +
		`"credentials":{"set":false},"health":{"status":"unknown"}}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/connectors/slack":
			io.WriteString(w, conn)
		case "PUT /v1/connectors/con_01k6h7kp6msmzrhdzwtcr3jnkp/credentials":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&put)
			mu.Unlock()
			io.WriteString(w, `{"object":"credential","set":true,"kind":"fields","hint":"2 fields","version":1,"kek_id":"kek:abcdef012345",`+
				`"updated_at":"2026-10-10T04:00:00.000Z","updated_by":"usr_01k6h7kp6msmzrhdzwtcr3jnkp"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"No.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/not_found"}}`)
		}
	}))
	t.Cleanup(api.Close)
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "signing.txt")
	if err := os.WriteFile(secretFile, []byte("0123456789abcdef0123456789abcdef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tight := filepath.Join(dir, "tight.txt")
	if err := os.WriteFile(tight, []byte("0123456789abcdef0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const token = "xoxb-1234567890-abcdefghijklmnop"
	env := map[string]string{"DEEDGLASS_SERVER": api.URL, "DEEDGLASS_API_KEY": "adm", "NO_COLOR": "1", "SLACK_BOT_TOKEN": token}
	for _, c := range []struct {
		name string
		tty  bool
		args []string
		file string
	}{
		{"slack/credentials_fields", false, []string{"connector", "credentials", "set", "slack", "--field", "bot_token=env:SLACK_BOT_TOKEN", "--field", "signing_secret=file:SIGNING"}, secretFile},
		{"slack/credentials_fields_tty", true, []string{"connector", "credentials", "set", "slack", "--field", "bot_token=env:SLACK_BOT_TOKEN", "--field", "signing_secret=file:SIGNING"}, tight},
		{"slack/credentials_fields_json", false, []string{"--json", "connector", "credentials", "set", "slack", "--field", "bot_token=env:SLACK_BOT_TOKEN", "--field", "signing_secret=file:SIGNING"}, tight},
	} {
		args := append([]string(nil), c.args...)
		for i, a := range args {
			args[i] = strings.Replace(a, "SIGNING", c.file, 1)
		}
		r := cliIn(t, nil, env, c.tty, args...)
		if r.code != 0 {
			t.Fatalf("%s: exit %d\n%s%s", c.name, r.code, r.stdout, r.stderr)
		}
		if strings.Contains(r.stdout+r.stderr, token) || strings.Contains(r.stdout+r.stderr, "0123456789abcdef") {
			t.Fatalf("%s: a value was printed", c.name)
		}
		mu.Lock()
		sec, _ := put["secret"].(map[string]any)
		if put["kind"] != "fields" || sec["bot_token"] != token || sec["signing_secret"] != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("%s: sent %v", c.name, put)
		}
		mu.Unlock()
		r.stderr = strings.ReplaceAll(r.stderr, dir, "DIR")
		golden(t, c.name, r)
	}
	// Two fields from stdin are refused; a value flag is refused.
	r := cliIn(t, []byte("a\n"), env, false, "connector", "credentials", "set", "slack", "--field", "a=stdin", "--field", "b=stdin")
	if r.code != 2 {
		t.Fatalf("two stdin fields: exit %d %s", r.code, r.stderr)
	}
	r = cliIn(t, nil, env, false, "connector", "credentials", "set", "slack", "--field", "bot_token=xoxb-literal")
	if r.code != 2 || strings.Contains(r.stdout+r.stderr, "xoxb-literal") {
		t.Fatalf("a value flag: exit %d %s", r.code, r.stderr)
	}
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAPI answers the S08 routes the CLI calls with the brief's examples (S08 §3.8, §5.1).
func fakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	ago := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format("2006-01-02T15:04:05.000Z") }
	agent := func(name, id, status string, labels string, keys int, used string) string {
		u := "null"
		if used != "" {
			u = `"` + used + `"`
		}
		return `{"object":"agent","id":"` + id + `","name":"` + name + `","description":"","labels":` + labels +
			`,"budgets":{"calls_per_minute":60},"status":"` + status + `","status_reason":null,"status_changed_at":null,"status_changed_by":null,` +
			`"owner":null,"active_keys":` + itoa(keys) + `,"intent_mode":"off","last_used_at":` + u + `,"created_at":"2026-10-03T10:02:11.000Z","updated_at":"2026-10-03T10:02:11.000Z"`
	}
	sb := agent("support-bot", "agt_01k633rrmh3bcz2p39gg4vp46q", "active", `{"env":"prod","team":"support"}`, 2, ago(2*time.Minute))
	ra := agent("refund-agent", "agt_01k633s0x9h4m2z7c1v8b3n5q6", "stopped", `{"env":"prod"}`, 1, ago(time.Hour+time.Minute))
	key := `{"object":"api_key","id":"key_01k6jz12ptrvg2zn5vts5xc1ay","agent":"agt_01k633rrmh3bcz2p39gg4vp46q","name":"prod runner","mode":"live",` +
		`"prefix":"dg_live_jJbEEMWk","scopes":["actions:execute","actions:read","deeds:read"],"secret":"dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8",` +
		`"expires_at":null,"last_used_at":null,"revoked_at":null,"rolled_from":null,"created_at":"2026-10-03T10:03:40.000Z","deed":"deed_01k6vrddq2atgb2023qvhhct3g"}`
	h := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		auth := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		if auth == "Bearer revoked" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"type":"authentication_error","code":"api_key_revoked","message":"The API key has been revoked. Create a new key in Console › Agents.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/api_key_revoked"}}`)
			return
		}
		if auth == "Bearer agentkey" {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"type":"permission_error","code":"insufficient_scope","message":"This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server.","param":null,"request_id":"req_2","doc_url":"https://docs.deedglass.com/errors/insufficient_scope"}}`)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/agents":
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			if in["name"] != "support-bot" {
				t.Errorf("create body: %s", body)
			}
			w.WriteHeader(201)
			io.WriteString(w, agent("support-bot", "agt_01k633rrmh3bcz2p39gg4vp46q", "active", `{"env":"prod","team":"support"}`, 0, "")+
				`,"deed":"deed_01k6vrddq2atgb2023qvhhct3g"}`)
		case "GET /v1/agents":
			switch r.URL.Query().Get("name") {
			case "support-bot":
				io.WriteString(w, `{"object":"list","data":[`+sb+`}],"has_more":false,"next_cursor":null}`)
			case "":
				io.WriteString(w, `{"object":"list","data":[`+sb+`},`+ra+`}],"has_more":false,"next_cursor":null}`)
			default:
				io.WriteString(w, `{"object":"list","data":[],"has_more":false,"next_cursor":null}`)
			}
		case "GET /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q":
			io.WriteString(w, sb+"}")
		case "POST /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q/keys":
			w.WriteHeader(201)
			io.WriteString(w, key)
		case "POST /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q/stop":
			io.WriteString(w, strings.Replace(sb, `"status":"active"`, `"status":"stopped"`, 1)+`,"deed":"deed_01k6vrddq2atgb2023qvhhct3h"}`)
		case "POST /v1/keys/key_01k6jz12ptrvg2zn5vts5xc1ay/roll":
			w.WriteHeader(201)
			io.WriteString(w, `{"object":"api_key","id":"key_01k6jt6tgx72gjr876976xak5e","agent":"agt_01k633rrmh3bcz2p39gg4vp46q","name":"prod runner","mode":"live",`+
				`"prefix":"dg_live_0IDw6L5O","scopes":["actions:execute","actions:read","deeds:read"],"secret":"dg_live_0IDw6L5O_9hxevwY7nqdKIZZi5A2xlI2O2qn8znik",`+
				`"expires_at":null,"last_used_at":null,"revoked_at":null,"rolled_from":"key_01k6jz12ptrvg2zn5vts5xc1ay","created_at":"2026-10-03T10:03:40.000Z",`+
				`"deed":"deed_01k6vrddq2atgb2023qvhhct3j","previous":{"id":"key_01k6jz12ptrvg2zn5vts5xc1ay","prefix":"dg_live_jJbEEMWk","expires_at":"2099-10-04T10:03:40.000Z"}}`)
		case "GET /v1/whoami":
			io.WriteString(w, `{"object":"whoami","org":{"id":"org_01k6h7kp6msmzrhdzwtcr3jnkp","name":"Acme","slug":"acme","stopped":false},"agent":null,`+
				`"key":{"id":"key_01k6jz1","prefix":"dg_live_admin000","mode":"live","scopes":["admin"],"expires_at":null},"budgets":null}`)
		case "POST /v1/org/stop-all":
			io.WriteString(w, `{"object":"org_stop","org":"org_01k6h7kp6msmzrhdzwtcr3jnkp","stopped":true,"stopped_at":"2026-10-03T14:05:00.000Z",`+
				`"stopped_by":"key_01k6jz1","reason":"Investigating unexpected refunds","agents_affected":1,"deed":"deed_01k6vrddq2atgb2023qvhhct3k"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"There's no endpoint here.","param":null,"request_id":"req_3","doc_url":"https://docs.deedglass.com/errors/not_found"}}`)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(s.Close)
	return s
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestAgentCommandGoldens(t *testing.T) {
	api := fakeAPI(t)
	env := func(key string, extra ...string) map[string]string {
		m := map[string]string{"DEEDGLASS_SERVER": api.URL, "DEEDGLASS_API_KEY": key, "NO_COLOR": "1"}
		for i := 0; i+1 < len(extra); i += 2 {
			m[extra[i]] = extra[i+1]
		}
		return m
	}
	cases := []struct {
		name  string
		env   map[string]string
		tty   bool
		stdin string
		args  []string
		code  int
	}{
		{"agent/create", env("adm"), false, "", []string{"agent", "create", "support-bot", "--label", "env=prod", "--label", "team=support", "--calls-per-minute", "60"}, 0},
		{"agent/create_json", env("adm"), false, "", []string{"--json", "agent", "create", "support-bot", "--label", "env=prod"}, 0},
		{"agent/keys_create", env("adm"), false, "", []string{"agent", "keys", "create", "support-bot", "--name", "prod runner"}, 0},
		{"agent/list", env("adm"), false, "", []string{"agent", "list"}, 0},
		{"agent/list_color", map[string]string{"DEEDGLASS_SERVER": api.URL, "DEEDGLASS_API_KEY": "adm"}, true, "", []string{"agent", "list"}, 0},
		{"agent/stop", env("adm"), false, "", []string{"agent", "stop", "support-bot", "--reason", "Refund loop in staging"}, 0},
		{"agent/start_no_reason", env("adm"), false, "", []string{"agent", "start", "support-bot"}, 2},
		{"agent/roll", env("adm"), false, "", []string{"agent", "keys", "roll", "key_01k6jz12ptrvg2zn5vts5xc1ay", "--overlap", "24h"}, 0},
		{"agent/stop_all_typed", env("adm"), true, "acme\n", []string{"--no-color", "org", "stop-all", "--reason", "Investigating unexpected refunds"}, 0},
		{"agent/stop_all_wrong", env("adm"), true, "acne\n", []string{"--no-color", "org", "stop-all"}, 1},
		{"agent/stop_all_pipe", env("adm"), false, "", []string{"org", "stop-all"}, 2},
		{"agent/revoked_key", env("revoked"), false, "", []string{"agent", "list"}, 3},
		{"agent/revoked_key_json", env("revoked"), false, "", []string{"--json", "agent", "list"}, 3},
		{"agent/agent_key", env("agentkey"), false, "", []string{"agent", "stop", "support-bot"}, 1},
		{"agent/no_key", env(""), false, "", []string{"agent", "list"}, 3},
		{"agent/unknown_agent", env("adm"), false, "", []string{"agent", "show", "nobody"}, 1},
		{"agent/unreachable", map[string]string{"DEEDGLASS_SERVER": "http://127.0.0.1:1", "DEEDGLASS_API_KEY": "adm", "NO_COLOR": "1"}, false, "",
			[]string{"agent", "list"}, 1},
	}
	for _, c := range cases {
		r := cliIn(t, []byte(c.stdin), c.env, c.tty, c.args...)
		if r.code != c.code {
			t.Errorf("%s: exit %d, want %d\n%s%s", c.name, r.code, c.code, r.stdout, r.stderr)
		}
		// Relative times move; the fixture makes them 2m and 1h ago.
		golden(t, c.name, r)
	}
}

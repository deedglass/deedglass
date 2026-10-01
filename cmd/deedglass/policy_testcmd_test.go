package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var secondsRE = regexp.MustCompile(`[0-9]+\.[0-9] s\b`)

// normalizeTest removes the run time, the only part of the output that varies.
func normalizeTest(r result) result {
	r.stdout = secondsRE.ReplaceAllString(r.stdout, "N s")
	r.stdout = regexp.MustCompile(`"(elapsed_us|elapsed_ms)": ?[0-9]+`).ReplaceAllString(r.stdout, `"$1":0`)
	return r
}

// TestPolicyTestGoldens covers deedglass policy test (S12 §5): the --dir layout, a failing case
// with its diff, an uncovered rule, cases-file errors with their line and did-you-mean, --run,
// --json and every exit code.
func TestPolicyTestGoldens(t *testing.T) {
	const ex = "../../examples/policies"
	const td = "testdata/policytest/"
	cases := []struct {
		name string
		tty  bool
		args []string
		code int
	}{
		{"test_dir", false, []string{"policy", "test", "--dir", ex, "--require-coverage", "--voice"}, 0},
		{"test_dir_color", true, []string{"policy", "test", "--dir", ex}, 0},
		{"test_one", false, []string{"policy", "test", ex + "/postgres-safe.yaml"}, 0},
		{"test_run", false, []string{"policy", "test", ex + "/postgres-safe.yaml", "--run", "DROP|TRUNCATE"}, 0},
		{"test_fail", false, []string{"policy", "test", td + "fail.cases.yaml"}, 1},
		{"test_uncovered", false, []string{"policy", "test", td + "fail.cases.yaml", "--require-coverage", "--run", "reads"}, 1},
		{"test_uncovered_ok", false, []string{"policy", "test", td + "fail.cases.yaml", "--run", "reads|describe"}, 0},
		{"test_bad_input", false, []string{"policy", "test", td + "bad_input.cases.yaml"}, 1},
		{"test_bad_expect", false, []string{"policy", "test", td + "bad_expect.cases.yaml"}, 1},
		{"test_json", false, []string{"policy", "test", ex + "/gmail-drafts-only.yaml", "--json"}, 0},
		{"test_no_args", false, []string{"policy", "test"}, 2},
		{"test_no_policy_key", false, []string{"policy", "test", td + "no_policy_key.cases.yaml"}, 2},
		{"test_bad_run", false, []string{"policy", "test", ex + "/postgres-safe.yaml", "--run", "("}, 2},
		{"test_dir_and_file", false, []string{"policy", "test", "--dir", ex, ex + "/postgres-safe.yaml"}, 2},
		{"test_missing_cases", false, []string{"policy", "test", "testdata/policy/policies/postgres-safe.yaml"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := normalizeTest(cli(t, nil, c.tty, c.args...))
			if r.code != c.code {
				t.Errorf("exit %d, want %d\n%s%s", r.code, c.code, r.stdout, r.stderr)
			}
			golden(t, "policytest/"+c.name, r)
		})
	}
}

// fakePolicyServer answers the routes push and activate call, the way the real server does.
type fakePolicyServer struct {
	mu sync.Mutex
	fakeCfg
	calls  []string
	bodies []string
}

type fakeCfg struct {
	exists, same bool
	enforce      string
}

func (f *fakePolicyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, string(b))
	w.Header().Set("Content-Type", "application/json")
	const pol = `{"object":"policy","id":"pol_01jb8m2x7k4q9r3t5v6w8y0z1a","name":"postgres-safe","description":"Reads run freely.","status":"inactive",
	  "active_version":null,"shadow_version":null,"latest_version":1,"created_at":"2026-10-04T09:12:44.000Z","updated_at":"2026-10-04T09:12:44.000Z"}`
	const ver = `{"object":"policy_version","id":"pv_01jb8m3c0d2f4g6h8j0k2m4n6p","policy":"pol_01jb8m2x7k4q9r3t5v6w8y0z1a","version":1,"name":"postgres-safe",
	  "status":"inactive","version_hash":"sha256:2c4c3f1058e5b52c0c96469dcde7354e1047cd4f238e785c074e5566efc504fd","source_hash":"sha256:a41b",
	  "lang":"deedglass.policy/v1","origin":"manual","author":{"object":"api_key","id":"key_01jb"},"notes":null,"rules":12,"budgets":2,"warnings":[],
	  "created_at":"2026-10-04T09:12:44.000Z"}`
	act := func(mode string, deeds string, promoted bool) string {
		p := "false"
		if promoted {
			p = "true"
		}
		return `{"object":"policy_activation","id":"pa_01jb8p0z","policy":"pol_01jb8m2x7k4q9r3t5v6w8y0z1a","name":"postgres-safe","mode":"` + mode + `",
		  "version":{"id":"pv_01jb8m3c0d2f4g6h8j0k2m4n6p","version":1,"version_hash":"sha256:2c4c"},"previous":null,"promoted_from_shadow":` + p + `,
		  "acknowledged_warnings":[],"reason":"Guard orders","activated_by":{"object":"api_key","id":"key_01jb"},"activated_at":"2026-10-04T11:02:10.000Z",
		  "deactivated_at":null,"deactivated_by":null,"deeds":[` + deeds + `]}`
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/policies/postgres-safe":
		if !f.exists {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"There's no policy postgres-safe in this org.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/not_found"}}`)
			return
		}
		_, _ = io.WriteString(w, pol)
	case r.Method == "POST" && r.URL.Path == "/v1/policies":
		w.WriteHeader(201)
		_, _ = io.WriteString(w, strings.TrimSuffix(pol, "}")+`,"version":`+ver+`}`)
	case r.Method == "POST" && r.URL.Path == "/v1/policies/pol_01jb8m2x7k4q9r3t5v6w8y0z1a/versions":
		if !f.same {
			w.WriteHeader(201)
		}
		_, _ = io.WriteString(w, ver)
	case r.Method == "GET" && r.URL.Path == "/v1/policies/active":
		_, _ = io.WriteString(w, `{"object":"policy_active_set","enforce":{"policy":"pol_x","name":"`+f.enforce+`","version":1,"version_id":"pv_x","version_hash":"sha256:26",
		  "activation":"pa_x","activated_at":"2026-10-04T09:00:00.000Z","deed":"deed_x"},"shadow":null}`)
	case r.Method == "GET" && r.URL.Path == "/v1/whoami":
		_, _ = io.WriteString(w, `{"object":"whoami","org":{"id":"org_01","name":"Acme Robotics","slug":"acme","stopped":false}}`)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/activate"):
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		if body["mode"] == "shadow" {
			_, _ = io.WriteString(w, act("shadow", `"deed_01jb8p1a1c3"`, false))
			return
		}
		_, _ = io.WriteString(w, act("enforce", `"deed_01jb8p1a1c3","deed_01jb8p2m4x9"`, true))
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deactivate"):
		_, _ = io.WriteString(w, act("shadow", `"deed_01jb8p3c"`, false))
	default:
		w.WriteHeader(404)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"no","param":null,"request_id":"req_1","doc_url":"x"}}`)
	}
}

// TestPolicyServerGoldens covers push and activate against a fake server (S12 §5).
func TestPolicyServerGoldens(t *testing.T) {
	const ps = "../../examples/policies/postgres-safe.yaml"
	cases := []struct {
		name   string
		fake   fakeCfg
		tty    bool
		stdin  string
		args   []string
		code   int
		expect []string // the calls the command makes
	}{
		{"push_new", fakeCfg{}, false, "", []string{"policy", "push", ps, "--notes", "Orders DB, first cut"}, 0,
			[]string{"GET /v1/policies/postgres-safe", "POST /v1/policies"}},
		{"push_version", fakeCfg{exists: true}, false, "", []string{"policy", "push", ps}, 0,
			[]string{"GET /v1/policies/postgres-safe", "POST /v1/policies/pol_01jb8m2x7k4q9r3t5v6w8y0z1a/versions"}},
		{"push_same", fakeCfg{exists: true, same: true}, false, "", []string{"policy", "push", ps}, 0, nil},
		{"push_invalid", fakeCfg{}, false, "", []string{"policy", "push", "testdata/policy/refunds.yaml"}, 1, []string{}},
		{"activate_shadow", fakeCfg{exists: true, enforce: "default-deny"}, false, "",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--mode", "shadow", "--reason", "Watch it for a day before enforcing"}, 0, nil},
		{"activate_enforce_tty", fakeCfg{exists: true, enforce: "default-deny"}, true, "postgres-safe\n",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--mode", "enforce", "--reason", "Guard the orders database before the pilot"}, 0, nil},
		{"activate_enforce_mistyped", fakeCfg{exists: true, enforce: "default-deny"}, true, "postgres\n",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--mode", "enforce", "--reason", "Guard orders"}, 1, nil},
		{"activate_enforce_no_tty", fakeCfg{exists: true}, false, "",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--mode", "enforce", "--reason", "Guard orders"}, 2, []string{}},
		{"activate_enforce_yes", fakeCfg{exists: true}, false, "",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--mode", "enforce", "--reason", "Guard orders", "--yes"}, 0, nil},
		{"activate_clear", fakeCfg{exists: true}, false, "",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--clear", "--reason", "Seen enough"}, 0, nil},
		{"activate_no_reason", fakeCfg{exists: true}, false, "",
			[]string{"policy", "activate", "postgres-safe", "--version", "1", "--mode", "shadow"}, 2, []string{}},
		{"activate_bad_mode", fakeCfg{exists: true}, false, "",
			[]string{"policy", "activate", "postgres-safe", "--mode", "both", "--reason", "x"}, 2, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakePolicyServer{fakeCfg: c.fake}
			ts := httptest.NewServer(f)
			defer ts.Close()
			env := map[string]string{"DEEDGLASS_SERVER": ts.URL, "DEEDGLASS_API_KEY": "dg_test_AAAAAAAA_" + strings.Repeat("B", 32)}
			r := cliIn(t, []byte(c.stdin), env, c.tty, c.args...)
			if r.code != c.code {
				t.Errorf("exit %d, want %d\n%s%s", r.code, c.code, r.stdout, r.stderr)
			}
			if c.expect != nil && strings.Join(f.calls, ",") != strings.Join(c.expect, ",") {
				t.Errorf("calls %v, want %v", f.calls, c.expect)
			}
			golden(t, "policytest/"+c.name, r)
		})
	}
}

// A bare --server is the configured server, not a value for the next flag.
func TestBareServerFlag(t *testing.T) {
	g, rest, err := parseGlobals([]string{"policy", "evaluate", "--server", "--tool", "postgres.query"})
	if err != nil || !g.ServerFlag || g.Server != "" || strings.Join(rest, " ") != "policy evaluate --tool postgres.query" {
		t.Fatalf("%+v %v %v", g, rest, err)
	}
	g, _, _ = parseGlobals([]string{"--server", "http://x:1", "policy"})
	if !g.ServerFlag || g.Server != "http://x:1" {
		t.Fatalf("%+v", g)
	}
	g, _, _ = parseGlobals([]string{"policy", "evaluate", "--server"})
	if !g.ServerFlag || g.Server != "" {
		t.Fatalf("%+v", g)
	}
}

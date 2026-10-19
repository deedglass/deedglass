//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/store/storetest"
)

// phiPlanted are the synthetic patient values the PHI-mode test sends and receives. Once the
// actions are sealed, none of them may be found in any stored row, deed or server log line.
var phiPlanted = []string{"Jane Q Synthetic", "MRN-0042-SYN", "72.123456", "chest pain, synthetic"}

const phiConnector = `type: rest
name: notes
description: A clinical-notes API (synthetic data) for the PHI-mode test.
config:
  base_url: %s
  auth: { kind: none }
  allowed_methods: [GET, POST, PATCH]
  allowed_path_prefixes: [/v1/notes]
  tools:
    - name: get_note
      method: GET
      path: /v1/notes/{id}
      args: { id: { in: path, type: string, pattern: "^[a-z0-9-]{1,40}$", required: true } }
    - name: create_note
      method: POST
      path: /v1/notes
      args:
        patient: { in: body, type: string, required: true }
        text:    { in: body, type: string, required: true }
        value:   { in: body, type: number, required: true }
      readback:
        tool: get_note
        args: { id: "{{result.body.id}}" }
        expect: { "body.text": "{{args.text}}", "body.value": "{{args.value}}" }
    - name: amend_note
      method: PATCH
      path: /v1/notes/{id}
      args:
        id:   { in: path, type: string, pattern: "^[a-z0-9-]{1,40}$", required: true }
        text: { in: body, type: string, required: true }
`

const phiPolicy = `deedglass: policy/v1
name: notes-phi
description: Clinical notes in a PHI-mode org (HC-P09).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: note-reads
    match: { tool: ["notes.get_note"] }
    decision: allow
  - id: note-create
    match: { tool: ["notes.create_note"] }
    decision: allow
    verify: L1
  - id: note-amend
    match: { tool: ["notes.amend_note"] }
    decision: require_approval
    reason: A clinician reviews every change to a note.
    approval: { approvers: [role:admin], required: 1, expires_in: 1h }
`

// TestPHIModeLive is HC-P09 on a real server (attack case I11's shape): an org in PHI mode, a REST
// connector whose requests and answers carry synthetic patient values, a checked create, a create
// the service refuses (its answer echoes the patient), and a change that waits for approval and is
// cancelled. Once the actions are sealed, the serve job replaces their payloads with ids and
// digests: no planted value is left in any table, in any deed or in the server's log, the approval
// snapshot never held one, the API still shows the verdicts and the identifiers, and the deeds
// still verify.
func TestPHIModeLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	var mu sync.Mutex
	notes := map[string]map[string]any{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/v1/notes/")
		switch r.Method {
		case http.MethodPost:
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in["value"] == float64(0) {
				w.WriteHeader(400)
				_, _ = fmt.Fprintf(w, `{"error":"no chart for patient %v (%v)"}`, in["patient"], in["text"])
				return
			}
			in["id"] = fmt.Sprintf("note-%d", len(notes)+1)
			notes[in["id"].(string)] = in
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(in)
		case http.MethodGet:
			n, ok := notes[id]
			if !ok {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":"not found"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(n)
		default:
			w.WriteHeader(405)
		}
	}))
	defer up.Close()

	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 11)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}
	s := startServe(t, env, "--migrate")
	var base string
	select {
	case a := <-s.addr:
		base = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(env map[string]string, args ...string) result {
		t.Helper()
		return cli(t, env, false, args...)
	}
	r := run(env, "org", "create", "--name", "Synthetic Clinic", "--slug", "clinic", "--owner-email", "owner@clinic.test", "--admin-key", "--json")
	var created struct {
		Org      struct{ ID string }     `json:"org"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	admin := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = admin
	// PATCH /v1/org's settings.phi_mode needs an owner's session (internal/identity TestOrgPHIMode
	// covers it); here the setting is written the way that handler writes it.
	adm := e.Admin(t)
	if _, err := adm.Exec(context.Background(), `UPDATE deedglass.orgs SET settings = jsonb_set(settings, '{phi_mode}', 'true'::jsonb)
	    WHERE slug = 'clinic'`); err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(t.TempDir(), "notes-phi.yaml")
	_ = os.WriteFile(pf, []byte(phiPolicy), 0o600)
	if r = run(env, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run(env, "policy", "activate", "notes-phi", "--version", "1", "--mode", "enforce", "--reason", "PHI mode", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	cf := filepath.Join(t.TempDir(), "notes.yaml")
	_ = os.WriteFile(cf, []byte(fmt.Sprintf(phiConnector, up.URL)), 0o600)
	if r = run(env, "connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("connector: %+v", r)
	}
	call := func(key, method, p, body string, hdr ...string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, base+p, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := call(admin, "POST", "/v1/agents", `{"name":"scribe-agent"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(admin, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	agentEnv := map[string]string{}
	for k, v := range env {
		agentEnv[k] = v
	}
	agentEnv["DEEDGLASS_API_KEY"] = ak.Secret

	type actionView struct {
		ID      string `json:"id"`
		State   string `json:"state"`
		Witness *struct {
			Level, Verdict string
		} `json:"witness"`
	}
	act := func(tool, args string) actionView {
		t.Helper()
		r := run(agentEnv, "act", tool, "--args", args, "--json")
		var a actionView
		if err := json.Unmarshal([]byte(r.stdout), &a); err != nil || a.ID == "" {
			t.Fatalf("%s: %+v", tool, r)
		}
		return a
	}
	// 1. A checked create carrying the patient's values.
	ok := act("notes.create_note", `{"patient":"Jane Q Synthetic (MRN-0042-SYN)","text":"chest pain, synthetic","value":72.123456}`)
	if ok.Witness == nil || ok.Witness.Verdict != "VERIFIED" || ok.Witness.Level != "L1" {
		t.Fatalf("create: %+v", ok)
	}
	// 2. The service refuses a create and echoes the patient in its answer.
	bad := act("notes.create_note", `{"patient":"Jane Q Synthetic","text":"chest pain, synthetic","value":0}`)
	// 3. A change that waits for approval (its snapshot is written now, in PHI form), then is cancelled.
	pending := act("notes.amend_note", `{"id":"note-1","text":"Jane Q Synthetic, chest pain, synthetic"}`)
	if pending.State != "pending_approval" {
		t.Fatalf("amend: %+v", pending)
	}
	var snapshot string
	if err := adm.QueryRow(context.Background(), `SELECT context::text FROM deedglass.approvals WHERE action_id = $1`, pending.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "PHI mode is on") || !strings.Contains(snapshot, `"id": "note-1"`) {
		t.Fatalf("the approval snapshot: %s", snapshot)
	}
	// While it waits, the action keeps its request (an approved action runs with it): the sweep
	// below would see it, which is what makes its zero count mean something.
	var held int
	if err := adm.QueryRow(context.Background(), `SELECT count(*) FROM deedglass.actions a WHERE a.id = $1 AND a::text LIKE '%chest pain, synthetic%'`,
		pending.ID).Scan(&held); err != nil || held != 1 {
		t.Fatalf("the waiting action's request: %d (%v)", held, err)
	}
	if code, body := call(ak.Secret, "POST", "/v1/actions/"+pending.ID+"/cancel", "", "Idempotency-Key", "phi-cancel-1"); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}

	// The serve job minimizes every sealed PHI-mode action (it ticks every 2 s).
	deadline := time.Now().Add(30 * time.Second)
	for {
		var total, left int
		if err := adm.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE payload_minimized_at IS NULL)
		    FROM deedglass.actions WHERE phi`).Scan(&total, &left); err != nil {
			t.Fatal(err)
		}
		if total == 3 && left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 30 s: %d PHI-mode actions, %d still hold payloads", total, left)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// What the API shows now: verdicts and identifiers, digests for every value.
	code, body = call(admin, "GET", "/v1/actions/"+pending.ID, "")
	if code != 200 || !strings.Contains(body, `"id": "note-1"`) || !strings.Contains(body, `"text": "sha256:`) {
		t.Fatalf("the cancelled action: %d %s", code, body)
	}
	code, body = call(admin, "GET", "/v1/actions/"+ok.ID+"?expand[]=witness.detail", "")
	if code != 200 || !strings.Contains(body, `"verdict": "VERIFIED"`) || !strings.Contains(body, `"patient": "sha256:`) {
		t.Fatalf("the checked create: %d %s", code, body)
	}
	t.Logf("GET /v1/actions/%s after minimizing:\n%s", ok.ID, body)
	if code, body := call(admin, "GET", "/v1/actions/"+bad.ID, ""); code != 200 || strings.Contains(body, "no chart for patient") {
		t.Fatalf("the refused create: %d %s", code, body)
	}

	// The sweep: every table of the schema, every deed, the server's log.
	s.sigs <- syscall.SIGTERM
	if c := s.wait(t); c != 0 {
		t.Fatalf("exit %d\n%s", c, s.err)
	}
	rows, err := adm.Query(context.Background(), `SELECT table_name FROM information_schema.tables
	    WHERE table_schema = 'deedglass' AND table_type = 'BASE TABLE' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	if len(tables) < 30 {
		t.Fatalf("only %d tables: %v", len(tables), tables)
	}
	brows, err := adm.Query(context.Background(), `SELECT table_name, column_name FROM information_schema.columns
	    WHERE table_schema = 'deedglass' AND data_type = 'bytea' ORDER BY 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	var byteaCols [][2]string
	for brows.Next() {
		var tb, col string
		_ = brows.Scan(&tb, &col)
		if slices.Contains(tables, tb) {
			byteaCols = append(byteaCols, [2]string{tb, col})
		}
	}
	brows.Close()
	if !slices.Contains(byteaCols, [2]string{"deeds", "envelope"}) || !slices.Contains(byteaCols, [2]string{"idempotency_keys", "response_body"}) {
		t.Fatalf("bytea columns: %v", byteaCols)
	}
	for _, p := range phiPlanted {
		for _, tb := range tables {
			var n int
			// The table name comes from information_schema, never from input.
			q := fmt.Sprintf(`SELECT count(*) FROM deedglass.%q x WHERE x::text LIKE '%%' || $1 || '%%'`, tb)
			if err := adm.QueryRow(context.Background(), q, p).Scan(&n); err != nil {
				t.Fatalf("%s: %v", tb, err)
			}
			if n != 0 {
				var sample string
				_ = adm.QueryRow(context.Background(), fmt.Sprintf(`SELECT left(x::text, 600) FROM deedglass.%q x WHERE x::text LIKE '%%' || $1 || '%%' LIMIT 1`, tb), p).Scan(&sample)
				t.Errorf("%q is still in %d row(s) of deedglass.%s: %s", p, n, tb, sample)
			}
		}
		// Row text shows bytea as hex, so bytea columns (deed envelopes, stored responses) are
		// searched as bytes.
		for _, bc := range byteaCols {
			var n int
			q := fmt.Sprintf(`SELECT count(*) FROM deedglass.%q WHERE position(convert_to($1, 'UTF8') in %q) > 0`, bc[0], bc[1])
			if err := adm.QueryRow(context.Background(), q, p).Scan(&n); err != nil || n != 0 {
				t.Errorf("%q is in %d row(s) of deedglass.%s.%s (%v)", p, n, bc[0], bc[1], err)
			}
		}
		if logs := s.out.String() + s.err.String(); strings.Contains(logs, p) {
			i := strings.Index(logs, p)
			t.Errorf("%q is in the server's log: …%s…", p, logs[max(0, i-200):min(len(logs), i+100)])
		}
	}
	// The deeds still verify.
	bundle := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if br := run(env, "deeds", "export", "--out", bundle); br.code != 0 {
		t.Fatalf("export: %+v", br)
	}
	if vr := run(env, "verify", bundle); vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v", vr)
	}
}

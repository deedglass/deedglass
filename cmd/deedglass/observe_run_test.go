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
	"time"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/observe"
	"github.com/deedglass/deedglass/internal/observeapi"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// FEAT-04: observe run around a coding agent's command, against a fake observe API. At the end the
// run prints the caught line, the free way back (the undo SQL saved to --undo-sql's file, or the
// command that prints it) and the any-writer note. The real decoder and psql run in
// TestObserveLive (integration).

const watchID = "obs_01k7wat0000000000000000000"

const watchSQL = "-- Deedglass undo SQL for watched session " + watchID + " (connector dev).\nBEGIN;\nSELECT 1;\nCOMMIT;\n"

// TestObserveRunChild is the agent the observe run tests start (the helper-process pattern): it
// refuses to run with any DEEDGLASS_* variable and exits with the code it was given.
func TestObserveRunChild(t *testing.T) {
	if os.Getenv("FEAT04_CHILD") != "1" {
		t.Skip("run by the observe run tests")
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), "DEEDGLASS_") {
			os.Exit(9)
		}
	}
	code := 0
	if os.Getenv("FEAT04_CHILD_EXIT") == "3" {
		code = 3
	}
	os.Exit(code)
}

type watchAPI struct {
	mu       sync.Mutex
	calls    []string
	state    string // the state close answers with
	txs      int
	caughtOn bool
	sql      *string
	refusals []string
	preview  int           // the status the preview answers with (200 when 0)
	decoder  *decoder.View // what GET …/decoder answers (404 when nil)
}

func (a *watchAPI) session(state string) observeapi.SessionJSON {
	opened := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	x := observeapi.SessionJSON{Object: "observe_session", ID: watchID, Connector: observeapi.ConnectorJSON{ID: "con_01k7wat0000000000000000000", Name: "dev"},
		State: state, OpenedAt: opened, Flags: []string{}, Watched: []string{"public.orders"}, Caught: []caught.Item{}, Notes: []string{},
		Observed: observe.NoteObserved, Attribution: observe.NoteAttribution,
		Summary: observeapi.SummaryJSON{Tables: map[string]observe.Counts{}}}
	if state == observe.StateOpen {
		return x
	}
	closed := opened.Add(4 * time.Minute)
	x.ClosedAt = &closed
	x.Summary.Transactions = a.txs
	if a.txs > 0 {
		at := opened.Add(2 * time.Minute)
		x.Summary.Tables["public.orders"] = observe.Counts{Update: 60}
		x.Summary.Rows, x.Summary.FirstCommit, x.Summary.LastCommit = 60, &at, &at
		x.Undo.Possible = true
	}
	if a.caughtOn {
		x.Caught = caught.Session(watchID, []caught.Frame{{ID: "dtx_01k7wat0000000000000000000", Counts: map[string]caught.Counts{"public.orders": {Update: 60}}}}, 50, true)
	}
	return x
}

func (a *watchAPI) serve(t *testing.T) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		a.mu.Lock()
		a.calls = append(a.calls, r.Method+" "+r.URL.Path)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/observe/sessions":
			w.WriteHeader(201)
			_ = enc.Encode(a.session(observe.StateOpen))
		case r.Method == "POST" && r.URL.Path == "/v1/observe/sessions/"+watchID+"/close",
			r.Method == "GET" && r.URL.Path == "/v1/observe/sessions/"+watchID:
			_ = enc.Encode(a.session(a.state))
		case r.Method == "GET" && r.URL.Path == "/v1/connectors/dev/decoder" && a.decoder != nil:
			_ = enc.Encode(a.decoder)
		case r.Method == "GET" && r.URL.Path == "/v1/observe/sessions/"+watchID+"/undo_preview":
			if r.URL.Query().Get("allow_triggers") != "true" {
				t.Errorf("the preview for the undo SQL must allow triggers: %s", r.URL.RawQuery)
			}
			if a.preview != 0 {
				w.WriteHeader(a.preview)
				io.WriteString(w, `{"error":{"type":"api_error","code":"internal_error","message":"Something went wrong on our side.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/internal_error"}}`)
				return
			}
			refusals := a.refusals
			if refusals == nil {
				refusals = []string{}
			}
			_ = enc.Encode(observeapi.PreviewJSON{Object: "observe_undo_preview", Session: watchID, Available: a.sql != nil, SQL: a.sql,
				Refusals: refusals, FreePath: caught.FreeSessionPath(watchID), Observed: observe.NoteObserved})
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"Not found.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/not_found"}}`)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(s.Close)
	return s
}

func (a *watchAPI) called(call string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c == call {
			return true
		}
	}
	return false
}

func watchRun(t *testing.T, a *watchAPI, extra ...string) result {
	t.Helper()
	srv := a.serve(t)
	t.Setenv("FEAT04_CHILD", "1")
	t.Setenv("DEEDGLASS_PROBE_FOR_THE_CHILD", "never")
	env := map[string]string{"DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": "adm", "NO_COLOR": "1"}
	args := append([]string{"observe", "run", "--connector", "dev"}, extra...)
	args = append(args, "--", os.Args[0], "-test.run=^TestObserveRunChild$")
	return cli(t, env, false, args...)
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("missing %q in:\n%s", w, out)
		}
	}
}

func TestObserveRunUndoSQL(t *testing.T) {
	sql := watchSQL
	t.Run("caught, saved", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "undo.sql")
		// A symlink planted at the path is replaced, never followed.
		target := filepath.Join(dir, "elsewhere.txt")
		if err := os.WriteFile(target, []byte("keep me\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path)
		t.Logf("observe run:\n%s", r.stderr)
		if r.code != 0 || r.stdout != "" {
			t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		mustContain(t, r.stderr,
			"◐ Watching dev (public.orders) · session "+watchID,
			"✓ Session sealed · 1 transaction · 09:02:00–09:02:00",
			"  public.orders  60 updated",
			"Deedglass caught this: one transaction changed 60 rows in public.orders.",
			"  Undo this session in one step (Pro): deedglass observe undo "+watchID,
			// The planted symlink counts as an earlier file: it is replaced, and the line says so.
			"  Undo SQL saved to "+path+" (free), replacing an earlier file. Deedglass doesn't run it: review it, then run it yourself:\n",
			"    psql \"$DATABASE_URL\" -v ON_ERROR_STOP=1 -f "+path+"\n",
			"  It holds row values: keep it out of version control and out of the agent's folder.\n",
			"  "+observe.NoteAttribution+"\n",
			observe.NoteObserved+"\n")
		if strings.Contains(r.stderr, "Or print the undo SQL") || strings.Contains(r.stderr, "The agent can read") || strings.Contains(r.stderr, "decoder") {
			t.Fatalf("the saved file is the free path; the generic line is redundant:\n%s", r.stderr)
		}
		// The caught line comes first, then the free path, then the any-writer note.
		if i, j, k := strings.Index(r.stderr, "Deedglass caught this"), strings.Index(r.stderr, "Undo SQL saved"), strings.Index(r.stderr, observe.NoteAttribution); !(i < j && j < k) {
			t.Fatalf("order: caught %d, saved %d, any writer %d\n%s", i, j, k, r.stderr)
		}
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the undo SQL file: %v %v", fi.Mode(), err)
		}
		if b, _ := os.ReadFile(path); string(b) != sql {
			t.Fatalf("the file holds %q", b)
		}
		if b, _ := os.ReadFile(target); string(b) != "keep me\n" {
			t.Fatalf("the symlink's target was written: %q", b)
		}
		if left, _ := filepath.Glob(filepath.Join(dir, ".deedglass-undo-*")); len(left) != 0 {
			t.Fatalf("a temporary file was left behind: %v", left)
		}
	})
	t.Run("without --undo-sql: the command that prints it", func(t *testing.T) {
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql}
		r := watchRun(t, a)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "Deedglass caught this: one transaction changed 60 rows in public.orders.",
			"  Or print the undo SQL and run it yourself (free): deedglass observe show "+watchID+" --undo-sql",
			"  "+observe.NoteAttribution)
		if a.called("GET /v1/observe/sessions/" + watchID + "/undo_preview") {
			t.Fatal("the preview was read without --undo-sql")
		}
	})
	t.Run("not caught, saved", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "  Undo SQL saved to "+path+" (free).", "  "+observe.NoteAttribution)
		if strings.Contains(r.stderr, "caught this") {
			t.Fatalf("an ordinary session shows no caught line:\n%s", r.stderr)
		}
	})
	t.Run("the child's exit code still passes through", func(t *testing.T) {
		t.Setenv("FEAT04_CHILD_EXIT", "3")
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != 3 {
			t.Fatalf("exit %d, want 3 (9 means the child saw a DEEDGLASS_ variable)\n%s", r.code, r.stderr)
		}
		if b, _ := os.ReadFile(path); string(b) != sql {
			t.Fatalf("the file holds %q", b)
		}
	})
	t.Run("the record can't undo it", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, refusals: []string{"public.orders was truncated; the change log holds no rows to put back."}}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "  No undo SQL saved: public.orders was truncated; the change log holds no rows to put back.\n", "  "+observe.NoteAttribution)
		if strings.Contains(r.stderr, "(Pro)") {
			t.Fatalf("a one-step undo the preview already refused is offered:\n%s", r.stderr)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a file was written: %v", err)
		}
	})
	t.Run("nothing committed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "✓ Session sealed · 0 transactions",
			"  No undo SQL saved: the change log holds no row changes on the watched tables. Schema changes (a DROP TABLE, an ORM's reset) never appear in it.\n")
		if strings.Contains(r.stderr, observe.NoteAttribution) || a.called("GET /v1/observe/sessions/"+watchID+"/undo_preview") {
			t.Fatalf("an empty session needs neither the note nor a preview:\n%s", r.stderr)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a file was written: %v", err)
		}
	})
	t.Run("the preview fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql, preview: 500}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != 1 {
			t.Fatalf("exit %d, want 1\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "  Couldn't read the undo SQL: ", ". Next: (umask 077; deedglass observe show "+watchID+" --undo-sql > "+path+")", "  "+observe.NoteAttribution)
	})
	t.Run("the preview fails and the agent failed: the agent's code wins", func(t *testing.T) {
		t.Setenv("FEAT04_CHILD_EXIT", "3")
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql, preview: 500}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != 3 {
			t.Fatalf("exit %d, want 3\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "  Couldn't read the undo SQL: ")
	})
	t.Run("still closing: exit 2, the line, no preview", func(t *testing.T) {
		saved := sealWait
		sealWait = 0
		t.Cleanup(func() { sealWait = saved })
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateClosing, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path)
		if r.code != ExitUsage {
			t.Fatalf("exit %d, want 2\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "◐ Session closing", "  The undo SQL isn't saved: the session is still closing. Next: (umask 077; deedglass observe show "+watchID+" --undo-sql > "+path+")")
		if a.called("GET /v1/observe/sessions/" + watchID + "/undo_preview") {
			t.Fatal("the preview was read for a closing session")
		}
	})
	t.Run("a folder: undo-<session>.sql in it", func(t *testing.T) {
		dir := t.TempDir()
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql}
		r := watchRun(t, a, "--undo-sql", dir)
		want := filepath.Join(dir, "undo-"+watchID+".sql")
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "  Undo SQL saved to "+want+" (free).", "-f "+want+"\n")
		if b, _ := os.ReadFile(want); string(b) != sql {
			t.Fatalf("the file holds %q", b)
		}
	})
	t.Run("an earlier file: replaced when saved, said so", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		if err := os.WriteFile(path, []byte("-- earlier\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &watchAPI{state: observe.StateSealed, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path)
		mustContain(t, r.stderr, "  Undo SQL saved to "+path+" (free), replacing an earlier file.")
		if b, _ := os.ReadFile(path); r.code != 0 || string(b) != sql {
			t.Fatalf("exit %d, the file holds %q\n%s", r.code, b, r.stderr)
		}
	})
	t.Run("an earlier file: kept when nothing is saved, said so", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		if err := os.WriteFile(path, []byte("-- earlier\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &watchAPI{state: observe.StateSealed}
		r := watchRun(t, a, "--undo-sql", path)
		mustContain(t, r.stderr, " The "+path+" already there is from an earlier session; it was left as it was.")
		if b, _ := os.ReadFile(path); r.code != 0 || string(b) != "-- earlier\n" {
			t.Fatalf("exit %d, the file holds %q\n%s", r.code, b, r.stderr)
		}
	})
	t.Run("the decoder stopped: a dropped table left the publication", func(t *testing.T) {
		reason := decoder.ReasonPublicationInvalid
		a := &watchAPI{state: observe.StateSealed, decoder: &decoder.View{Object: "decoder", Name: "dev", Status: decoder.StatusHalted, Health: "halted",
			Reason: &reason, Detail: map[string]any{"publication": "deedglass_pub", "missing": []string{"public.orders"}}}}
		r := watchRun(t, a, "--undo-sql", filepath.Join(t.TempDir(), "undo.sql"))
		mustContain(t, r.stderr, "  No undo SQL saved: the change log holds no row changes",
			"  The decoder stopped: public.orders left the publication (a DROP TABLE, DROP SCHEMA or an ORM's reset does this). Rows it hadn't read by then aren't in this session, and schema changes never are. Next: deedglass decoder status --connector dev\n"+observe.NoteObserved)
	})
	t.Run("the admin key file in reach of the agent is named", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".deedglass"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".deedglass", "admin.key"), []byte("adm\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(dir, "app")
		if err := os.Mkdir(sub, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Chdir(sub)
		a := &watchAPI{state: observe.StateSealed}
		r := watchRun(t, a)
		real, _ := filepath.EvalSymlinks(dir)
		if r.code != 0 || (!strings.Contains(r.stderr, "  The agent can read "+filepath.Join(dir, ".deedglass", "admin.key")+": ") &&
			!strings.Contains(r.stderr, "  The agent can read "+filepath.Join(real, ".deedglass", "admin.key")+": ")) {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		mustContain(t, r.stderr, "with that admin key it could stop this session or leave its own transactions out. Move the file out of this folder and its parents.")
	})
	t.Run("--json: a failed save is an error envelope on stderr", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql, preview: 500}
		r := watchRun(t, a, "--undo-sql", path, "--json")
		if r.code != 1 || !strings.Contains(r.stderr, `"code":"undo_sql_not_saved"`) {
			t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
	})
	t.Run("a folder that can't be written is refused before the agent runs", func(t *testing.T) {
		dir := readOnlyDir(t)
		a := &watchAPI{state: observe.StateSealed, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", filepath.Join(dir, "undo.sql"))
		if r.code != ExitUsage || !strings.Contains(r.stderr, "--undo-sql: can't write in "+dir+": permission denied") {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		if a.called("POST /v1/observe/sessions") {
			t.Fatal("a session was opened")
		}
	})
	t.Run("- is refused before the agent runs", func(t *testing.T) {
		a := &watchAPI{state: observe.StateSealed, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", "-")
		if r.code != ExitUsage || !strings.Contains(r.stderr, "--undo-sql: name a file or a folder") || a.called("POST /v1/observe/sessions") {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
	})
	t.Run("a file as the folder is refused before the agent runs", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "plain")
		if err := os.WriteFile(file, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		a := &watchAPI{state: observe.StateSealed, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", filepath.Join(file, "undo.sql"))
		if r.code != ExitUsage || !strings.Contains(r.stderr, "--undo-sql: "+file+" isn't a folder") || a.called("POST /v1/observe/sessions") {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
	})
	t.Run("a missing folder is refused before the agent runs", func(t *testing.T) {
		a := &watchAPI{state: observe.StateSealed, txs: 1, sql: &sql}
		r := watchRun(t, a, "--undo-sql", filepath.Join(t.TempDir(), "nope", "undo.sql"))
		if r.code != ExitUsage || !strings.Contains(r.stderr, "--undo-sql: the folder ") || !strings.Contains(r.stderr, "doesn't exist") {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		if a.called("POST /v1/observe/sessions") {
			t.Fatal("a session was opened")
		}
	})
	t.Run("--json: the session on stdout, the saved line on stderr", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "undo.sql")
		a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql}
		r := watchRun(t, a, "--undo-sql", path, "--json")
		var x observeapi.SessionJSON
		if r.code != 0 || json.Unmarshal([]byte(r.stdout), &x) != nil || x.ID != watchID || len(x.Caught) != 1 {
			t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
		}
		mustContain(t, r.stderr, "Undo SQL saved to "+path+" (free).")
		if b, _ := os.ReadFile(path); string(b) != sql {
			t.Fatalf("the file holds %q", b)
		}
	})
}

// readOnlyDir is a folder this process can't create files in.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

// TestSaveUndoSQLWriteFails: a folder that stops taking files after the check (the agent's run can
// be long) leaves no file, a line with the way to print the SQL instead, and an error.
func TestSaveUndoSQLWriteFails(t *testing.T) {
	sql := watchSQL
	a := &watchAPI{state: observe.StateSealed, txs: 1, caughtOn: true, sql: &sql}
	srv := a.serve(t)
	x := a.session(observe.StateSealed)
	path := filepath.Join(readOnlyDir(t), "undo sql.sql")
	line, _, err := saveUndoSQL(t.Context(), &deedglass.Client{BaseURL: srv.URL, APIKey: "adm"}, &x, path)
	if err == nil || line != "  Couldn't save the undo SQL to "+path+": permission denied. Next: (umask 077; deedglass observe show "+watchID+" --undo-sql > '"+path+"')" {
		t.Fatalf("%q %v", line, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a file was written: %v", err)
	}
}

// TestSaveUndoSQLLines covers the lines that need no server: a session still closing, and one
// with nothing committed.
func TestSaveUndoSQLLines(t *testing.T) {
	x := &observeapi.SessionJSON{ID: watchID, State: observe.StateClosing}
	line, _, err := saveUndoSQL(t.Context(), nil, x, "my undo.sql")
	if err != nil || line != "  The undo SQL isn't saved: the session is still closing. Next: (umask 077; deedglass observe show "+watchID+" --undo-sql > 'my undo.sql')" {
		t.Fatalf("%q %v", line, err)
	}
	x.State = observe.StateSealed
	if line, _, err := saveUndoSQL(t.Context(), nil, x, "undo.sql"); err != nil || line != "  No undo SQL saved: the change log holds no row changes on the watched tables. Schema changes (a DROP TABLE, an ORM's reset) never appear in it." {
		t.Fatalf("%q %v", line, err)
	}
	// A path that starts with - is printed so that no command reads it as an option.
	x.State = observe.StateClosing
	if line, _, _ := saveUndoSQL(t.Context(), nil, x, "-undo.sql"); !strings.HasSuffix(line, "--undo-sql > ./-undo.sql)") {
		t.Fatalf("%q", line)
	}
}

// TestPrintSealedAnyWriter: the any-writer note follows the counts whenever something was
// committed, for observe stop as well as observe run.
func TestPrintSealedAnyWriter(t *testing.T) {
	a := &watchAPI{txs: 1, caughtOn: true}
	x := a.session(observe.StateSealed)
	var b strings.Builder
	printSealed(&b, (&Env{}).Style, &x, runEnd{})
	mustContain(t, b.String(), "Deedglass caught this: one transaction changed 60 rows in public.orders.",
		"  Or print the undo SQL and run it yourself (free): deedglass observe show "+watchID+" --undo-sql\n",
		"  "+observe.NoteAttribution+"\n", observe.NoteObserved)
	a.txs, a.caughtOn = 0, false
	x = a.session(observe.StateSealed)
	b.Reset()
	printSealed(&b, (&Env{}).Style, &x, runEnd{})
	if strings.Contains(b.String(), observe.NoteAttribution) {
		t.Fatalf("no rows, no note:\n%s", b.String())
	}
}

// TestChildEnv: the agent's command gets no DEEDGLASS_* variable, so an MCP server it starts
// (the plugin's deedglass mcp serve) finds no key or server in its environment either.
func TestChildEnv(t *testing.T) {
	got := childEnv([]string{"PATH=/bin", "DEEDGLASS_API_KEY=adm", "DEEDGLASS_SERVER=http://10.0.0.2:8080", "deedglass_x=1", "HOME=/h"})
	if strings.Join(got, " ") != "PATH=/bin HOME=/h" {
		t.Fatalf("%q", got)
	}
}

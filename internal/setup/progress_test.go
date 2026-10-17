package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// TestProgress (PRD-06): the step is the one after the furthest done, whatever came before it.
func TestProgress(t *testing.T) {
	srv := "http://127.0.0.1:8080"
	for _, c := range []struct {
		f    Facts
		step int
		next string
	}{
		{Facts{}, 1, "deedglass init"},
		{Facts{SetUp: true, Server: srv}, 2, "deedglass doctor"},
		{Facts{SetUp: true, Checked: true, Server: srv}, 3, "deedglass init --trigger"},
		{Facts{SetUp: true, Mismatch: "act_01k6h7kp6msmzrhdzwtcr3jnkp", Server: srv}, 4, "deedglass undo preview act_01k6h7kp6msmzrhdzwtcr3jnkp (it prints the undo run line)"},
		{Facts{SetUp: true, Checked: true, Mismatch: "act_x", Undo: true, Server: srv}, 5, "countersign the undo at http://127.0.0.1:8080/app/approvals (the sign-in link is in .deedglass/serve.log)"},
		{Facts{SetUp: true, Mismatch: "act_x", Undo: true, Undone: true, Server: srv}, 0, ""},
	} {
		if step, next := c.f.Progress(); step != c.step || next != c.next {
			t.Errorf("%+v: %d %q, want %d %q", c.f, step, next, c.step, c.next)
		}
	}
}

// fakeLedger answers GET /v1/actions like the server: MISMATCH and *.undo filters, the admin key.
func fakeLedger(t *testing.T, mismatch string, undoStatuses ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/actions" || r.Header.Get("Authorization") != "Bearer dg_test_admin" {
			t.Errorf("%s %s %q", r.Method, r.URL, r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		type row struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		data := []row{}
		q := r.URL.Query()
		switch {
		case q.Get("verdict") == "MISMATCH" && mismatch != "":
			data = append(data, row{mismatch, "executed"})
		case q.Get("tool") == "*.undo":
			for _, s := range undoStatuses {
				data = append(data, row{"act_undo", s})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "has_more": false})
	}))
	t.Cleanup(ts.Close)
	return ts, &hits
}

// initDir writes the files init leaves behind, with the stack listening on server.
func initDir(t *testing.T, server string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(Path(dir, ""), 0o700); err != nil {
		t.Fatal(err)
	}
	listen := strings.TrimPrefix(server, "http://")
	for name, v := range map[string]any{StateFile: State{Object: "init_state", Version: 1, Org: "org_x", Agent: "agt_x"}, StackFile: Stack{Object: "init_stack", Listen: listen}} {
		if err := writeJSON(Path(dir, name), v); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeSecret(Path(dir, AdminKeyFile), []byte("dg_test_admin\n"), true); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestReadFacts: files first, then the Ledger; an unreadable Ledger or a missing key leaves the
// Ledger facts unset, and a directory init didn't set up reads nothing at all.
func TestReadFacts(t *testing.T) {
	ctx := context.Background()
	ts, hits := fakeLedger(t, "act_01k6h7kp6msmzrhdzwtcr3jnkp")
	f := ReadFacts(ctx, initDir(t, ts.URL))
	if !f.SetUp || f.Server != ts.URL || f.Mismatch != "act_01k6h7kp6msmzrhdzwtcr3jnkp" || f.Undo || f.Undone || f.Checked {
		t.Fatalf("%+v", f)
	}
	if hits.Load() != 2 {
		t.Fatalf("%d reads", hits.Load())
	}
	ts2, _ := fakeLedger(t, "act_x", "pending_approval")
	if f := ReadFacts(ctx, initDir(t, ts2.URL)); !f.Undo || f.Undone {
		t.Fatalf("pending undo: %+v", f)
	}
	ts3, _ := fakeLedger(t, "act_x", "pending_approval", "executed")
	if f := ReadFacts(ctx, initDir(t, ts3.URL)); !f.Undo || !f.Undone {
		t.Fatalf("executed undo: %+v", f)
	}
	// The server is down: the files still say step 1 is done.
	down := initDir(t, "http://127.0.0.1:1")
	if f := ReadFacts(ctx, down); !f.SetUp || f.Mismatch != "" || f.Undo {
		t.Fatalf("server down: %+v", f)
	}
	// No admin key: no Ledger read at all.
	ts4, hits4 := fakeLedger(t, "act_x")
	nokey := initDir(t, ts4.URL)
	_ = os.Remove(Path(nokey, AdminKeyFile))
	if f := ReadFacts(ctx, nokey); !f.SetUp || f.Mismatch != "" || hits4.Load() != 0 {
		t.Fatalf("no key: %+v, %d reads", f, hits4.Load())
	}
	// Not an init directory, or a state without an agent: nothing.
	if f := ReadFacts(ctx, t.TempDir()); f != (Facts{}) {
		t.Fatalf("empty dir: %+v", f)
	}
	half := initDir(t, ts4.URL)
	_ = writeJSON(Path(half, StateFile), State{Org: "org_x"})
	if f := ReadFacts(ctx, half); f.SetUp {
		t.Fatalf("no agent: %+v", f)
	}
}

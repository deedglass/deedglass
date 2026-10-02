//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// liveStream collects one GET /v1/events stream on a real server.
type liveStream struct {
	mu     sync.Mutex
	events []deedglass.StreamEvent
	raw    strings.Builder // the first bytes, for the report's wire samples
	cancel context.CancelFunc
	done   chan error
}

func openLiveStream(t *testing.T, base, key string) *liveStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ls := &liveStream{cancel: cancel, done: make(chan error, 1)}
	s := &deedglass.EventStream{Client: &deedglass.Client{BaseURL: base, APIKey: key}}
	hello := make(chan struct{})
	var once sync.Once
	go func() {
		ls.done <- s.Run(ctx, func(ev deedglass.StreamEvent) error {
			ls.mu.Lock()
			ls.events = append(ls.events, ev)
			if ls.raw.Len() < 6000 {
				if ev.ID != "" {
					ls.raw.WriteString("id: " + ev.ID + "\n")
				}
				ls.raw.WriteString("event: " + ev.Type + "\ndata: " + string(ev.Data) + "\n\n")
			}
			ls.mu.Unlock()
			if ev.Type == "hello" {
				once.Do(func() { close(hello) })
			}
			return nil
		})
	}()
	select {
	case <-hello:
	case err := <-ls.done:
		t.Fatalf("stream ended before hello: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no hello")
	}
	t.Cleanup(cancel)
	return ls
}

func (ls *liveStream) snapshot() []deedglass.StreamEvent {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return append([]deedglass.StreamEvent(nil), ls.events...)
}

// settle waits until the stream has been quiet for a second.
func (ls *liveStream) settle(t *testing.T) []deedglass.StreamEvent {
	t.Helper()
	last, n := time.Now(), -1
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if m := len(ls.snapshot()); m != n {
			n, last = m, time.Now()
		} else if time.Since(last) > time.Second {
			break
		}
	}
	return ls.snapshot()
}

func eventOf(t *testing.T, ev deedglass.StreamEvent) deedglass.Event {
	t.Helper()
	var e deedglass.Event
	if err := json.Unmarshal(ev.Data, &e); err != nil {
		t.Fatalf("%s: %v", ev.Data, err)
	}
	return e
}

var lifecycleRank = map[string]int{"action.created": 0, "action.decided": 1, "action.executed": 2, "action.witnessed": 3}

// TestEventsLive is S24 §9 on a real server: dev fake-action --n 50 streams lifecycle-ordered
// events ending in deed.sealed, hydrated in the REST list shapes; another org's stream and an agent
// key's stream see none of them; a rolled-back transaction emits nothing; serve stops cleanly with
// streams open.
func TestEventsLive(t *testing.T) {
	m := newMCPLive(t)
	env := m.env
	acmeKey := env["DEEDGLASS_API_KEY"]
	r := cli(t, env, false, "org", "create", "--name", "Globex", "--slug", "globex", "--owner-email", "hank@globex.test", "--admin-key", "--json")
	var globex struct {
		Org      struct{ ID string }     `json:"org"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &globex) != nil || globex.AdminKey.Secret == "" {
		t.Fatalf("org create: %+v", r)
	}
	acme := openLiveStream(t, m.base, acmeKey)
	other := openLiveStream(t, m.base, globex.AdminKey.Secret)
	agent := openLiveStream(t, m.base, m.key) // claude-desktop: none of the fake agents' actions are its own

	fenv := map[string]string{}
	for k, v := range env {
		fenv[k] = v
	}
	fenv["DEEDGLASS_ENV"] = "dev"
	delete(fenv, "DEEDGLASS_API_KEY")
	if r := cli(t, fenv, false, "--org", "acme", "dev", "fake-action", "--n", "50", "--quiet"); r.code != 0 {
		t.Fatalf("fake-action: %+v", r)
	}
	evs := acme.settle(t)
	t.Logf("wire sample (acme, admin key):\n%s", acme.raw.String())

	// Cursors increase; every action starts with action.created; events up to its action deed are
	// in lifecycle order; every action not waiting for a person ends with its deed.
	type track struct {
		rank    int
		sealed  bool
		pending bool
	}
	actions := map[string]*track{}
	prevSeq := -1
	hydrated := 0
	for _, ev := range evs {
		if ev.Type == "hello" {
			continue
		}
		_, seqStr, _ := strings.Cut(ev.ID, ".")
		seq := 0
		for _, c := range seqStr {
			seq = seq*10 + int(c-'0')
		}
		if seq <= prevSeq {
			t.Fatalf("cursor %s after %d", ev.ID, prevSeq)
		}
		prevSeq = seq
		e := eventOf(t, ev)
		if e.Org == "" || e.Object != "event" || !strings.HasPrefix(e.ID, "evt_") || e.Type != ev.Type {
			t.Fatalf("envelope: %s", ev.Data)
		}
		var obj map[string]any
		_ = json.Unmarshal(e.Data.Object, &obj)
		if e.Hydrated {
			hydrated++
		}
		switch {
		case strings.HasPrefix(ev.Type, "action."):
			id, _ := obj["id"].(string)
			tr := actions[id]
			if tr == nil {
				if ev.Type != "action.created" {
					t.Fatalf("%s's first event is %s", id, ev.Type)
				}
				tr = &track{}
				actions[id] = tr
			}
			if rk := lifecycleRank[ev.Type]; !tr.sealed && rk < tr.rank {
				t.Fatalf("%s: %s after rank %d", id, ev.Type, tr.rank)
			} else if !tr.sealed {
				tr.rank = rk
			}
			if !e.Hydrated || obj["object"] != "action" || obj["agent"] == nil || obj["request"] != nil || obj["result"] != nil {
				t.Fatalf("hydrated action: %s", ev.Data)
			}
			tr.pending = obj["status"] == "pending_approval"
		case ev.Type == "deed.sealed":
			if obj["object"] != "deed_summary" || !e.Hydrated {
				t.Fatalf("deed: %s", ev.Data)
			}
			if obj["kind"] == "action" {
				tr := actions[obj["action"].(string)]
				if tr == nil || tr.sealed {
					t.Fatalf("an action deed for an action not seen or sealed twice: %s", ev.Data)
				}
				tr.sealed = true
			}
		}
	}
	sealed := 0
	for id, tr := range actions {
		if tr.sealed {
			sealed++
		} else if !tr.pending {
			t.Errorf("%s never sealed and isn't waiting for a person", id)
		}
	}
	if len(actions) != 50 || sealed < 40 || hydrated == 0 {
		t.Fatalf("%d actions, %d sealed, %d hydrated", len(actions), sealed, hydrated)
	}
	t.Logf("acme stream: %d events, %d actions, %d sealed", len(evs), len(actions), sealed)
	for _, x := range []*liveStream{other, agent} {
		for _, ev := range x.snapshot() {
			if ev.Type != "hello" {
				t.Fatalf("another stream saw %s: %s", ev.Type, ev.Data)
			}
		}
	}

	// A rolled-back transaction emits nothing; the committed one after it arrives.
	db, err := store.Open(context.Background(), config.NewSecret(env["DEEDGLASS_DATABASE_URL"]), store.Options{MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	led := ledger.New(db, nil, nil)
	org := ids.MustParse[ids.OrgKind](eventOf(t, evs[len(evs)-1]).Org)
	rolled, committed := ids.New[ids.ActionKind]().String(), ids.New[ids.ActionKind]().String()
	errRollback := errors.New("roll back")
	err = db.OrgTx(context.Background(), org, func(ctx context.Context, tx pgx.Tx) error {
		if err := led.Publish(ctx, tx, ledger.Event{Type: ledger.EventActionCreated, Org: org.String(), ID: rolled, Action: &rolled}); err != nil {
			return err
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
	if err := db.OrgTx(context.Background(), org, func(ctx context.Context, tx pgx.Tx) error {
		return led.Publish(ctx, tx, ledger.Event{Type: ledger.EventActionCreated, Org: org.String(), ID: committed, Action: &committed})
	}); err != nil {
		t.Fatal(err)
	}
	var seenCommitted bool
	for deadline := time.Now().Add(10 * time.Second); !seenCommitted && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		for _, ev := range acme.snapshot() {
			if strings.Contains(string(ev.Data), rolled) {
				t.Fatal("a rolled-back transaction's event was streamed")
			}
			seenCommitted = seenCommitted || strings.Contains(string(ev.Data), committed)
		}
	}
	if !seenCommitted {
		t.Fatal("the committed event never arrived")
	}

	// Raw HTTP: the content type, a filter refusal before the first byte, a resync for a foreign cursor.
	req, _ := http.NewRequest("GET", m.base+"/v1/events?types=nope.*", nil)
	req.Header.Set("Authorization", "Bearer "+acmeKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("bad filter: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, "GET", m.base+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+acmeKey)
	req.Header.Set("Last-Event-ID", "zzzzzz.1")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(res.Body)
	var head strings.Builder
	for !strings.Contains(head.String(), "event: resync") {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("%v: %q", err, head.String())
		}
		head.WriteString(line)
	}
	line, _ := br.ReadString('\n')
	head.WriteString(line)
	res.Body.Close()
	if res.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || !strings.HasPrefix(head.String(), "retry: 3000\n\n") ||
		!strings.Contains(head.String(), `data: {"object":"resync","reason":"other_instance"}`) {
		t.Fatalf("resync wire:\n%s", head.String())
	}
	t.Logf("wire sample (foreign cursor):\n%s", head.String())

	// serve stops cleanly with streams open: they end at shutdown, nothing is cut off.
	m.stopped = true
	m.s.sigs <- syscall.SIGTERM
	if code := m.s.wait(t); code != 0 || !strings.Contains(m.s.out.String(), "0 requests cut off") {
		t.Fatalf("stop: %d\n%s%s", code, m.s.out, m.s.err)
	}
}

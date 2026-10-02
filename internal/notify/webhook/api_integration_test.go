//go:build integration

package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

type routesAuth struct{ x *env }

func (a routesAuth) AuthenticateSession(_ context.Context, r *http.Request) (httpx.Principal, error) {
	if r.Header.Get("X-Test-User") == "" {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return httpx.Principal{Kind: httpx.KindUser, OrgID: a.x.org, UserID: a.x.user, Role: httpx.RoleAdmin, OrgName: "Acme"}, nil
}

func (a routesAuth) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	if bearer != "admin" {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: a.x.org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin,
		Scopes: []string{httpx.ScopeAdmin}}, nil
}

// TestWebhookRoutes drives the six webhook routes through httpx, and the worker's Run loop
// delivering an event without a direct call.
func TestWebhookRoutes(t *testing.T) {
	x := newEnv(t)
	r := newReceiver(t)
	s := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: routesAuth{x}, Keys: routesAuth{x}})
	s.Mount(Routes(x.s)...)
	srv := httptest.NewServer(s)
	defer srv.Close()
	do := func(method, path, body string, hdr ...string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return res.StatusCode, m
	}
	user := []string{"X-Test-User", "a"}
	key := []string{"Authorization", "Bearer admin"}

	st, m := do("POST", "/v1/webhooks", `{"url":"`+r.srv.URL+`/hook","events":["approval.decided"],"description":"ops"}`, user...)
	id, _ := m["id"].(string)
	if st != 201 && st != 200 || id == "" || !strings.HasPrefix(m["secret"].(string), "whsec_") {
		t.Fatalf("create %d %v", st, m)
	}
	for _, bad := range []string{`{"url":"http://169.254.169.254/x","events":["deed.sealed"]}`, `{"url":"` + r.srv.URL + `","events":[]}`,
		`{"url":"` + r.srv.URL + `","events":["nope.event"]}`, `{"url":"` + r.srv.URL + `","events":["deed.sealed"],"description":"` + strings.Repeat("d", 201) + `"}`} {
		if st, m := do("POST", "/v1/webhooks", bad, key...); st < 400 {
			t.Errorf("%s: %d %v", bad, st, m)
		}
	}
	if st, m := do("GET", "/v1/webhooks", "", key...); st != 200 || len(m["data"].([]any)) != 1 {
		t.Errorf("list %d %v", st, m)
	}
	if st, m := do("GET", "/v1/webhooks/"+id, "", user...); st != 200 || m["secret"] != nil {
		t.Errorf("get %d %v", st, m)
	}
	if st, _ := do("GET", "/v1/webhooks/whk_nope", "", user...); st != 404 && st != 400 && st != 422 {
		t.Errorf("unknown %d", st)
	}
	if st, m := do("POST", "/v1/webhooks/"+id+"/test", "", user...); st != 200 || m["delivered"] != true {
		t.Errorf("test %d %v", st, m)
	}

	// The worker delivers a published event by itself.
	ctx, cancel := context.WithCancel(bg)
	wk := x.s.NewWorker("t")
	wk.PollEvery = 50 * time.Millisecond
	done := make(chan struct{})
	go func() { _ = wk.Run(ctx); close(done) }()
	x.publish(t, "approval.decided", false)
	wk.Wake("")
	deadline := time.Now().Add(10 * time.Second)
	for len(r.all()) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if n := len(r.all()); n < 2 {
		t.Fatalf("the worker delivered %d (want the test and the event)", n)
	}
	if st, m := do("GET", "/v1/webhooks/"+id+"/deliveries?limit=5", "", user...); st != 200 || len(m["data"].([]any)) < 1 {
		t.Errorf("deliveries %d %v", st, m)
	}
	if st, _ := do("GET", "/v1/webhooks/"+id+"/deliveries?limit=0", "", user...); st != 400 && st != 422 {
		t.Errorf("limit 0: %d", st)
	}
	if st, _ := do("DELETE", "/v1/webhooks/"+id, "", key...); st != 200 && st != 204 {
		t.Errorf("delete %d", st)
	}
	if st, _ := do("GET", "/v1/webhooks/"+id, "", user...); st != 404 {
		t.Errorf("after delete %d", st)
	}
	if st, _ := do("GET", "/v1/webhooks", ""); st != 401 {
		t.Errorf("no credential %d", st)
	}
	// Unknown and malformed ids answer like each other on every id route.
	for _, p := range []string{"/v1/webhooks/whk_nope", "/v1/webhooks/" + ids.New[ids.WebhookKind]().String()} {
		if st, _ := do("DELETE", p, "", key...); st != 404 && st != 400 && st != 422 {
			t.Errorf("DELETE %s: %d", p, st)
		}
		if st, _ := do("POST", p+"/test", "", key...); st != 404 && st != 400 && st != 422 {
			t.Errorf("test %s: %d", p, st)
		}
		if st, _ := do("GET", p+"/deliveries", "", key...); st != 404 && st != 400 && st != 422 && st != 200 {
			t.Errorf("deliveries %s: %d", p, st)
		}
	}

	// Without the vault key, webhooks can't be created (an operator's next step, not a 500).
	nv := New(Options{DB: x.e.DB, Ledger: x.led, Logger: slog.New(slog.DiscardHandler)})
	s2 := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: routesAuth{x}, Keys: routesAuth{x}})
	s2.Mount(Routes(nv)...)
	srv2 := httptest.NewServer(s2)
	defer srv2.Close()
	req, _ := http.NewRequest("POST", srv2.URL+"/v1/webhooks", strings.NewReader(`{"url":"`+r.srv.URL+`","events":["deed.sealed"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer admin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 503 || !strings.Contains(string(b), "vault") {
		t.Errorf("no vault: %d %s", res.StatusCode, b)
	}
	// A route mounted with no service answers service_unavailable.
	s3 := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: routesAuth{x}, Keys: routesAuth{x}})
	s3.Mount(Routes(nil)...)
	srv3 := httptest.NewServer(s3)
	defer srv3.Close()
	req, _ = http.NewRequest("GET", srv3.URL+"/v1/webhooks", nil)
	req.Header.Set("Authorization", "Bearer admin")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 503 {
		t.Errorf("nil service: %v %v", res, err)
	}
}

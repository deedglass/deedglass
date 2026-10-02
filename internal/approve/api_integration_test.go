//go:build integration

package approve_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// testAuth: a session names its person with X-Test-User; the bearer "agent" is the world's agent
// key and "admin" an admin key.
type testAuth struct{ w *world }

func (a testAuth) AuthenticateSession(_ context.Context, r *http.Request) (httpx.Principal, error) {
	u, ok := a.w.people[r.Header.Get("X-Test-User")]
	if !ok {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return httpx.Principal{Kind: httpx.KindUser, OrgID: a.w.org, UserID: u, Role: httpx.RoleAdmin, OrgName: "Acme"}, nil
}

func (a testAuth) AuthenticateKey(_ context.Context, bearer string) (httpx.Principal, error) {
	switch bearer {
	case "agent":
		return a.w.agentPr(), nil
	case "admin":
		return httpx.Principal{Kind: httpx.KindAdminKey, OrgID: a.w.org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin,
			Scopes: []string{httpx.ScopeActionsRead}}, nil
	}
	return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
}

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func (c client) do(method, path, body string, hdr ...string) (int, map[string]any, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return res.StatusCode, m, res.Header
}

func errOf(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

// TestRoutes drives every S22 approval route through the real HTTP pipeline (httpx): lists and
// filters, ?wait, votes by people and refusals for keys, the three link routes, and the page.
func TestRoutes(t *testing.T) {
	w := newWorld(t)
	auth := testAuth{w}
	s := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: auth, Keys: auth})
	s.Mount(approve.Routes(w.ap)...)
	s.Mount(approve.PageRoutes()...)
	srv := httptest.NewServer(s)
	defer srv.Close()
	c := client{t, srv}
	priya := []string{"X-Test-User", "priya"}
	agent := []string{"Authorization", "Bearer agent"}
	admin := []string{"Authorization", "Bearer admin"}

	a := w.pause("approvals", "")
	b := w.pause("approvals", "")
	apr := a.Approval.ID

	// Lists: filters, limit, cursor, and refusals.
	st, m, _ := c.do("GET", "/v1/approvals?status=pending&limit=1", "", priya...)
	if st != 200 || m["has_more"] != true || len(m["data"].([]any)) != 1 {
		t.Fatalf("list %d %v", st, m)
	}
	cur, _ := m["next_cursor"].(string)
	if st, m, _ = c.do("GET", "/v1/approvals?status=pending&limit=1&cursor="+cur, "", priya...); st != 200 || len(m["data"].([]any)) != 1 {
		t.Fatalf("page 2 %d %v", st, m)
	}
	if st, m, _ = c.do("GET", "/v1/approvals?agent="+w.agent.Name+"&tool=fake.write", "", agent...); st != 200 {
		t.Fatalf("agent list %d %v", st, m)
	}
	for _, q := range []string{"status=open", "limit=0", "limit=x", "colour=red", "cursor=%21%21"} {
		if st, m, _ := c.do("GET", "/v1/approvals?"+q, "", priya...); st != 400 && st != 422 {
			t.Errorf("%s: %d %v", q, st, m)
		}
	}
	if st, _, _ := c.do("GET", "/v1/approvals", ""); st != 401 {
		t.Errorf("no credential: %d", st)
	}

	// One approval, with and without a wait.
	st, m, _ = c.do("GET", "/v1/approvals/"+apr+"?wait=0.2", "", admin...)
	dg, _ := m["context_digest"].(string)
	if st != 200 || m["status"] != "pending" || dg == "" {
		t.Fatalf("get %d %v", st, m)
	}
	for _, p := range []string{"/v1/approvals/" + apr + "?wait=99", "/v1/approvals/" + apr + "?wait=x"} {
		if st, _, _ := c.do("GET", p, "", priya...); st != 400 && st != 422 {
			t.Errorf("%s: %d", p, st)
		}
	}
	if st, m, _ := c.do("GET", "/v1/approvals/apr_nope", "", priya...); st != 404 && st != 400 && st != 422 {
		t.Errorf("bad id %d %v", st, m)
	}

	// Votes: keys never vote, a deny needs a reason, a stale digest is refused, then a person approves.
	body := `{"context_digest":"` + dg + `"}`
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/approve", body, agent...); st != 403 || errOf(m) != "approval_needs_person" {
		t.Errorf("agent key %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/approve", body, admin...); st != 403 || errOf(m) != "approval_needs_person" {
		t.Errorf("admin key %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/deny", body, priya...); st != 422 && st != 400 {
		t.Errorf("deny without reason %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/approve", `{"context_digest":"sha256:00"}`, priya...); st != 409 || errOf(m) != "context_changed" {
		t.Errorf("stale %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/approve", `{nope`, priya...); st < 400 {
		t.Errorf("bad json %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/approve", body, priya...); st != 200 || m["status"] != "approved" {
		t.Fatalf("approve %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approvals/"+apr+"/approve", body, "X-Test-User", "arjun"); st != 409 || errOf(m) != "approval_decided" {
		t.Errorf("after approve %d %v", st, m)
	}

	// The link routes: preview decides nothing, decide once, status follows, every bad link is 401.
	tok := w.token(b.Approval.ID, "priya")
	st, m, h := c.do("POST", "/v1/approval-links/preview", `{"token":"`+tok+`"}`)
	ap, _ := m["approval"].(map[string]any)
	if st != 200 || ap["id"] != b.Approval.ID || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("preview %d %v %v", st, m, h)
	}
	dg2, _ := ap["context_digest"].(string)
	if st, m, _ := c.do("POST", "/v1/approval-links/decide", `{"token":"`+tok+`","vote":"deny","context_digest":"`+dg2+`","reason":"Not today."}`); st != 200 || m["status"] != "denied" {
		t.Fatalf("decide %d %v", st, m)
	}
	if st, m, _ := c.do("POST", "/v1/approval-links/status", `{"token":"`+tok+`"}`); st != 200 {
		t.Errorf("status %d %v", st, m)
	}
	for _, bad := range []string{`{"token":"` + tok + `","vote":"approve","context_digest":"` + dg2 + `"}`, `{"token":"short"}`, `{}`} {
		if st, m, _ := c.do("POST", "/v1/approval-links/decide", bad); st != 401 && st != 400 && st != 422 {
			t.Errorf("%s: %d %v", bad, st, m)
		}
	}
	if st, _, _ := c.do("POST", "/v1/approval-links/preview", `{"token":"x","extra":1}`); st != 400 && st != 422 && st != 401 {
		t.Errorf("unknown member: %d", st)
	}
	// The per-token limit: 10 a minute.
	limited := false
	for range 12 {
		if st, _, h := c.do("POST", "/v1/approval-links/status", `{"token":"`+strings.Repeat("Z", 43)+`"}`); st == 429 && h.Get("Retry-After") != "" {
			limited = true
		}
	}
	if !limited {
		t.Error("no rate limit on one token")
	}

	// The page and its assets.
	st, _, h = c.do("GET", "/approve", "")
	if st != 200 || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("page %d %v", st, h)
	}
	for f, ct := range map[string]string{"approve.js": "text/javascript", "approve.css": "text/css"} {
		if st, _, h := c.do("GET", "/approve/assets/"+f, ""); st != 200 || !strings.HasPrefix(h.Get("Content-Type"), ct) {
			t.Errorf("%s %d %v", f, st, h)
		}
	}
	if st, _, _ := c.do("GET", "/approve/assets/approve.html", ""); st != 404 {
		t.Errorf("html as an asset: %d", st)
	}
	if st, _, _ := c.do("HEAD", "/approve", ""); st != 200 {
		t.Errorf("HEAD %d", st)
	}
}

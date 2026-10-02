package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
)

// fakeQueue records submitted jobs without running them.
type fakeQueue struct {
	mu   sync.Mutex
	jobs []string
	full bool
}

func (q *fakeQueue) Submit(kind, _, _ string, _ mailer.Job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.full {
		return false
	}
	q.jobs = append(q.jobs, kind)
	return true
}

// noDB is a Service with no database: any handler that touches the store panics, and the pipeline
// turns that into a 500. POST /v1/auth/login must never get there.
func noDB(t *testing.T, q MailQueue, now func() time.Time) (*Service, *httptest.Server) {
	t.Helper()
	base, _ := url.Parse("https://app.deedglass.example")
	s, err := New(Options{BaseURL: base, Pepper: bytes.Repeat([]byte{1}, 32), Mail: q, Now: now, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: s})
	srv.Mount(s.Routes()...)
	return s, httptest.NewServer(srv)
}

func post(t *testing.T, ts *httptest.Server, path, body string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, b
}

// The login answer doesn't depend on whether the account exists: same status, same headers (but
// the request id), same body shape and template. The handler never reads the database (it has
// none here), and the job is queued either way.
func TestLoginEnumeration(t *testing.T) {
	q := &fakeQueue{}
	_, ts := noDB(t, q, nil)
	defer ts.Close()
	known, kb := post(t, ts, "/v1/auth/login", `{"email":"Priya@Acme.com"}`)
	unknown, ub := post(t, ts, "/v1/auth/login", `{"email":"nobody@acme.io"}`) // same length
	if known.StatusCode != 202 || unknown.StatusCode != 202 {
		t.Fatalf("status %d %d: %s", known.StatusCode, unknown.StatusCode, kb)
	}
	for _, h := range []http.Header{known.Header, unknown.Header} {
		h.Del("Deedglass-Request-Id")
		h.Del("Date")
	}
	if !equalHeaders(known.Header, unknown.Header) {
		t.Fatalf("headers differ:\n%v\n%v", known.Header, unknown.Header)
	}
	norm := strings.ReplaceAll(string(ub), "nobody@acme.io", "priya@acme.com")
	if string(kb) != norm {
		t.Fatalf("bodies differ:\n%s\n%s", kb, ub)
	}
	var lr LoginRequest
	if err := json.Unmarshal(kb, &lr); err != nil || lr.Object != "login_request" || lr.Email != "priya@acme.com" || lr.ExpiresIn != 900 ||
		lr.Message != "If priya@acme.com has a Deedglass account, a sign-in link and code are on their way." {
		t.Fatalf("%v %+v", err, lr)
	}
	if len(q.jobs) != 2 || q.jobs[0] != "login" {
		t.Fatalf("jobs %v", q.jobs)
	}
	// A full queue is still 202.
	q.full = true
	if r, b := post(t, ts, "/v1/auth/login", `{"email":"sam@acme.com"}`); r.StatusCode != 202 {
		t.Fatalf("full queue: %d %s", r.StatusCode, b)
	}
}

func equalHeaders(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func TestLoginValidationAndLimits(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	_, ts := noDB(t, &fakeQueue{}, func() time.Time { return now })
	defer ts.Close()
	r, b := post(t, ts, "/v1/auth/login", `{"email":"priya@acme"}`)
	if r.StatusCode != 400 || !strings.Contains(string(b), `"priya@acme\" isn't a complete email address. Use the form name@company.com.`) ||
		!strings.Contains(string(b), `"param": "email"`) {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	// 5 per address per hour, known or not: the 6th is 429.
	for i := 0; i < 5; i++ {
		if r, b := post(t, ts, "/v1/auth/login", `{"email":"x@acme.com"}`); r.StatusCode != 202 {
			t.Fatalf("%d: %d %s", i, r.StatusCode, b)
		}
	}
	r, b = post(t, ts, "/v1/auth/login", `{"email":"X@Acme.com"}`)
	if r.StatusCode != 429 || r.Header.Get("Retry-After") != "720" ||
		!strings.Contains(string(b), "Too many sign-in requests for this email address. Try again in 12 minutes.") {
		t.Fatalf("6th for one address: %d %s %s", r.StatusCode, r.Header.Get("Retry-After"), b)
	}
	// 20 per IP per hour (that request above counted too): the 21st is 429.
	for i := 0; i < 14; i++ {
		post(t, ts, "/v1/auth/login", `{"email":"p`+string(rune('a'+i))+`@acme.com"}`)
	}
	r, b = post(t, ts, "/v1/auth/login", `{"email":"last@acme.com"}`)
	if r.StatusCode != 429 || !strings.Contains(string(b), "Too many sign-in requests from this network. Try again in 3 minutes.") ||
		r.Header.Get("Retry-After") == "" {
		t.Fatalf("21st from one IP: %d %s", r.StatusCode, b)
	}
	// Unknown fields and a malformed body are refused before anything else.
	if r, b := post(t, ts, "/v1/auth/login", `{"email":"a@b.co","password":"x"}`); r.StatusCode != 400 || !strings.Contains(string(b), "password") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
}

// Without a session cookie every session route is 401 with the challenge header and the sign-in
// URL; the preview and callback refuse malformed tokens without a database.
func TestUnauthenticated(t *testing.T) {
	s, ts := noDB(t, &fakeQueue{}, nil)
	defer ts.Close()
	for _, rt := range s.Routes() {
		if rt.Auth != httpx.Session {
			continue
		}
		path := strings.ReplaceAll(rt.Pattern, "{id}", "mem_01k6f6evxjk5zr0t0kntw9ehx5")
		req, _ := http.NewRequest(rt.Method, ts.URL+path, nil)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 401 || r.Header.Get("WWW-Authenticate") != `Bearer realm="deedglass"` ||
			!strings.Contains(string(b), "You're not signed in. Sign in at https://app.deedglass.example/login.") {
			t.Errorf("%s %s: %d %q %s", rt.Method, rt.Pattern, r.StatusCode, r.Header.Get("WWW-Authenticate"), b)
		}
	}
	for _, body := range []string{`{"token":"short"}`, `{"token":"` + strings.Repeat("A", 42) + `="}`} {
		r, b := post(t, ts, "/v1/auth/preview", body)
		if r.StatusCode != 400 || !strings.Contains(string(b), "login_link_invalid") {
			t.Fatalf("preview %s: %d %s", body, r.StatusCode, b)
		}
		r, b = post(t, ts, "/v1/auth/callback", body)
		if r.StatusCode != 400 || !strings.Contains(string(b), "login_link_invalid") {
			t.Fatalf("callback %s: %d %s", body, r.StatusCode, b)
		}
	}
	if r, b := post(t, ts, "/v1/auth/callback", `{"token":"x","code":"1"}`); r.StatusCode != 400 || !strings.Contains(string(b), "Send either token, or email and code.") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
	if r, b := post(t, ts, "/v1/auth/callback", `{"email":"bad","code":"12"}`); r.StatusCode != 400 || !strings.Contains(string(b), "login_code_invalid") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
}

// Failed previews and callbacks are limited per IP: the 31st failure in an hour is 429.
func TestFailureBudget(t *testing.T) {
	_, ts := noDB(t, &fakeQueue{}, nil)
	defer ts.Close()
	for i := 0; i < FailuresPerIP; i++ {
		if r, _ := post(t, ts, "/v1/auth/preview", `{"token":"nope"}`); r.StatusCode != 400 {
			t.Fatalf("%d: %d", i, r.StatusCode)
		}
	}
	r, b := post(t, ts, "/v1/auth/callback", `{"token":"nope"}`)
	if r.StatusCode != 429 || !strings.Contains(string(b), "Too many attempts from this network. Try again in 2 minutes.") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
}

// fixedRole authenticates every request as a member with one role.
type fixedRole struct{ role Role }

func (f fixedRole) AuthenticateSession(context.Context, *http.Request) (Principal, error) {
	return Principal{Kind: KindUser, OrgID: ids.New[ids.OrgKind](), UserID: ids.New[ids.UserKind](), Role: f.role, OrgName: "Acme"}, nil
}

// The role matrix, generated from the route table: every session route × every role. Below
// MinRole is 403 insufficient_role with the brand message; at or above it the request reaches the
// handler (which fails here, having no database, but not with 401 or 403).
func TestRoleMatrix(t *testing.T) {
	base, _ := url.Parse("https://app.deedglass.example")
	s, err := New(Options{BaseURL: base, Pepper: make([]byte, 32), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	purposes := map[string]bool{}
	for _, role := range []Role{RoleViewer, RoleAdmin, RoleOwner} {
		srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: fixedRole{role}})
		srv.Mount(s.Routes()...)
		ts := httptest.NewServer(srv)
		for _, rt := range srv.Routes() {
			if rt.Auth != httpx.Session {
				continue
			}
			if rt.MinRole == "" {
				t.Fatalf("%s has no MinRole", rt.Key())
			}
			path := strings.ReplaceAll(rt.Pattern, "{id}", "mem_01k6f6evxjk5zr0t0kntw9ehx5")
			var body io.Reader
			if rt.Method == "POST" || rt.Method == "PATCH" {
				body = strings.NewReader("{}")
			}
			req, _ := http.NewRequest(rt.Method, ts.URL+path, body)
			req.Header.Set("Content-Type", "application/json")
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			allowed := role.AtLeast(rt.MinRole)
			switch {
			case !allowed && (r.StatusCode != 403 || !strings.Contains(string(b), "insufficient_role")):
				t.Errorf("%s as %s: want 403, got %d %s", rt.Key(), role, r.StatusCode, b)
			case !allowed:
				want := "Your role in Acme is " + role.Title() + ", and " + rt.Purpose + " needs " + rt.MinRole.Title() + ". Ask an owner to change your role."
				if !strings.Contains(string(b), want) {
					t.Errorf("%s: message %s, want %q", rt.Key(), b, want)
				}
				purposes[rt.Purpose] = true
			case r.StatusCode == 401 || r.StatusCode == 403:
				t.Errorf("%s as %s: allowed, but got %d %s", rt.Key(), role, r.StatusCode, b)
			}
		}
		ts.Close()
	}
	if !purposes["inviting people"] || !purposes["changing roles"] {
		t.Fatalf("purposes seen: %v", purposes)
	}
}

func TestPages(t *testing.T) {
	_, ts := noDB(t, &fakeQueue{}, nil)
	defer ts.Close()
	get := func(path string) (*http.Response, string) {
		r, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, string(b)
	}
	for path, want := range map[string]string{
		"/login":        "Sign in to Deedglass",
		"/auth/confirm": "Checking your link…",
		"/auth/confirm?t=" + strings.Repeat("A", 43): "Checking your link…", // a query token is never read
	} {
		r, b := get(path)
		if r.StatusCode != 200 || r.Header.Get("Content-Security-Policy") != PageCSP || r.Header.Get("Referrer-Policy") != "no-referrer" ||
			r.Header.Get("Cache-Control") != "no-store" || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") ||
			!strings.Contains(b, want) || strings.Contains(b, "<script>") || strings.Contains(b, "style=") ||
			!strings.Contains(b, `<noscript>`) || !strings.Contains(b, `/auth/assets/auth.js`) {
			t.Errorf("%s: %d %v", path, r.StatusCode, r.Header)
		}
	}
	for name, ct := range assetTypes {
		r, b := get("/auth/assets/" + name)
		if r.StatusCode != 200 || r.Header.Get("Content-Type") != ct || len(b) < 1000 {
			t.Errorf("%s: %d %s", name, r.StatusCode, r.Header.Get("Content-Type"))
		}
	}
	if r, _ := get("/auth/assets/login.html"); r.StatusCode != 404 {
		t.Errorf("only the two assets are served: %d", r.StatusCode)
	}
	if r, _ := get("/app/"); r.StatusCode != 404 {
		t.Errorf("/app is the Console's since S24, not identity's: %d", r.StatusCode)
	}
	// The script reads one query parameter, next (S24: the Console return path, checked like
	// console.SafeNext), never a token from the query, and only reads the fragment it was given.
	js, _ := pagesFS.ReadFile("pages/auth.js")
	src := string(js)
	if strings.Count(src, "location.search") != 1 || !strings.Contains(src, "new URLSearchParams(window.location.search).get('next')") ||
		strings.Contains(src, "innerHTML") || !strings.Contains(src, "replaceState") {
		t.Error("auth.js may read only next from location.search, must not use innerHTML, and must clear the fragment")
	}
}

func TestPrincipalContext(t *testing.T) {
	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Fatal("empty context")
	}
	p := &Principal{Kind: KindUser, Role: RoleAdmin}
	got, ok := PrincipalFrom(WithPrincipal(context.Background(), p))
	if !ok || got != p {
		t.Fatal("WithPrincipal")
	}
	for s, ok := range map[string]bool{"viewer": true, "admin": true, "owner": true, "Owner": false, "": false} {
		if _, got := ParseRole(s); got != ok {
			t.Errorf("ParseRole(%q)", s)
		}
	}
}

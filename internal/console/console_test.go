package console

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/deedglass/deedglass/internal/httpx"
)

type fakeSessions struct{ ok bool }

func (f fakeSessions) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	if !f.ok {
		return httpx.Principal{}, errors.New("no session")
	}
	return httpx.Principal{Kind: httpx.KindUser}, nil
}

func buildFS() fstest.MapFS {
	page := func(name string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("<!doctype html><title>" + name + "</title>")}
	}
	m := fstest.MapFS{
		"index.html": page("index"), "404.html": page("404"), "kit.html": page("kit"),
		"_astro/shell.abc123.js":    {Data: []byte(strings.Repeat("console.log(1);", 100))},
		"_astro/shell.abc123.js.br": {Data: []byte("BR")},
		"_astro/shell.abc123.js.gz": {Data: []byte("GZ")},
		"_astro/style.abc123.css":   {Data: []byte("body{}")},
		"icons.svg":                 {Data: []byte("<svg/>")},
		"theme.js":                  {Data: []byte("1")},
	}
	for s := range screens {
		m[s+".html"] = page(s)
		m[s+"/detail.html"] = page(s + "-detail")
	}
	return m
}

func newConsole(t *testing.T, prod, signedIn bool) *httpx.Server {
	t.Helper()
	c, err := New(Options{Prod: prod, Sessions: fakeSessions{ok: signedIn}, FS: buildFS()})
	if err != nil {
		t.Fatal(err)
	}
	s := httpx.New(httpx.Options{Prod: prod})
	s.Mount(c.Routes()...)
	return s
}

func get(s http.Handler, method, path string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestPages(t *testing.T) {
	s := newConsole(t, false, true)
	cases := []struct{ path, title string }{
		{"/app", "index"}, {"/app/", "index"},
		{"/app/actions", "actions"}, {"/app/approvals", "approvals"}, {"/app/agents", "agents"}, {"/app/policies", "policies"},
		{"/app/connectors", "connectors"}, {"/app/insights", "insights"}, {"/app/deeds", "deeds"}, {"/app/settings", "settings"},
		{"/app/settings/members", "settings"}, {"/app/settings/seal-keys", "settings"},
		{"/app/actions/act_01k6h7mz3c9d8e7f6g5h4jq3m8", "actions-detail"},
		{"/app/approvals/apr_01k6h7mz3c9d8e7f6g5h4jq3m8", "approvals-detail"},
		{"/app/agents/agt_01k6h7mz3c9d8e7f6g5h4jq3m8", "agents-detail"},
		{"/app/policies/pol_01k6h7mz3c9d8e7f6g5h4jq3m8", "policies-detail"},
		{"/app/connectors/con_01k6h7mz3c9d8e7f6g5h4jq3m8", "connectors-detail"},
		{"/app/deeds/deed_01k6h7mz3c9d8e7f6g5h4jq3m8", "deeds-detail"},
		{"/app/_kit", "kit"},
	}
	for _, c := range cases {
		w := get(s, "GET", c.path)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "<title>"+c.title+"</title>") {
			t.Errorf("%s: %d %q, want 200 %s", c.path, w.Code, w.Body.String(), c.title)
			continue
		}
		h := w.Header()
		if got := h.Get("Content-Security-Policy"); got != CSP {
			t.Errorf("%s: CSP %q", c.path, got)
		}
		if h.Get("Cache-Control") != "no-cache" || h.Get("ETag") == "" || h.Get("Cross-Origin-Opener-Policy") != "same-origin" || h.Get("Permissions-Policy") != PermissionsPolicy {
			t.Errorf("%s: headers %v", c.path, h)
		}
		if nm := get(s, "GET", c.path, "If-None-Match", h.Get("ETag")); nm.Code != http.StatusNotModified {
			t.Errorf("%s: If-None-Match → %d", c.path, nm.Code)
		}
		if hd := get(s, "HEAD", c.path); hd.Code != 200 || hd.Body.Len() != 0 || hd.Header().Get("Content-Length") != h.Get("Content-Length") {
			t.Errorf("%s: HEAD %d len %d", c.path, hd.Code, hd.Body.Len())
		}
	}
}

func TestNotFoundNeverEchoes(t *testing.T) {
	s := newConsole(t, false, true)
	for _, p := range []string{"/app/actions/not-an-id", "/app/actions/agt_01k6h7mz3c9d8e7f6g5h4jq3m8", "/app/nope", "/app/insights/anm_01k6h7mz3c9d8e7f6g5h4jq3m8",
		"/app/settings/Bad_Section", "/app/actions/act_01k6h7mz3c9d8e7f6g5h4jq3m8/extra", "/app/%3Cscript%3E"} {
		w := get(s, "GET", p)
		if w.Code != 404 || !strings.Contains(w.Body.String(), "<title>404</title>") {
			t.Errorf("%s: %d %q, want the Console 404", p, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "not-an-id") || strings.Contains(w.Body.String(), "script") {
			t.Errorf("%s: the 404 echoes the path", p)
		}
		if w.Header().Get("Content-Security-Policy") != CSP {
			t.Errorf("%s: 404 without the CSP", p)
		}
	}
}

func TestKitIsDevOnly(t *testing.T) {
	if w := get(newConsole(t, false, true), "GET", "/app/_kit"); w.Code != 200 {
		t.Fatalf("dev: %d", w.Code)
	}
	if w := get(newConsole(t, true, true), "GET", "/app/_kit"); w.Code != 404 {
		t.Fatalf("prod: %d", w.Code)
	}
}

func TestSignedOutRedirects(t *testing.T) {
	s := newConsole(t, false, false)
	cases := map[string]string{
		"/app":                          "/login?next=%2Fapp",
		"/app/actions":                  "/login?next=%2Fapp%2Factions",
		"/app/actions?verdict=MISMATCH": "/login?next=%2Fapp%2Factions%3Fverdict%3DMISMATCH",
		"/app/_kit":                     "/login?next=%2Fapp%2F_kit",
		"/app/nope":                     "/login?next=%2Fapp%2Fnope",
	}
	for p, want := range cases {
		w := get(s, "GET", p)
		if w.Code != http.StatusFound || w.Header().Get("Location") != want {
			t.Errorf("%s: %d Location %q, want 302 %q", p, w.Code, w.Header().Get("Location"), want)
		}
		if w.Body.Len() != 0 {
			t.Errorf("%s: a signed-out answer carries a body", p)
		}
	}
	// No sessions configured at all: fail closed.
	c, _ := New(Options{FS: buildFS()})
	srv := httpx.New(httpx.Options{})
	srv.Mount(c.Routes()...)
	if w := get(srv, "GET", "/app/actions"); w.Code != http.StatusFound {
		t.Fatalf("no session authenticator: %d", w.Code)
	}
}

func TestSafeNext(t *testing.T) {
	ok := []string{"/app", "/app/actions", "/app/actions?verdict=MISMATCH&agent=agt_01k6", "/app/settings/members", "/app/deeds/deed_01k6h7mz3c9d8e7f6g5h4jq3m8"}
	bad := []string{"//evil.com", "/\\evil", "/app/../v1", "/app/..", "/app//evil.com", `/app/\evil`, "https://evil.com/app", "/apps", "/v1/me",
		"/app/%2e%2e/v1", "/app/%2F%2Fevil", "/app/x y", "/app/<script>", "/app/./x", strings.Repeat("/app", 400)}
	for _, p := range ok {
		if SafeNext(p) != p {
			t.Errorf("SafeNext(%q) dropped a good path", p)
		}
	}
	for _, p := range bad {
		if SafeNext(p) != "" {
			t.Errorf("SafeNext(%q) kept a bad path", p)
		}
	}
	// A bad path is dropped from the redirect, never echoed.
	s := newConsole(t, false, false)
	if w := get(s, "GET", "/app/x%5C%5Cevil"); w.Header().Get("Location") != "/login" {
		t.Errorf("backslash next: %q", w.Header().Get("Location"))
	}
}

func TestAssets(t *testing.T) {
	s := newConsole(t, false, false) // assets need no session
	w := get(s, "GET", "/app/_astro/shell.abc123.js", "Accept-Encoding", "gzip, br")
	if w.Code != 200 || w.Body.String() != "BR" || w.Header().Get("Content-Encoding") != "br" || w.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("br: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("hashed asset cache: %q", cc)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("type %q", ct)
	}
	if w := get(s, "GET", "/app/_astro/shell.abc123.js", "Accept-Encoding", "gzip"); w.Body.String() != "GZ" || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %q %v", w.Body.String(), w.Header())
	}
	if w := get(s, "GET", "/app/_astro/shell.abc123.js", "Accept-Encoding", "br;q=0, gzip;q=0"); w.Header().Get("Content-Encoding") != "" {
		t.Fatalf("q=0 still encoded: %v", w.Header())
	}
	if w := get(s, "GET", "/app/icons.svg?v=abc"); w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("versioned sprite: %v", w.Header())
	}
	if w := get(s, "GET", "/app/theme.js"); w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("unversioned theme.js: %v", w.Header())
	}
	for _, p := range []string{"/app/_astro/missing.js", "/app/_astro/x.exe", "/app/_astro/../index.html"} {
		if w := get(s, "GET", p); w.Code != 404 && w.Code != http.StatusFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if w := get(s, "HEAD", "/app/_astro/style.abc123.css"); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD asset: %d", w.Code)
	}
}

func TestPlaceholder(t *testing.T) {
	empty := fstest.MapFS{".keep": {Data: nil}}
	if _, err := New(Options{Prod: true, FS: empty}); !errors.Is(err, ErrNotBuilt) {
		t.Fatalf("prod with the placeholder: %v", err)
	}
	c, err := New(Options{Sessions: fakeSessions{ok: true}, FS: empty})
	if err != nil || c.Built() {
		t.Fatalf("dev placeholder: %v built=%v", err, c.Built())
	}
	s := httpx.New(httpx.Options{})
	s.Mount(c.Routes()...)
	w := get(s, "GET", "/app/actions")
	if w.Code != 200 || !strings.Contains(w.Body.String(), MsgNotBuilt) || w.Header().Get("Content-Security-Policy") != CSP {
		t.Fatalf("placeholder page: %d %q", w.Code, w.Body.String())
	}
	if !strings.Contains(string(placeholderHTML), MsgNotBuilt) {
		t.Fatal("placeholder.html drifted from MsgNotBuilt")
	}
}

// TestEmbeddedBuild checks whatever is embedded: the placeholder (CI without Node) or a real build,
// which must carry every page the router names and no inline script.
func TestEmbeddedBuild(t *testing.T) {
	c, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Built() {
		t.Skip("the Console isn't built into this binary (run make web-build)")
	}
	want := []string{"index.html", "404.html", "kit.html"}
	for s := range screens {
		want = append(want, s+".html")
		if screens[s] != "" {
			want = append(want, s+"/detail.html")
		}
	}
	for _, f := range want {
		b, err := fs.ReadFile(c.fsys, f)
		if err != nil {
			t.Errorf("the build has no %s", f)
			continue
		}
		html := string(b)
		if strings.Contains(html, " style=") || strings.Contains(html, "<style") {
			t.Errorf("%s has inline style", f)
		}
		for _, m := range strings.Split(html, "<script")[1:] {
			if !strings.Contains(strings.SplitN(m, ">", 2)[0], "src=") {
				t.Errorf("%s has an inline <script>", f)
			}
		}
	}
}

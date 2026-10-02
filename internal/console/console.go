package console

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/httpx"
)

// dist is the Console build (web/ → make web-build). Only .keep is committed: a binary built
// without Node serves the placeholder page for every Console URL, and prod refuses to start.
//
//go:embed all:dist
var dist embed.FS

//go:embed placeholder.html
var placeholderHTML []byte

// Options configure the Console's page handler.
type Options struct {
	Prod     bool                       // DEEDGLASS_ENV=prod: the kit is 404 and the placeholder refuses to start
	Sessions httpx.SessionAuthenticator // S07's sessions: no valid session → 302 /login?next=
	Log      *slog.Logger
	// FS replaces the embedded build (tests).
	FS fs.FS
}

// Console serves /app and every page and asset under it.
type Console struct {
	o     Options
	fsys  fs.FS
	built bool
	etags map[string]string // page file → "sha256 prefix"
}

// ErrNotBuilt is New's refusal in prod when the binary carries only the placeholder.
var ErrNotBuilt = errors.New("console: " + MsgNotBuilt)

// MsgNotBuilt is the placeholder's sentence (S24 §5).
const MsgNotBuilt = "The Console wasn't built into this binary. Run make web-build, then restart deedglass serve."

// New returns the Console. In prod it refuses a binary whose dist holds only the placeholder.
func New(o Options) (*Console, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	fsys := o.FS
	if fsys == nil {
		sub, err := fs.Sub(dist, "dist")
		if err != nil {
			return nil, err
		}
		fsys = sub
	}
	c := &Console{o: o, fsys: fsys, etags: map[string]string{}}
	if _, err := fs.Stat(fsys, "index.html"); err == nil {
		c.built = true
	}
	if !c.built && o.Prod {
		return nil, ErrNotBuilt
	}
	return c, nil
}

// Built reports whether the real Console is embedded (not the placeholder).
func (c *Console) Built() bool { return c.built }

// Routes are GET /app and GET /app/{path...}. Both are Public in the pipeline: the handler checks
// the session itself, so a signed-out person is redirected to /login instead of getting a 401
// envelope (S24 §3.10).
func (c *Console) Routes() []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: "/app", OperationID: "consoleIndex", Auth: httpx.Public, Extension: ADR, Handler: c.serve},
		{Method: "GET", Pattern: "/app/{path...}", OperationID: "consolePage", Auth: httpx.Public, Extension: ADR, Handler: c.serve},
	}
}

// screens are the nine destinations (brand §13.1); the id-bearing ones take /app/<screen>/<id>,
// and the id's prefix must match the screen.
var screens = map[string]string{
	"actions": "act_", "approvals": "apr_", "agents": "agt_", "policies": "pol_", "connectors": "con_",
	"deeds": "deed_", "insights": "", "settings": "",
}

var (
	idRE      = regexp.MustCompile(`^[a-z]{2,4}_[0-7][0-9a-hjkmnp-tv-z]{25}$`)
	sectionRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	// nextRE is the only shape /login?next= may carry (S24 §3.10): a path under /app, with a query.
	nextRE = regexp.MustCompile(`^/app(/[A-Za-z0-9._~%/?&=,*:+-]*)?$`)
)

// SafeNext returns p when it may be a sign-in return path, else "".
func SafeNext(p string) string {
	if len(p) > 1024 || !nextRE.MatchString(p) || strings.Contains(p, "//") || strings.Contains(p, `\`) ||
		strings.Contains(p, "/../") || strings.HasSuffix(p, "/..") || strings.Contains(p, "/./") {
		return ""
	}
	if u, err := url.PathUnescape(p); err != nil || strings.Contains(u, "..") || strings.Contains(u, "//") || strings.Contains(u, `\`) {
		return ""
	}
	return p
}

func (c *Console) serve(w http.ResponseWriter, r *http.Request) error {
	rest := strings.TrimPrefix(r.PathValue("path"), "/")
	if isAsset(rest) {
		return c.asset(w, r, rest)
	}
	if !c.signedIn(r) {
		target := "/login"
		next := r.URL.Path
		if r.URL.RawQuery != "" {
			next += "?" + r.URL.RawQuery
		}
		if n := SafeNext(next); n != "" {
			target += "?next=" + url.QueryEscape(n)
		}
		h := w.Header()
		h.Set("Location", target)
		h.Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusFound)
		return nil
	}
	if !c.built {
		return c.write(w, r, http.StatusOK, "placeholder", placeholderHTML, "no-store")
	}
	file, status := c.route(rest)
	b, err := fs.ReadFile(c.fsys, file)
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		etag := c.etag(file, b)
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
			setPageHeaders(w.Header(), "no-cache")
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
	}
	return c.write(w, r, status, file, b, "no-cache")
}

// route maps the path after /app/ to a page file. Anything unknown is the Console 404, which
// never echoes the path.
func (c *Console) route(rest string) (string, int) {
	rest = strings.TrimSuffix(rest, "/")
	notFound := func() (string, int) { return "404.html", http.StatusNotFound }
	if rest == "" {
		return "index.html", http.StatusOK
	}
	if rest == "_kit" {
		if c.o.Prod {
			return notFound()
		}
		return "kit.html", http.StatusOK
	}
	parts := strings.Split(rest, "/")
	prefix, ok := screens[parts[0]]
	if !ok {
		return notFound()
	}
	switch len(parts) {
	case 1:
		return parts[0] + ".html", http.StatusOK
	case 2:
		if parts[0] == "settings" && sectionRE.MatchString(parts[1]) {
			return "settings.html", http.StatusOK
		}
		if prefix != "" && strings.HasPrefix(parts[1], prefix) && idRE.MatchString(parts[1]) {
			return parts[0] + "/detail.html", http.StatusOK
		}
	}
	return notFound()
}

func (c *Console) signedIn(r *http.Request) bool {
	if c.o.Sessions == nil {
		return false
	}
	p, err := c.o.Sessions.AuthenticateSession(r.Context(), r)
	return err == nil && p.Kind == httpx.KindUser
}

func (c *Console) etag(file string, b []byte) string {
	if e, ok := c.etags[file]; ok {
		return e
	}
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

func (c *Console) write(w http.ResponseWriter, r *http.Request, status int, _ string, b []byte, cache string) error {
	h := w.Header()
	setPageHeaders(h, cache)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
	return nil
}

func setPageHeaders(h http.Header, cache string) {
	h.Set("Content-Security-Policy", CSP)
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Permissions-Policy", PermissionsPolicy)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", cache)
}

// assetTypes are the only files served from the build without a session.
var assetTypes = map[string]string{
	".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".svg": "image/svg+xml",
	".woff2": "font/woff2", ".woff": "font/woff", ".png": "image/png", ".webmanifest": "application/manifest+json",
}

// isAsset: /app/_astro/*, /app/icons.svg, /app/theme.js, /app/favicon.svg.
func isAsset(rest string) bool {
	switch rest {
	case "icons.svg", "theme.js", "favicon.svg":
		return true
	}
	return strings.HasPrefix(rest, "_astro/") && !strings.Contains(rest, "..")
}

func (c *Console) asset(w http.ResponseWriter, r *http.Request, rest string) error {
	ct, ok := assetTypes[path.Ext(rest)]
	clean := path.Clean(rest)
	if !ok || clean != rest || strings.HasPrefix(clean, "../") {
		return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no Console asset with that name."))
	}
	b, enc := []byte(nil), ""
	ae := r.Header.Get("Accept-Encoding")
	for _, e := range []struct{ token, ext string }{{"br", ".br"}, {"gzip", ".gz"}} {
		if acceptsEncoding(ae, e.token) {
			if x, err := fs.ReadFile(c.fsys, rest+e.ext); err == nil {
				b, enc = x, e.token
				break
			}
		}
	}
	if b == nil {
		x, err := fs.ReadFile(c.fsys, rest)
		if err != nil {
			return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no Console asset with that name."))
		}
		b = x
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Vary", "Accept-Encoding")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if enc != "" {
		h.Set("Content-Encoding", enc)
	}
	// Hashed names (_astro/*) and versioned URLs (?v=) never change; anything else revalidates.
	if strings.HasPrefix(rest, "_astro/") || r.URL.Query().Get("v") != "" {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
	return nil
}

// acceptsEncoding reports whether an Accept-Encoding header accepts token with a non-zero q.
func acceptsEncoding(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), token) {
			continue
		}
		params = strings.ReplaceAll(params, " ", "")
		if strings.HasPrefix(params, "q=0") && !strings.HasPrefix(params, "q=0.") {
			return false
		}
		if params == "q=0.0" || params == "q=0.00" || params == "q=0.000" {
			return false
		}
		return true
	}
	return false
}

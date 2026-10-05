package approve

import (
	"embed"
	"net/http"
	"strconv"

	"github.com/deedglass/deedglass/internal/httpx"
)

// The /approve page (S22 §3.7): plain HTML, one stylesheet and one script, embedded in the binary,
// sharing S07's page tokens (/auth/assets/tokens.css). It reads no cookie and sets none; the query
// string is never read (a token a scanner or a mangled link puts there is ignored), and the
// script reads only the fragment. S24 may re-skin it, but /approve and #t= stay: links already sit
// in inboxes.
//
//go:embed pages/approve.html pages/approve.css pages/approve.js
var pagesFS embed.FS

// PageCSP is the page's Content-Security-Policy: nothing inline, nothing from another origin, no framing.
const PageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// AssetsADR names the ADR that adds GET /approve/assets/{file}, a route beyond contract §9.
const AssetsADR = "ADR-048"

var assetTypes = map[string]string{
	"approve.css": "text/css; charset=utf-8",
	"approve.js":  "text/javascript; charset=utf-8",
}

// PageRoutes are GET /approve and its assets.
func PageRoutes() []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: "/approve", OperationID: "approvePage", Auth: httpx.Public, Handler: page},
		{Method: "GET", Pattern: "/approve/assets/{file}", OperationID: "approveAsset", Auth: httpx.Public, Extension: AssetsADR, PublicReason: "the approval page's own static files; the token travels in the fragment, never here", Handler: asset},
	}
}

func securityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", PageCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
}

func page(w http.ResponseWriter, r *http.Request) error {
	b, err := pagesFS.ReadFile("pages/approve.html")
	if err != nil {
		return err
	}
	h := w.Header()
	securityHeaders(h)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
	return nil
}

func asset(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("file")
	ct, ok := assetTypes[name]
	if !ok {
		return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no approval page asset with that name."))
	}
	b, err := pagesFS.ReadFile("pages/" + name)
	if err != nil {
		return err
	}
	h := w.Header()
	securityHeaders(h)
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(b)
	return err
}

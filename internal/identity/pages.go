package identity

import (
	"embed"
	"net/http"
	"strconv"

	"github.com/deedglass/deedglass/internal/httpx"
)

// The sign-in pages (S07 §5.3): plain HTML, one stylesheet and one script, embedded in the binary.
// S24 may re-skin them, but /auth/confirm and its #t= fragment stay: emails already in inboxes
// point there.
//
//go:embed pages/login.html pages/confirm.html pages/app.html pages/auth.css pages/auth.js
var pagesFS embed.FS

// PageCSP is the Content-Security-Policy of every sign-in page: no inline code, nothing from
// another origin, no framing.
const PageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// AssetsADR names the ADR that adds GET /auth/assets/{file}, a route beyond contract §9.
const AssetsADR = "ADR-028"

var assetTypes = map[string]string{
	"auth.css": "text/css; charset=utf-8",
	"auth.js":  "text/javascript; charset=utf-8",
}

func (s *Service) pageRoutes() []httpx.Route {
	return []httpx.Route{
		{Method: "GET", Pattern: "/login", OperationID: "loginPage", Auth: httpx.Public, Handler: page("pages/login.html")},
		{Method: "GET", Pattern: "/auth/confirm", OperationID: "confirmPage", Auth: httpx.Public, Handler: page("pages/confirm.html")},
		// Until the Console (S24) serves /app/, a plain page shows who is signed in.
		{Method: "GET", Pattern: "/app/", OperationID: "consolePage", Auth: httpx.Session, MinRole: RoleViewer, Handler: page("pages/app.html")},
		{Method: "GET", Pattern: "/auth/assets/{file}", OperationID: "authAsset", Auth: httpx.Public, Extension: AssetsADR, Handler: asset},
	}
}

// page serves one embedded HTML page. The query string is never read: a token that a scanner or a
// mangled link puts there is ignored, and the page itself only reads the URL fragment.
func page(name string) httpx.HandlerFunc {
	b, err := pagesFS.ReadFile(name)
	if err != nil {
		panic("identity: missing page " + name)
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", PageCSP)
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(b)
		}
		return nil
	}
}

func asset(w http.ResponseWriter, r *http.Request) error {
	name := r.PathValue("file")
	ct, ok := assetTypes[name]
	if !ok {
		return httpx.E(httpx.CodeNotFound, httpx.Msg("There's no sign-in page asset with that name."))
	}
	b, err := pagesFS.ReadFile("pages/" + name)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(b)
	return err
}

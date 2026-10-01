package httpx

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// The dev principal (S06 §3.9) is refused in prod, off loopback (directly or through a trusted
// proxy) and without exactly one valid Deedglass-Dev-Org header.
func TestDevPrincipal(t *testing.T) {
	const org = "org_01j9z8m2k4p6r8t0v2x4z6b8d0"
	cases := []struct {
		name    string
		prod    bool
		remote  string
		xff     string
		headers []string
		ok      bool
	}{
		{"dev loopback v4", false, "127.0.0.1:5000", "", []string{org}, true},
		{"dev loopback v6", false, "[::1]:5000", "", []string{org}, true},
		{"prod", true, "127.0.0.1:5000", "", []string{org}, false},
		{"remote peer", false, "203.0.113.9:5000", "", []string{org}, false},
		{"proxy forwards a remote client", false, "127.0.0.1:5000", "203.0.113.9", []string{org}, false},
		{"no header", false, "127.0.0.1:5000", "", nil, false},
		{"two headers", false, "127.0.0.1:5000", "", []string{org, org}, false},
		{"not an org id", false, "127.0.0.1:5000", "", []string{"acme"}, false},
		{"agent id", false, "127.0.0.1:5000", "", []string{"agt_01j9z9b1d3f5h7k9m1p3r5t7v9"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(Options{Prod: c.prod, Sessions: DevPrincipal{Prod: c.prod},
				TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}})
			var got Principal
			s.Mount(Route{Method: "GET", Pattern: "/v1/deeds", OperationID: "listDeeds", Auth: SessionOrKey,
				Scopes: []string{"deeds:read"}, Handler: func(w http.ResponseWriter, r *http.Request) error {
					got, _ = PrincipalFrom(r.Context())
					return WriteJSON(w, 200, map[string]string{"ok": "yes"})
				}})
			req := httptest.NewRequest("GET", "/v1/deeds", nil)
			req.RemoteAddr = c.remote
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			for _, h := range c.headers {
				req.Header.Add(DevOrgHeader, h)
			}
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if c.ok {
				if rec.Code != 200 || got.OrgID.String() != org || got.Role != RoleViewer || got.Kind != "dev" {
					t.Fatalf("status %d, principal %+v", rec.Code, got)
				}
				return
			}
			if rec.Code != 401 {
				t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body)
			}
		})
	}
}

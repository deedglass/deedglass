package identity

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestCookieFlags (S34 review row 5): the session and CSRF cookies as login, refresh and logout
// write them in prod: __Host- names, Secure, Path=/, no Domain; the session cookie HttpOnly and
// SameSite=Lax (so a link from an email or Slack opens the Console signed in; CSRF stays closed by
// the double-submit token and Sec-Fetch-Site), the CSRF cookie readable by the Console and
// SameSite=Strict. The logout response clears both with the same attributes.
func TestCookieFlags(t *testing.T) {
	base, _ := url.Parse("https://app.deedglass.com")
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s := &Service{cookies: cookieNamesFor(true, base), now: func() time.Time { return now }}
	for name, write := range map[string]func(http.ResponseWriter){
		"login and refresh": func(w http.ResponseWriter) {
			s.setCookies(w, NewSession{Token: "tok", CSRF: "csrf", ExpiresAt: now.Add(12 * time.Hour)})
		},
		"logout": func(w http.ResponseWriter) { s.clearCookies(w) },
	} {
		rec := httptest.NewRecorder()
		write(rec)
		cs := rec.Result().Cookies()
		if len(cs) != 2 {
			t.Fatalf("%s: %d cookies", name, len(cs))
		}
		for _, c := range cs {
			if c.Path != "/" || c.Domain != "" || !c.Secure || len(c.Name) < 7 || c.Name[:7] != "__Host-" {
				t.Errorf("%s: %s path %q domain %q secure %v", name, c.Name, c.Path, c.Domain, c.Secure)
			}
			switch c.Name {
			case SessionCookie:
				if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
					t.Errorf("%s: session HttpOnly %v SameSite %v", name, c.HttpOnly, c.SameSite)
				}
			case CSRFCookie:
				if c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
					t.Errorf("%s: csrf HttpOnly %v SameSite %v", name, c.HttpOnly, c.SameSite)
				}
			default:
				t.Errorf("%s: unexpected cookie %s", name, c.Name)
			}
			if name == "logout" && c.MaxAge >= 0 {
				t.Errorf("logout leaves %s alive (Max-Age %d)", c.Name, c.MaxAge)
			}
		}
	}
}

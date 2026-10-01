package identity

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		"Priya@Acme.com":           "priya@acme.com",
		"  sam@acme.co.in ":        "sam@acme.co.in",
		"a.b+tag@sub.example.test": "a.b+tag@sub.example.test",
		"x@y.io":                   "x@y.io",
		"o'neil@example.com":       "o'neil@example.com",
	} {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "priya", "priya@acme", "@acme.com", "priya@", "a@@b.com", "a b@c.com", "a@b..com",
		"a@-b.com", "a@b-.com", ".a@b.com", "a.@b.com", "a..b@c.com", "<a@b.com>", "a\x00@b.com",
		strings.Repeat("a", 65) + "@b.com", "a@" + strings.Repeat("b", 250) + ".com"} {
		if got, err := NormalizeEmail(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	_, err := NormalizeEmail("priya@acme")
	var ee *EmailError
	if !errors.As(err, &ee) || err.Error() != `"priya@acme" isn't a complete email address. Use the form name@company.com.` {
		t.Fatalf("message: %v", err)
	}
	_, err = NormalizeEmail(strings.Repeat("x", 300))
	if len(err.Error()) > 160 {
		t.Fatalf("a long value is clipped in the message: %d", len(err.Error()))
	}
}

func FuzzEmailNormalize(f *testing.F) {
	for _, s := range []string{"Priya@Acme.com", "a@b", "", " x@y.z ", "\xff@x.io", "A@B.C"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, err := NormalizeEmail(s)
		if err != nil {
			return
		}
		again, err := NormalizeEmail(got)
		if err != nil || again != got {
			t.Fatalf("not idempotent: %q → %q → %q (%v)", s, got, again, err)
		}
		if got != strings.ToLower(got) || len(got) > MaxEmail || strings.Count(got, "@") != 1 {
			t.Fatalf("bad result %q", got)
		}
	})
}

func TestDescribeUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36":                   "Chrome on macOS",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15":                   "Safari on macOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:143.0) Gecko/20100101 Firefox/143.0":                                                        "Firefox on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36 Edg/141.0":                   "Edge on Windows",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Mobile/15E148 Safari/604.1": "Safari on iOS",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/141.0 Mobile/15E148 Safari/604.1":  "Chrome on iOS",
		"Mozilla/5.0 (Linux; Android 16; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Mobile Safari/537.36":                       "Chrome on Android",
		"Mozilla/5.0 (Linux; Android 16) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/28.0 Chrome/130.0 Mobile Safari/537.36":            "a browser on Android",
		"Mozilla/5.0 (X11; Linux x86_64; rv:143.0) Gecko/20100101 Firefox/143.0":                                                                  "Firefox on Linux",
		"curl/8.9.1":                           "",
		"":                                     "",
		"Mozilla/5.0 (PlayStation) Chrome/1.0": "Chrome",
	} {
		if got := DescribeUserAgent(ua); got != want {
			t.Errorf("%q → %q, want %q", ua, got, want)
		}
	}
}

func TestLandingOrg(t *testing.T) {
	at := func(m int) *time.Time { v := time.Date(2026, 10, 3, 9, m, 0, 0, time.UTC); return &v }
	a, b, c := ids.New[ids.OrgKind](), ids.New[ids.OrgKind](), ids.New[ids.OrgKind]()
	m := func(org ids.OrgID, used, joined *time.Time, status, ostatus string) store.PreAuthMembership {
		return store.PreAuthMembership{OrgID: org, Status: status, OrgStatus: ostatus, LastUsedAt: used, JoinedAt: joined}
	}
	for name, tc := range map[string]struct {
		ms   []store.PreAuthMembership
		want ids.OrgID
		ok   bool
	}{
		"latest use wins":       {[]store.PreAuthMembership{m(a, at(1), at(0), "active", "active"), m(b, at(5), at(9), "active", "active")}, b, true},
		"used beats never used": {[]store.PreAuthMembership{m(a, nil, at(0), "active", "active"), m(b, at(1), at(9), "active", "active")}, b, true},
		"tie: oldest join":      {[]store.PreAuthMembership{m(a, at(5), at(3), "active", "active"), m(b, at(5), at(1), "active", "active")}, b, true},
		"never used: oldest":    {[]store.PreAuthMembership{m(c, nil, at(7), "active", "active"), m(a, nil, at(2), "active", "active")}, a, true},
		"invited skipped":       {[]store.PreAuthMembership{m(a, at(9), at(0), "invited", "active"), m(b, nil, at(1), "active", "active")}, b, true},
		"disabled org skipped":  {[]store.PreAuthMembership{m(a, at(9), at(0), "active", "disabled")}, ids.OrgID{}, false},
		"none":                  {nil, ids.OrgID{}, false},
	} {
		got, ok := landingOrg(tc.ms)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: %v %v", name, got, ok)
		}
	}
}

func TestSlugsNamesZones(t *testing.T) {
	for in, want := range map[string]string{"Acme": "acme", "Acme Robotics, Inc.": "acme-robotics-inc", "  --x--  ": "x", "日本": "",
		strings.Repeat("ab", 30): strings.Repeat("ab", 20)} {
		if got := SlugFor(in); got != want {
			t.Errorf("SlugFor(%q) = %q, want %q", in, got, want)
		}
	}
	for s, ok := range map[string]bool{"acme": true, "a": true, "a-b": true, "-a": false, "a-": false, "A": false, strings.Repeat("a", 41): false} {
		if ValidSlug(s) != ok {
			t.Errorf("ValidSlug(%q) != %v", s, ok)
		}
	}
	if n, err := CheckOrgName("  Acme  "); err != nil || n != "Acme" {
		t.Fatal(n, err)
	}
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("x", 101)} {
		if _, err := CheckOrgName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for tz, ok := range map[string]bool{"UTC": true, "Asia/Kolkata": true, "America/New_York": true, "Local": false, "": false, "Mars/Base": false, "../etc/passwd": false} {
		if ValidTimezone(tz) != ok {
			t.Errorf("ValidTimezone(%q) != %v", tz, ok)
		}
	}
}

func TestCookieNames(t *testing.T) {
	u := func(s string) *url.URL { v, _ := url.Parse(s); return v }
	for name, tc := range map[string]struct {
		prod   bool
		base   string
		secure bool
	}{
		"prod https":          {true, "https://app.deedglass.com", true},
		"dev http localhost":  {false, "http://localhost:8080", false},
		"dev http 127.0.0.1":  {false, "http://127.0.0.1:8080", false},
		"dev https localhost": {false, "https://localhost:8443", true},
		"dev http elsewhere":  {false, "http://dev.internal:8080", true},
	} {
		c := cookieNamesFor(tc.prod, u(tc.base))
		if c.secure != tc.secure || (c.secure && (c.session != SessionCookie || c.csrf != CSRFCookie)) ||
			(!c.secure && (c.session != DevSessionCookie || c.csrf != DevCSRFCookie)) {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestNewChecksOptions(t *testing.T) {
	base, _ := url.Parse("https://app.deedglass.com")
	for name, o := range map[string]Options{
		"no base":    {Pepper: make([]byte, 32)},
		"short":      {BaseURL: base, Pepper: make([]byte, 31)},
		"bad signup": {BaseURL: base, Pepper: make([]byte, 32), Signup: "open"},
	} {
		if _, err := New(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s, err := New(Options{BaseURL: base, Pepper: make([]byte, 32)})
	if err != nil || s.signup != "invite" || s.LoginURL() != "https://app.deedglass.com/login" ||
		s.ConfirmLink("abc") != "https://app.deedglass.com/auth/confirm#t=abc" || s.InsecureDevCookies() {
		t.Fatalf("%v %+v", err, s)
	}
	if s.emailKey("a@b.co") == s.emailKey("a@b.com") || strings.Contains(s.emailKey("a@b.co"), "@") {
		t.Fatal("email keys")
	}
}

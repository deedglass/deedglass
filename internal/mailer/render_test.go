package mailer

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

const fixtureLink = "http://127.0.0.1:8080/auth/confirm#t=OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc"

func fixtureLogin() LoginData {
	return LoginData{
		OrgName:       "Acme",
		Email:         "priya@acme.com",
		Link:          fixtureLink,
		Code:          "482913",
		RequestedFrom: "Chrome on macOS",
		RequestedAt:   time.Date(2026, 10, 3, 9, 14, 7, 0, time.UTC),
		ExpiresIn:     15 * time.Minute,
	}
}

func fixtureInvite() InviteData {
	return InviteData{
		InviterName:  "Priya Raman",
		InviterEmail: "priya@acme.com",
		OrgName:      "Acme",
		Role:         "viewer",
		Email:        "sam@acme.com",
		Link:         fixtureLink,
		ExpiresAt:    time.Date(2026, 10, 10, 9, 20, 11, 0, time.UTC),
		Now:          time.Date(2026, 10, 3, 9, 20, 11, 0, time.UTC),
	}
}

// golden compares got with testdata/<name>.golden, rewriting it under -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/mailer -run TestGoldens -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file (run with -update after checking the change)\ngot:\n%s", name, got)
	}
}

func TestGoldens(t *testing.T) {
	login, err := RenderLogin(fixtureLogin())
	if err != nil {
		t.Fatal(err)
	}
	invite, err := RenderInvite(fixtureInvite())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got string }{
		{"login.html", login.HTML},
		{"login.txt", login.Text},
		{"invite.html", invite.HTML},
		{"invite.txt", invite.Text},
	} {
		t.Run(c.name, func(t *testing.T) { golden(t, c.name, c.got) })
	}
}

func TestCopy(t *testing.T) {
	login, err := RenderLogin(fixtureLogin())
	if err != nil {
		t.Fatal(err)
	}
	invite, err := RenderInvite(fixtureInvite())
	if err != nil {
		t.Fatal(err)
	}
	footer := "Deedglass · See what your agents actually did. · "
	for _, c := range []struct {
		name, got string
		want      []string
	}{
		{"login subject", login.Subject, []string{"Sign in to Deedglass"}},
		{"login preheader", login.Preheader, []string{"Your sign-in link for Acme. It works once and expires in 15 minutes."}},
		{"login html", login.HTML, []string{
			">deedglass</p>",
			"Sign in to Acme</h1>",
			"Use the button to sign in as priya@acme.com. The link works once and expires in 15 minutes.",
			">Sign in to Deedglass</a>",
			"Or enter this code on the sign-in page:",
			">482 913</p>",
			"Requested from Chrome on macOS at 09:14 UTC.",
			"If you didn't ask to sign in, you can ignore this email. Nothing happens until someone uses the link or the code.",
			footer + `<a href="https://deedglass.com"`,
			`href="` + fixtureLink + `"`,
			login.Preheader + "</div>",
		}},
		{"login text", login.Text, []string{
			"deedglass\n\nSign in to Acme\n",
			"Or enter this code on the sign-in page: 482 913\n",
			"\nRequested from Chrome on macOS at 09:14 UTC.\n",
			"\n" + fixtureLink + "\n",
			"If you didn't ask to sign in, you can ignore this email. Nothing happens until someone uses the link or the code.",
			"─────────────\n" + footer + "deedglass.com\n",
		}},
		{"invite subject", invite.Subject, []string{"Priya Raman invited you to Acme on Deedglass"}},
		{"invite preheader", invite.Preheader, []string{"Join Acme as a Viewer. The invite expires in 7 days."}},
		{"invite html", invite.HTML, []string{
			"Priya Raman invited you to join Acme as a Viewer.",
			"Deedglass is where Acme sees what its AI agents actually did: every action, the policy that allowed it, and a signed deed.",
			">Accept invite</a>",
			"This invite works once and expires on 10 October 2026.",
			"If you weren't expecting this, you can ignore it.",
			footer,
		}},
		{"invite text", invite.Text, []string{
			"Priya Raman invited you to join Acme as a Viewer.\n",
			"Accept invite:\n" + fixtureLink + "\n",
			"This invite works once and expires on 10 October 2026.\n",
			"If you weren't expecting this, you can ignore it.\n",
		}},
	} {
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("%s: missing %q", c.name, w)
			}
		}
	}
	if c := strings.Count(login.HTML, fixtureLink); c != 1 {
		t.Errorf("login html has the link %d times, want 1 (the button)", c)
	}
}

// TestEmailSafety checks the brand §13.5 rules on every rendered body: inline styles only, no
// remote resources, no images, no scripts, no exclamation marks and no emoji.
func TestEmailSafety(t *testing.T) {
	login, _ := RenderLogin(fixtureLogin())
	invite, _ := RenderInvite(fixtureInvite())
	for name, r := range map[string]Rendered{"login": login, "invite": invite} {
		for _, bad := range []string{"<img", "<script", "<style", "<link", "src=", "url(", "@import", "background-image"} {
			if strings.Contains(strings.ToLower(r.HTML), bad) {
				t.Errorf("%s html contains %q", name, bad)
			}
		}
		for _, need := range []string{
			`<meta name="color-scheme" content="light">`,
			`<meta name="supported-color-schemes" content="light">`,
			`max-width:600px`, `background-color:#F7F7F4`, `border:1px solid #E4E5EA;border-radius:14px;padding:40px`,
			`font-size:20px;line-height:24px;font-weight:600;letter-spacing:-0.045em;color:#0D0F14`,
			`bgcolor="#2F5BEA"`, `border-radius:10px`, `height:44px`, `color:#FFFFFF`,
			`font-family:Inter,-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif`,
			`font-size:15px;line-height:24px;color:#0D0F14`, `font-size:13px;line-height:20px;color:#6B7080`,
		} {
			if !strings.Contains(r.HTML, need) {
				t.Errorf("%s html is missing %q", name, need)
			}
		}
		html := strings.Replace(r.HTML, "<!DOCTYPE", "", 1)
		for part, s := range map[string]string{"subject": r.Subject, "preheader": r.Preheader, "html": html, "text": r.Text} {
			if strings.Contains(s, "!") {
				t.Errorf("%s %s has an exclamation mark", name, part)
			}
			for _, c := range s {
				if c > 0x7e && c != '·' && c != '─' && c != '…' {
					t.Errorf("%s %s has an unexpected character %q", name, part, c)
				}
			}
		}
	}
	if !strings.Contains(login.HTML, `font-family:'Geist Mono',ui-monospace,Menlo,monospace;font-size:22px;line-height:30px;letter-spacing:0.12em`) {
		t.Error("login html: the code is not in the mono style")
	}
}

func TestEscaping(t *testing.T) {
	d := fixtureLogin()
	d.OrgName = `<script>alert(1)</script>`
	d.Email = `"x"@acme.com`
	d.RequestedFrom = `<b>Firefox</b>`
	r, err := RenderLogin(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<script>") || strings.Contains(r.HTML, "<b>") {
		t.Fatalf("html is not escaped:\n%s", r.HTML)
	}
	for _, w := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "&#34;x&#34;@acme.com", "&lt;b&gt;Firefox&lt;/b&gt;"} {
		if !strings.Contains(r.HTML, w) {
			t.Errorf("html is missing %q", w)
		}
	}

	i := fixtureInvite()
	i.InviterName = `Eve <eve@x.com>`
	i.OrgName = `<script>x</script>`
	ri, err := RenderInvite(i)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ri.HTML, "<script>") || strings.Contains(ri.HTML, "<eve@") {
		t.Fatalf("invite html is not escaped:\n%s", ri.HTML)
	}

	// Control characters never reach a body or the subject.
	i = fixtureInvite()
	i.InviterName = "Priya\r\nBcc: x@y.com"
	ri, err = RenderInvite(i)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(ri.Subject, "\r\n") || strings.Contains(ri.Text, "Priya\n") {
		t.Fatalf("control characters kept: %q", ri.Subject)
	}
}

func TestInviteWithoutName(t *testing.T) {
	d := fixtureInvite()
	d.InviterName = ""
	r, err := RenderInvite(d)
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "priya@acme.com invited you to Acme on Deedglass" {
		t.Errorf("subject %q", r.Subject)
	}
	for _, body := range []string{r.HTML, r.Text} {
		if !strings.Contains(body, "priya@acme.com invited you to join Acme as a Viewer.") {
			t.Errorf("body does not use the inviter's email:\n%s", body)
		}
	}
}

func TestLoginWithoutRequestedFrom(t *testing.T) {
	d := fixtureLogin()
	d.RequestedFrom = ""
	r, err := RenderLogin(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{r.HTML, r.Text} {
		if !strings.Contains(body, "Requested at 09:14 UTC.") || strings.Contains(body, "Requested from") {
			t.Errorf("requested line:\n%s", body)
		}
	}
	d.RequestedAt = time.Time{}
	r, err = RenderLogin(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "Requested") || strings.Contains(r.Text, "Requested") {
		t.Error("a zero RequestedAt must omit the line")
	}
}

func TestRenderVariants(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*InviteData)
		want string
	}{
		{"admin takes an", func(d *InviteData) { d.Role = "admin" }, "Join Acme as an Admin. The invite expires in 7 days."},
		{"one day", func(d *InviteData) { d.ExpiresAt = d.Now.Add(3 * time.Hour) }, "Join Acme as a Viewer. The invite expires in 1 day."},
		{"partial days round up", func(d *InviteData) { d.ExpiresAt = d.Now.Add(36 * time.Hour) }, "Join Acme as a Viewer. The invite expires in 2 days."},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := fixtureInvite()
			c.edit(&d)
			r, err := RenderInvite(d)
			if err != nil {
				t.Fatal(err)
			}
			if r.Preheader != c.want {
				t.Errorf("preheader %q, want %q", r.Preheader, c.want)
			}
		})
	}
	// A zero Now uses the clock.
	d := fixtureInvite()
	d.Now = time.Time{}
	d.ExpiresAt = time.Now().Add(72 * time.Hour)
	if r, err := RenderInvite(d); err != nil || !strings.Contains(r.Preheader, "3 days") {
		t.Errorf("zero Now: %v %q", err, r.Preheader)
	}
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{time.Minute, "1 minute"}, {15 * time.Minute, "15 minutes"}, {90 * time.Minute, "90 minutes"},
		{time.Hour, "1 hour"}, {2 * time.Hour, "2 hours"}, {10*time.Minute + 40*time.Second, "11 minutes"},
	} {
		if got := humanDuration(c.in); got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRenderErrors(t *testing.T) {
	login := []struct {
		name string
		edit func(*LoginData)
	}{
		{"no org", func(d *LoginData) { d.OrgName = " " }},
		{"no email", func(d *LoginData) { d.Email = "" }},
		{"short code", func(d *LoginData) { d.Code = "48291" }},
		{"letters in code", func(d *LoginData) { d.Code = "48291a" }},
		{"no expiry", func(d *LoginData) { d.ExpiresIn = 0 }},
		{"javascript link", func(d *LoginData) { d.Link = "javascript:alert(1)" }},
		{"relative link", func(d *LoginData) { d.Link = "/auth/confirm" }},
		{"link with newline", func(d *LoginData) { d.Link = fixtureLink + "\n" }},
		{"bad link", func(d *LoginData) { d.Link = "http://[::1" }},
	}
	for _, c := range login {
		t.Run("login/"+c.name, func(t *testing.T) {
			d := fixtureLogin()
			c.edit(&d)
			if _, err := RenderLogin(d); err == nil {
				t.Fatal("accepted")
			}
			if _, err := LoginMessage(d); err == nil {
				t.Fatal("LoginMessage accepted")
			}
		})
	}
	invite := []struct {
		name string
		edit func(*InviteData)
	}{
		{"no inviter", func(d *InviteData) { d.InviterName, d.InviterEmail = "", "" }},
		{"no org", func(d *InviteData) { d.OrgName = "" }},
		{"no role", func(d *InviteData) { d.Role = "" }},
		{"no email", func(d *InviteData) { d.Email = "" }},
		{"expired", func(d *InviteData) { d.ExpiresAt = d.Now }},
		{"bad link", func(d *InviteData) { d.Link = "ftp://x" }},
	}
	for _, c := range invite {
		t.Run("invite/"+c.name, func(t *testing.T) {
			d := fixtureInvite()
			c.edit(&d)
			if _, err := RenderInvite(d); err == nil {
				t.Fatal("accepted")
			}
			if _, err := InviteMessage(d); err == nil {
				t.Fatal("InviteMessage accepted")
			}
		})
	}
}

func TestMessages(t *testing.T) {
	lm, err := LoginMessage(fixtureLogin())
	if err != nil {
		t.Fatal(err)
	}
	if lm.Kind != "login" || lm.To != "priya@acme.com" || lm.Subject != "Sign in to Deedglass" || lm.HTML == "" || lm.Text == "" {
		t.Errorf("login message %+v", lm)
	}
	if err := lm.validate(); err != nil {
		t.Error(err)
	}
	im, err := InviteMessage(fixtureInvite())
	if err != nil {
		t.Fatal(err)
	}
	if im.Kind != "invite" || im.To != "sam@acme.com" || im.Subject != "Priya Raman invited you to Acme on Deedglass" {
		t.Errorf("invite message %+v", im)
	}
}

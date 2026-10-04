package mailer

// The digest goldens live in testdata/digest*.golden. After checking a copy change, regenerate them:
//
//	go test ./internal/mailer -run TestDigest -update

import (
	"strings"
	"testing"
	"time"
)

const digestLink = "https://app.deedglass.com/app/insights"

// fixtureDigest is S28 §3.9's example week for Acme (week 41 of 2026).
func fixtureDigest() DigestData {
	return DigestData{
		OrgName: "Acme", Email: "priya@acme.com", Role: "owner",
		Year: 2026, Week: 41, Monday: time.Date(2026, 10, 5, 0, 0, 0, 0, time.FixedZone("IST", 5*3600+1800)),
		Actions: 5612, Verified: 1080, Witnessed: 1215, Denied: 311, Mismatches: 9,
		SuggestionsOpen:  2,
		SuggestionTitles: []string{"Allow updates to public.orders without approval", "Cap refunds at $200 for support-agent"},
		AnomaliesOpen:    1,
		AnomalyTitle:     "3 MISMATCH verdicts in 1 hour for ops-agent",
		ShadowLine:       "acme-sim v3 has run in shadow for 2 days and agrees on 1,180 of 1,204 actions.",
		TopDenyRules:     []DigestRule{{Rule: "refund-limit", Count: 15}, {Rule: "@default", Count: 2}},
		Link:             digestLink,
	}
}

// fixtureQuietDigest is an admin's week with nothing to review, spanning two months.
func fixtureQuietDigest() DigestData {
	return DigestData{
		OrgName: "Acme", Email: "sam@acme.com", Role: "admin",
		Year: 2026, Week: 40, Monday: time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		Actions: 1, AnomaliesOpened: 2, AnomaliesResolved: 2,
		Link: digestLink,
	}
}

const digestWantText = `Deedglass · Acme · week 41 (5 to 11 Oct 2026)

5,612 agent actions · 88.9% verified · 311 denied · 9 mismatches

To review
  2 suggestions: "Allow updates to public.orders without approval" and 1 more
  1 open anomaly: 3 MISMATCH verdicts in 1 hour for ops-agent
Shadow
  acme-sim v3 has run in shadow for 2 days and agrees on 1,180 of 1,204 actions.
Denied most by
  refund-limit 15 · @default 2

Open Insights: https://app.deedglass.com/app/insights

You get this weekly summary because you're an owner of Acme on Deedglass.
Change who receives it in Console › Insights › Settings.
`

func TestDigestGoldens(t *testing.T) {
	r, err := RenderDigest(fixtureDigest())
	if err != nil {
		t.Fatal(err)
	}
	if r.Text != digestWantText {
		t.Errorf("text differs from S28 §3.9:\n%s", r.Text)
	}
	q, err := RenderDigest(fixtureQuietDigest())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got string }{
		{"digest.html", r.HTML}, {"digest.txt", r.Text},
		{"digest_quiet.html", q.HTML}, {"digest_quiet.txt", q.Text},
	} {
		t.Run(c.name, func(t *testing.T) { golden(t, c.name, c.got) })
	}
}

func TestDigestCopy(t *testing.T) {
	r, err := RenderDigest(fixtureDigest())
	if err != nil {
		t.Fatal(err)
	}
	if want := "Acme · week 41 · 5,612 agent actions, 2 suggestions to review"; r.Subject != want {
		t.Errorf("subject %q, want %q", r.Subject, want)
	}
	if want := "5,612 agent actions · 88.9% verified · 311 denied · 9 mismatches"; r.Preheader != want {
		t.Errorf("preheader %q, want %q", r.Preheader, want)
	}
	for _, want := range []string{
		"Acme &middot; week 41</h1>", ">5 to 11 Oct 2026</p>",
		"<span style=\"white-space:nowrap;\">9 mismatches</span></td>",
		">To review</h2>", "2 suggestions: &#34;Allow updates to public.orders without approval&#34; and 1 more",
		"1 open anomaly: 3 MISMATCH verdicts in 1 hour for ops-agent", ">Shadow</h2>", ">Denied most by</h2>",
		">refund-limit</span> 15 &middot; ", ">@default</span> 2</p>",
		`href="` + digestLink + `"`, ">Open Insights</a>",
		"You get this weekly summary because you&rsquo;re an owner of Acme on Deedglass. Change who receives it in Console &rsaquo; Insights &rsaquo; Settings.",
		r.Preheader + "</div>",
	} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	// One action (the button) plus the footer's site link; no images, no tracking, far under Gmail's 102 KB clip.
	if n := strings.Count(r.HTML, "<a href="); n != 2 || strings.Count(r.HTML, digestLink) != 1 || len(r.HTML) > 20_000 {
		t.Errorf("html: %d links, the Insights link %d times, %d bytes", n, strings.Count(r.HTML, digestLink), len(r.HTML))
	}

	q, err := RenderDigest(fixtureQuietDigest())
	if err != nil {
		t.Fatal(err)
	}
	if q.Subject != "Acme · week 40 · 1 agent action" {
		t.Errorf("quiet subject %q", q.Subject)
	}
	for _, want := range []string{
		"Deedglass · Acme · week 40 (28 Sep to 4 Oct 2026)\n\n1 agent action · no verdicts · 0 denied · 0 mismatches\n\n",
		"\n  Nothing to review this week.\nAnomalies this week\n  2 opened · 2 resolved\n\nOpen Insights: ",
		"because you're an admin of Acme on Deedglass.\n",
	} {
		if !strings.Contains(q.Text, want) {
			t.Errorf("quiet text lacks %q:\n%s", want, q.Text)
		}
	}
	for _, absent := range []string{"To review", "Shadow", "Denied most by"} {
		if strings.Contains(q.Text, absent) || strings.Contains(q.HTML, ">"+absent+"<") {
			t.Errorf("quiet digest shows %q", absent)
		}
	}
	if !strings.Contains(q.HTML, ">Nothing to review this week.</p>") || !strings.Contains(q.HTML, "you&rsquo;re an admin of Acme") {
		t.Errorf("quiet html:\n%s", q.HTML)
	}

	for _, s := range []string{r.Subject, r.Preheader, r.HTML, r.Text, q.Subject, q.HTML, q.Text} {
		if strings.ContainsAny(s, "—–") || strings.Contains(strings.NewReplacer("<!DOCTYPE", "", "!important", "").Replace(s), "!") {
			t.Error("an em or en dash, or an exclamation mark, in the digest")
		}
	}
}

func TestDigestLines(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*DigestData)
		want []string // in the text
		not  []string
	}{
		{"one suggestion", func(d *DigestData) { d.SuggestionsOpen, d.SuggestionTitles = 1, d.SuggestionTitles[:1] },
			[]string{"  1 suggestion: \"Allow updates to public.orders without approval\"\n"}, []string{"and 1 more"}},
		{"suggestions without titles", func(d *DigestData) { d.SuggestionsOpen, d.SuggestionTitles = 3, []string{" ", ""} },
			[]string{"To review\n  3 suggestions\n  1 open anomaly"}, nil},
		{"only anomalies", func(d *DigestData) { d.SuggestionsOpen = 0 },
			[]string{"To review\n  1 open anomaly: 3 MISMATCH"}, []string{"suggestion"}},
		{"several anomalies", func(d *DigestData) { d.AnomaliesOpen = 3 },
			[]string{"  3 open anomalies, newest: 3 MISMATCH verdicts in 1 hour for ops-agent\n"}, nil},
		{"anomaly without title", func(d *DigestData) { d.SuggestionsOpen, d.AnomalyTitle = 0, "" },
			[]string{"To review\n  1 open anomaly\nShadow"}, nil},
		{"nothing to review", func(d *DigestData) { d.SuggestionsOpen, d.AnomaliesOpen = 0, 0 },
			[]string{"9 mismatches\n\n  Nothing to review this week.\nShadow\n"}, []string{"To review"}},
		{"no shadow, no denials", func(d *DigestData) { d.ShadowLine, d.TopDenyRules = "", nil },
			[]string{"for ops-agent\n\nOpen Insights: "}, []string{"Shadow", "Denied most by"}},
		{"three rules at most", func(d *DigestData) {
			d.TopDenyRules = []DigestRule{{"a", 1234567}, {"", 9}, {"b", 2}, {"c", 1}, {"d", 1}}
		}, []string{"Denied most by\n  a 1,234,567 · b 2 · c 1\n"}, []string{" d 1"}},
		{"no verdicts", func(d *DigestData) { d.Witnessed, d.Verified = 0, 0 },
			[]string{"5,612 agent actions · no verdicts · 311 denied · 9 mismatches\n"}, []string{"%"}},
		{"all verified, one mismatch", func(d *DigestData) { d.Verified, d.Witnessed, d.Mismatches = 7, 7, 1 },
			[]string{"· 100.0% verified · 311 denied · 1 mismatch\n"}, nil},
		{"rounding half up", func(d *DigestData) { d.Verified, d.Witnessed = 1, 8 }, // 12.5%
			[]string{"· 12.5% verified ·"}, nil},
		{"anomalies opened and resolved", func(d *DigestData) { d.AnomaliesOpened, d.AnomaliesResolved = 1, 0 },
			[]string{"for ops-agent\nAnomalies this week\n  1 opened · 0 resolved\nShadow\n"}, nil},
		{"year boundary", func(d *DigestData) {
			d.Year, d.Week, d.Monday = 2026, 53, time.Date(2026, 12, 28, 23, 30, 0, 0, time.UTC)
		}, []string{"week 53 (28 Dec 2026 to 3 Jan 2027)\n"}, nil},
		{"role is case-insensitive", func(d *DigestData) { d.Role = " Admin " },
			[]string{"because you're an admin of Acme on Deedglass.\n"}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := fixtureDigest()
			c.edit(&d)
			r, err := RenderDigest(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !strings.Contains(r.Text, w) {
					t.Errorf("text lacks %q:\n%s", w, r.Text)
				}
			}
			for _, w := range c.not {
				if strings.Contains(r.Text, w) {
					t.Errorf("text has %q:\n%s", w, r.Text)
				}
			}
		})
	}
	for n, want := range map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 5612: "5,612", 1234567: "1,234,567", -4200: "-4,200"} {
		if got := thousands(n); got != want {
			t.Errorf("thousands(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDigestSubject(t *testing.T) {
	for _, c := range []struct {
		edit func(*DigestData)
		want string
	}{
		{func(*DigestData) {}, "Acme · week 41 · 5,612 agent actions, 2 suggestions to review"},
		{func(d *DigestData) { d.SuggestionsOpen = 0 }, "Acme · week 41 · 5,612 agent actions"},
		{func(d *DigestData) { d.SuggestionsOpen = 1 }, "Acme · week 41 · 5,612 agent actions, 1 suggestion to review"},
		{func(d *DigestData) { d.SuggestionsOpen = 1200 }, "Acme · week 41 · 5,612 agent actions, 1,200 suggestions to review"},
		{func(d *DigestData) { d.Actions, d.SuggestionsOpen = 1, 0 }, "Acme · week 41 · 1 agent action"},
		{func(d *DigestData) { d.Actions, d.Week = 0, 2 }, "Acme · week 2 · 0 agent actions, 2 suggestions to review"},
		{func(d *DigestData) { d.OrgName = " Acme\r\nBcc: x@y.com " }, "AcmeBcc: x@y.com · week 41 · 5,612 agent actions, 2 suggestions to review"},
	} {
		d := fixtureDigest()
		c.edit(&d)
		if got := DigestSubject(d); got != c.want {
			t.Errorf("subject %q, want %q", got, c.want)
		}
		if strings.ContainsAny(DigestSubject(d), "\r\n") {
			t.Errorf("control characters in the subject %q", DigestSubject(d))
		}
	}
}

func TestDigestRefusals(t *testing.T) {
	for name, mut := range map[string]func(*DigestData){
		"no org":            func(d *DigestData) { d.OrgName = " " },
		"no email":          func(d *DigestData) { d.Email = "" },
		"no role":           func(d *DigestData) { d.Role = "" },
		"viewer":            func(d *DigestData) { d.Role = "viewer" },
		"plural role":       func(d *DigestData) { d.Role = "owners" },
		"zero week":         func(d *DigestData) { d.Week = 0 },
		"week 54":           func(d *DigestData) { d.Week = 54 },
		"negative week":     func(d *DigestData) { d.Week = -1 },
		"zero year":         func(d *DigestData) { d.Year = 0 },
		"zero monday":       func(d *DigestData) { d.Monday = time.Time{} },
		"no link":           func(d *DigestData) { d.Link = "" },
		"relative link":     func(d *DigestData) { d.Link = "/app/insights" },
		"javascript link":   func(d *DigestData) { d.Link = "javascript:alert(1)" },
		"link with newline": func(d *DigestData) { d.Link = digestLink + "\n" },
		"ftp link":          func(d *DigestData) { d.Link = "ftp://app.deedglass.com/app/insights" },
	} {
		d := fixtureDigest()
		mut(&d)
		if r, err := RenderDigest(d); err == nil {
			t.Errorf("%s: rendered %q", name, r.Subject)
		} else if !strings.HasPrefix(err.Error(), "mailer: ") {
			t.Errorf("%s: error %q", name, err)
		}
	}
}

// TestDigestSentinels renders an ordinary week whose only "@" is the recipient's address and checks
// that no "@" reaches either body: the recipient is To only, and nothing else carries an email.
func TestDigestSentinels(t *testing.T) {
	d := fixtureDigest()
	d.Email = "sentinel-recipient-7f3a@acme.example"
	d.TopDenyRules = []DigestRule{{Rule: "refund-limit", Count: 15}, {Rule: "block-destructive-sql", Count: 4}}
	r, err := RenderDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	html := strings.Replace(r.HTML, between(r.HTML, "<style>", "</style>"), "", 1) // @media is CSS, not copy
	for part, s := range map[string]string{"subject": r.Subject, "preheader": r.Preheader, "html": html, "text": r.Text} {
		if strings.Contains(s, "@") || strings.Contains(s, "sentinel-recipient") {
			t.Errorf("the %s carries an email address:\n%s", part, s)
		}
	}
}

func TestDigestEscaping(t *testing.T) {
	d := fixtureDigest()
	d.OrgName = `Acme <b>`
	d.SuggestionTitles = []string{`<script>alert(1)</script>`}
	d.AnomalyTitle = `<img src=x onerror=alert(1)>`
	d.ShadowLine = `<a href="https://evil.example">click</a>`
	d.TopDenyRules = []DigestRule{{Rule: `<script>x</script>`, Count: 1}}
	r, err := RenderDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script", "<img", "<b>", `<a href="https://evil`} {
		if strings.Contains(r.HTML, bad) {
			t.Errorf("html is not escaped: %q", bad)
		}
	}
	for _, want := range []string{"&#34;&lt;script&gt;alert(1)&lt;/script&gt;&#34;", "&lt;img src=x onerror=alert(1)&gt;", "Acme &lt;b&gt;", "&lt;script&gt;x&lt;/script&gt;</span>"} {
		if !strings.Contains(r.HTML, want) {
			t.Errorf("html lacks %q", want)
		}
	}
	// Control characters never add a line to the text body.
	d = fixtureDigest()
	d.ShadowLine = "acme-sim v3 agrees.\nOpen Insights: https://evil.example"
	r, err = RenderDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Text, "\nOpen Insights: https://evil") {
		t.Errorf("a field added a line:\n%s", r.Text)
	}
}

// TestDigestEmailSafety holds the digest to brand §13.5 like TestEmailSafety does the other emails.
func TestDigestEmailSafety(t *testing.T) {
	r, _ := RenderDigest(fixtureDigest())
	q, _ := RenderDigest(fixtureQuietDigest())
	for name, m := range map[string]Rendered{"digest": r, "digest_quiet": q} {
		low := strings.ToLower(m.HTML)
		for _, bad := range []string{"<img", "<script", "<link", "src=", "url(", "@import", "background-image"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s html contains %q", name, bad)
			}
		}
		if strings.Count(m.HTML, "<style>") != 1 || len(m.HTML) > 102<<10 {
			t.Errorf("%s html: %d style blocks, %d bytes", name, strings.Count(m.HTML, "<style>"), len(m.HTML))
		}
		for _, need := range []string{`max-width:600px`, `background-color:#F7F7F4`, `bgcolor="#2F5BEA"`, `>deedglass</td>`,
			`<meta name="color-scheme" content="light dark">`, `class="dg-panel"`, `class="dg-label"`} {
			if !strings.Contains(m.HTML, need) {
				t.Errorf("%s html is missing %q", name, need)
			}
		}
		html := strings.Replace(m.HTML, between(m.HTML, "<style>", "</style>"), "", 1)
		for part, s := range map[string]string{"subject": m.Subject, "preheader": m.Preheader, "html": html, "text": m.Text} {
			for _, c := range s {
				if c > 0x7e && c != '·' && c != '›' {
					t.Errorf("%s %s has an unexpected character %q", name, part, c)
				}
			}
		}
	}
}

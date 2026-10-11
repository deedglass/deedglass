package sections

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/policy"
)

var update = flag.Bool("update", false, "rewrite the email_preview goldens")

const step1 = `{"to":"maya@acme-robotics.example","subject":"your agent and Postgres",
"body_text":"Hi Maya,\n\nYour jobs post says the support agent updates order records in Postgres.\n\nOne question: would anyone see it?\n\nArjun Prabhakaran\nFounder, Deedglass\n",
"body_sha256":"sha256:x","step":1,"thread":null,"unsubscribe":{"token":"k3x7abcdefghijklmnopqrstuvwxyz23"},
"window":{"not_before":"2026-10-21T15:30:00Z","not_after":"2026-10-21T17:30:00Z","tz":"America/Los_Angeles"},
"arm":"A","review":{"company":"Acme Robotics","signal_summary":"HN jobs post: support agent updates order records in Postgres.","lint_summary":"14 of 14","judge_summary":"flag only · 6 of 6","cta":"scan_command"},
"signal":{"url":"https://news.ycombinator.com/item?id=1","observed_at":"2026-10-12T14:02:00Z"}}`

func input(args string, mail policy.MailFacts) gate.ApprovalInput {
	in := gate.ApprovalInput{Args: json.RawMessage(args)}
	in.Tool, in.ConnectorType = "mail.send", "outbound_mail"
	in.Request.Mail = mail
	return in
}

func TestEmailPreviewGoldens(t *testing.T) {
	facts := policy.MailFacts{From: "arjun@trydeedglass.example", Words: 20, Questions: 1, Step: 1, Enriched: true}
	threaded := strings.Replace(step1, `"step":1,"thread":null`, `"step":2,"thread":{"gmail_thread_id":"t1","in_reply_to":"<a@b>","references":["<a@b>"]}`, 1)
	cases := map[string]gate.ApprovalInput{
		"step1":    input(step1, facts),
		"threaded": input(threaded, policy.MailFacts{From: "arjun@trydeedglass.example", Words: 9, Step: 2, Threaded: true, Enriched: true}),
	}
	for name, in := range cases {
		got, err := Build(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > approve.MaxSectionBytes {
			t.Fatalf("%s: %d bytes", name, len(got))
		}
		var pretty map[string]any
		_ = json.Unmarshal(got, &pretty)
		out, _ := json.MarshalIndent(pretty, "", "  ")
		p := filepath.Join("testdata", "email_preview_"+name+".json")
		if *update {
			_ = os.WriteFile(p, append(out, '\n'), 0o644)
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != string(out)+"\n" {
			t.Errorf("%s differs (run -update and review):\n%s", name, out)
		}
	}
}

func TestEmailPreviewBindsBody(t *testing.T) {
	a, _ := Build(input(step1, policy.MailFacts{}))
	b, _ := Build(input(strings.Replace(step1, "Maya,", "Maye,", 1), policy.MailFacts{}))
	if string(a) == string(b) {
		t.Fatal("one changed body byte must change the section (and so the context digest)")
	}
}

func TestEmailPreviewMatches(t *testing.T) {
	for _, c := range []struct {
		tool, ctype string
		want        bool
	}{{"mail.send", "outbound_mail", true}, {"gmail.create_draft", "gmail", true}, {"mail.get_message", "outbound_mail", false},
		{"gmail.update_draft", "gmail", false}, {"x.send", "rest", false}, {"send", "outbound_mail", false}} {
		if Matches(c.tool, c.ctype) != c.want {
			t.Errorf("%s %s", c.tool, c.ctype)
		}
	}
}

func TestEmailPreviewLongBodyAndGmailList(t *testing.T) {
	long := strings.Repeat("word ", 4000)
	args := `{"to":["a@b.example","c@d.example"],"subject":"s","body_text":"` + long + `"}`
	in := gate.ApprovalInput{Args: json.RawMessage(args)}
	in.Tool, in.ConnectorType = "gmail.create_draft", "gmail"
	got, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	var p EmailPreview
	_ = json.Unmarshal(got, &p)
	if !p.BodyClipped || len(got) > approve.MaxSectionBytes || p.To != "a@b.example, c@d.example" || p.Words != 4000 {
		t.Fatalf("%d bytes, %+v", len(got), p.To)
	}
	if _, err := Build(gate.ApprovalInput{Args: json.RawMessage(`[`)}); err == nil {
		t.Fatal("bad args")
	}
	RegisterEmail()
	RegisterEmail() // twice is safe
}

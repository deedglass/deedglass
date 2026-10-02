package mailer

import (
	"strings"
	"testing"
	"time"
)

const approvalLink = "http://127.0.0.1:8080/approve#t=OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc"

var approvalNow = time.Date(2026, 10, 8, 4, 41, 7, 0, time.UTC)

func ist(t *testing.T) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func fixtureApproval(t *testing.T) ApprovalData {
	return ApprovalData{AgentName: "claude-desktop", OrgName: "Acme Demo", Tool: "postgres.execute", RuleID: "orders-writes-need-approval",
		Reason: "Writes to the orders database need a person to countersign.", Summary: "UPDATE about 1 row in public.orders", Estimated: true,
		Required: 1, ExpiresAt: approvalNow.Add(30 * time.Minute), Location: ist(t), ApprovalID: "apr_01m3y2z9q4hk7c8vd5w3e6f2ab",
		Email: "priya@acme.com", Link: approvalLink}
}

func fixtureFourEyes(t *testing.T) ApprovalData {
	d := fixtureApproval(t)
	d.Required = 2
	d.Email = "arjun@acme.com"
	d.CountersignedBy = []ApprovalCountersign{{Name: "Priya Raman", At: approvalNow.Add(-27 * time.Minute)}}
	return d
}

func TestApprovalGoldens(t *testing.T) {
	r, err := RenderApproval(fixtureApproval(t), approvalNow)
	if err != nil {
		t.Fatal(err)
	}
	f, err := RenderApproval(fixtureFourEyes(t), approvalNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got string }{
		{"approval_requested.html", r.HTML}, {"approval_requested.txt", r.Text},
		{"approval_foureyes.html", f.HTML}, {"approval_foureyes.txt", f.Text},
	} {
		t.Run(c.name, func(t *testing.T) { golden(t, c.name, c.got) })
	}
}

func TestApprovalCopy(t *testing.T) {
	r, err := RenderApproval(fixtureApproval(t), approvalNow)
	if err != nil {
		t.Fatal(err)
	}
	if r.Subject != "claude-desktop is waiting for your countersign" {
		t.Errorf("subject %q", r.Subject)
	}
	if r.Preheader != "postgres.execute on Acme Demo · UPDATE about 1 row in public.orders · expires 10:41 IST" {
		t.Errorf("preheader %q", r.Preheader)
	}
	for _, want := range []string{
		"claude-desktop is waiting for you\n",
		`It wants to run postgres.execute on Acme Demo. Policy "orders-writes-need-approval" asks a person to countersign: Writes to the orders database need a person to countersign.`,
		"  UPDATE about 1 row in public.orders (planner estimate)\n",
		"  Expires  10:41 IST · in 30 min\n",
		"Review and countersign:\n" + approvalLink + "\n",
		"This link is yours alone. It works once, until 10:41 IST. Nothing runs unless you approve.\n",
		"1 of 1 countersign needed · approval apr_01m3y2z9…\n",
		"If you weren't expecting this, you can ignore it. Nothing happens.\n",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("text part lacks %q:\n%s", want, r.Text)
		}
	}
	// One action, no images, no tracking, small enough that Gmail never clips it (102 KB).
	if strings.Count(r.HTML, "<a href=") != 2 || strings.Contains(r.HTML, "<img") || len(r.HTML) > 100_000 {
		t.Errorf("html: %d links, img %v, %d bytes", strings.Count(r.HTML, "<a href="), strings.Contains(r.HTML, "<img"), len(r.HTML))
	}
	// The SQL never leaves Deedglass by email unless the org asked for it (email_detail: full).
	if strings.Contains(r.HTML, "UPDATE orders SET") {
		t.Error("the summary email carries SQL")
	}
	f, _ := RenderApproval(fixtureFourEyes(t), approvalNow)
	if !strings.Contains(f.Text, "Priya Raman countersigned at 09:44 IST. One more countersign is needed.") ||
		!strings.Contains(f.Text, "1 of 2 countersigns needed") {
		t.Errorf("four-eyes text:\n%s", f.Text)
	}
	for _, s := range []string{r.HTML, r.Text, f.HTML, f.Text} {
		if strings.ContainsAny(s, "—–") || strings.Contains(strings.NewReplacer("<!DOCTYPE", "", "!important", "").Replace(s), "!") {
			t.Error("an em or en dash, or an exclamation mark, in the approval email")
		}
	}
}

func TestApprovalRefusals(t *testing.T) {
	for name, mut := range map[string]func(*ApprovalData){
		"no agent": func(d *ApprovalData) { d.AgentName = "" },
		"no email": func(d *ApprovalData) { d.Email = " " },
		"bad link": func(d *ApprovalData) { d.Link = "javascript:alert(1)" },
		"expired":  func(d *ApprovalData) { d.ExpiresAt = approvalNow.Add(-time.Second) },
		"no tool":  func(d *ApprovalData) { d.Tool = "" },
		"no org":   func(d *ApprovalData) { d.OrgName = "" },
	} {
		d := fixtureApproval(t)
		mut(&d)
		if _, err := ApprovalMessage(d, approvalNow); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
	d := fixtureApproval(t)
	d.ReviewOnly, d.Self, d.Detail = true, true, "UPDATE orders SET status = 'shipped' WHERE id = 42"
	m, err := ApprovalMessage(d, approvalNow)
	if err != nil || m.Kind != "approval" || !strings.Contains(m.Text, "Review the action:") ||
		!strings.Contains(m.Text, "another approver has to countersign it") || !strings.Contains(m.Text, "WHERE id = 42") {
		t.Errorf("%v %+v", err, m.Text)
	}
}

package mailer

//deedglass:allow-text-template the plain-text part of an email (*.txt.tmpl); every HTML part uses html/template

import (
	"bytes"
	"embed"
	"errors"
	htmltemplate "html/template"
	"net/url"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode"
	"unicode/utf8"
)

// templateFS holds the email templates: one HTML and one text file per kind, and the shared HTML
// layout (head and footer).
//
//go:embed templates/*
var templateFS embed.FS

var (
	htmlTemplates = htmltemplate.Must(htmltemplate.New("mail").Option("missingkey=error").Funcs(htmltemplate.FuncMap{"button": newButton}).ParseFS(templateFS, "templates/*.html.tmpl"))
	textTemplates = texttemplate.Must(texttemplate.New("mail").Option("missingkey=error").ParseFS(templateFS, "templates/*.txt.tmpl"))
)

// Subjects.
const (
	loginSubject = "Sign in to Deedglass"
)

// LoginData fills the sign-in email.
type LoginData struct {
	// OrgName is the org being signed in to ("Acme").
	OrgName string
	// Email is the recipient, shown in the body and used as To.
	Email string
	// Link is the one-time sign-in link (http or https).
	Link string
	// Code is the 6-digit one-time code; it is shown as "482 913".
	Code string
	// RequestedFrom describes the requesting browser ("Chrome on macOS"); empty omits it.
	RequestedFrom string
	// RequestedAt is when the sign-in was requested, shown as 15:04 UTC; zero omits the line.
	RequestedAt time.Time
	// ExpiresIn is how long the link and code work (15 minutes shows as "15 minutes").
	ExpiresIn time.Duration
}

// InviteData fills the invite email.
type InviteData struct {
	// InviterName is the inviting person's name; empty uses InviterEmail instead.
	InviterName string
	// InviterEmail is the inviting person's address.
	InviterEmail string
	// OrgName is the org the invite joins.
	OrgName string
	// Role is the role in lowercase ("viewer"); it is shown capitalised ("Viewer").
	Role string
	// Email is the invited address, used as To.
	Email string
	// Link is the one-time accept link (http or https).
	Link string
	// ExpiresAt is when the invite stops working, shown as "10 October 2026" (UTC).
	ExpiresAt time.Time
	// Now is the render time used for "expires in 7 days"; zero means time.Now().
	Now time.Time
}

// Rendered is one rendered email.
type Rendered struct {
	// Subject is the subject line.
	Subject string
	// Preheader is the inbox preview text, hidden at the top of the HTML body.
	Preheader string
	// HTML is the HTML body.
	HTML string
	// Text is the plain-text alternative.
	Text string
}

// loginView is what the login templates see; every field is already formatted.
type loginView struct {
	Subject, Preheader, OrgName, Email, Link, Code, ExpiresIn, Requested string
}

// inviteView is what the invite templates see; every field is already formatted.
type inviteView struct {
	Subject, Preheader, Inviter, OrgName, Role, Link, ExpiresOn string
}

// RenderLogin renders the sign-in email. It returns an error when a required field is missing,
// the link is not http(s), the code is not 6 digits or ExpiresIn is under a minute.
func RenderLogin(d LoginData) (Rendered, error) {
	org, email, from := clean(d.OrgName), clean(d.Email), clean(d.RequestedFrom)
	switch {
	case org == "":
		return Rendered{}, errors.New("mailer: the sign-in email needs an org name; set LoginData.OrgName")
	case email == "":
		return Rendered{}, errors.New("mailer: the sign-in email needs a recipient; set LoginData.Email")
	case !sixDigits(d.Code):
		return Rendered{}, errors.New("mailer: the sign-in code must be 6 digits; check LoginData.Code")
	case d.ExpiresIn < time.Minute:
		return Rendered{}, errors.New("mailer: the sign-in link must work for at least a minute; check LoginData.ExpiresIn")
	}
	if err := checkLink(d.Link); err != nil {
		return Rendered{}, err
	}
	expires := humanDuration(d.ExpiresIn)
	v := loginView{
		Subject:   loginSubject,
		Preheader: "Your sign-in link for " + org + ". It works once and expires in " + expires + ".",
		OrgName:   org,
		Email:     email,
		Link:      d.Link,
		Code:      d.Code[:3] + " " + d.Code[3:],
		ExpiresIn: expires,
	}
	if !d.RequestedAt.IsZero() {
		at := d.RequestedAt.UTC().Format("15:04") + " UTC"
		if from != "" {
			v.Requested = "Requested from " + from + " at " + at + "."
		} else {
			v.Requested = "Requested at " + at + "."
		}
	}
	return render("login", v, v.Subject, v.Preheader)
}

// RenderInvite renders the invite email. It returns an error when a required field is missing,
// the link is not http(s) or the invite has already expired at Now.
func RenderInvite(d InviteData) (Rendered, error) {
	inviter := clean(d.InviterName)
	if inviter == "" {
		inviter = clean(d.InviterEmail)
	}
	org, role, email := clean(d.OrgName), clean(d.Role), clean(d.Email)
	now := d.Now
	if now.IsZero() {
		now = time.Now()
	}
	switch {
	case inviter == "":
		return Rendered{}, errors.New("mailer: the invite email needs the inviter; set InviteData.InviterName or InviterEmail")
	case org == "":
		return Rendered{}, errors.New("mailer: the invite email needs an org name; set InviteData.OrgName")
	case role == "":
		return Rendered{}, errors.New("mailer: the invite email needs a role; set InviteData.Role")
	case email == "":
		return Rendered{}, errors.New("mailer: the invite email needs a recipient; set InviteData.Email")
	case !d.ExpiresAt.After(now):
		return Rendered{}, errors.New("mailer: the invite has already expired; check InviteData.ExpiresAt")
	}
	if err := checkLink(d.Link); err != nil {
		return Rendered{}, err
	}
	asRole := article(role) + " " + capitalise(role)
	days := int((d.ExpiresAt.Sub(now) + 24*time.Hour - time.Second) / (24 * time.Hour))
	v := inviteView{
		Subject:   inviter + " invited you to " + org + " on Deedglass",
		Preheader: "Join " + org + " as " + asRole + ". The invite expires in " + plural(days, "day") + ".",
		Inviter:   inviter,
		OrgName:   org,
		Role:      asRole,
		Link:      d.Link,
		ExpiresOn: d.ExpiresAt.UTC().Format("2 January 2006"),
	}
	return render("invite", v, v.Subject, v.Preheader)
}

// LoginMessage renders the sign-in email as a Message of kind "login" to d.Email. The caller
// sets IdempotencyKey, UserID and RequestID.
func LoginMessage(d LoginData) (Message, error) {
	r, err := RenderLogin(d)
	if err != nil {
		return Message{}, err
	}
	return Message{To: clean(d.Email), Subject: r.Subject, HTML: r.HTML, Text: r.Text, Kind: "login"}, nil
}

// InviteMessage renders the invite email as a Message of kind "invite" to d.Email. The caller
// sets IdempotencyKey, UserID and RequestID.
func InviteMessage(d InviteData) (Message, error) {
	r, err := RenderInvite(d)
	if err != nil {
		return Message{}, err
	}
	return Message{To: clean(d.Email), Subject: r.Subject, HTML: r.HTML, Text: r.Text, Kind: "invite"}, nil
}

// render executes the HTML and text templates for kind.
func render(kind string, v any, subject, preheader string) (Rendered, error) {
	var h, t bytes.Buffer
	if err := htmlTemplates.ExecuteTemplate(&h, kind+".html.tmpl", v); err != nil {
		return Rendered{}, err
	}
	if err := textTemplates.ExecuteTemplate(&t, kind+".txt.tmpl", v); err != nil {
		return Rendered{}, err
	}
	return Rendered{Subject: subject, Preheader: preheader, HTML: h.String(), Text: t.String()}, nil
}

// clean trims s and removes control characters, so that no field can add a line to the text
// body or a header.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s))
}

// checkLink requires an absolute http or https URL with no control characters.
func checkLink(link string) error {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || clean(link) != link {
		return errors.New("mailer: the link must be an absolute http or https URL")
	}
	return nil
}

// sixDigits reports whether s is exactly 6 ASCII digits.
func sixDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// humanDuration shows d in whole hours when it is a whole number of hours, else in minutes
// (rounded): "15 minutes", "1 hour".
func humanDuration(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d.Round(time.Minute)/time.Minute), "minute")
}

// plural returns "1 day" or "7 days".
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// capitalise upper-cases the first letter of s.
func capitalise(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// article returns "an" before a vowel and "a" otherwise.
func article(s string) string {
	if s != "" && strings.ContainsRune("aeiouAEIOU", rune(s[0])) {
		return "an"
	}
	return "a"
}

// buttonView is what the layout's "button" template sees.
type buttonView struct{ Link, Label string }

// newButton is the templates' "button" function: {{template "button" button .Link "Label"}}.
func newButton(link, label string) buttonView { return buttonView{Link: link, Label: label} }

// ApprovalData fills the approval email (S22 §5.2): one person's link to countersign one action.
type ApprovalData struct {
	AgentName string // "claude-desktop"
	OrgName   string
	Tool      string // "postgres.execute"
	RuleID    string // the rule's id; empty when the default asked
	Reason    string // the policy author's reason for the rule
	// Summary is the plain sentence ("UPDATE about 1 row in public.orders"); Estimated adds
	// "(planner estimate)".
	Summary   string
	Estimated bool
	// Detail is up to 300 characters of the SQL (approvals.email_detail: full); empty by default,
	// so no argument values leave Deedglass by email.
	Detail          string
	Required        int
	CountersignedBy []ApprovalCountersign
	ExpiresAt       time.Time
	Location        *time.Location // the org's time zone; UTC when nil
	ApprovalID      string
	Email           string // the recipient, used as To
	Link            string // {base}/approve#t=…
	ReviewOnly      bool   // the org decides approvals in the Console
	Self            bool   // the action runs on the recipient's behalf
}

// ApprovalCountersign is one earlier countersign of a four-eyes approval.
type ApprovalCountersign struct {
	Name string
	At   time.Time
}

type approvalView struct {
	Subject, Preheader, Agent, OrgName, Tool, Rule, Reason, Summary, Detail, Needs, Expires string
	Countersigns                                                                            []string
	Self                                                                                    bool
	Link, ButtonLabel, Fineprint, Count, ApprovalShort                                      string
}

// RenderApproval renders the approval email. It refuses a missing agent, tool, org or recipient,
// a link that isn't http(s), and an approval that has already expired.
func RenderApproval(d ApprovalData, now time.Time) (Rendered, error) {
	agent, org, tool, email := clean(d.AgentName), clean(d.OrgName), clean(d.Tool), clean(d.Email)
	switch {
	case agent == "" || tool == "" || org == "":
		return Rendered{}, errors.New("mailer: the approval email needs the agent, the tool and the org; set ApprovalData")
	case email == "":
		return Rendered{}, errors.New("mailer: the approval email needs a recipient; set ApprovalData.Email")
	case !d.ExpiresAt.After(now):
		return Rendered{}, errors.New("mailer: the approval has already expired; check ApprovalData.ExpiresAt")
	}
	if err := checkLink(d.Link); err != nil {
		return Rendered{}, err
	}
	loc := d.Location
	if loc == nil {
		loc = time.UTC
	}
	clock := d.ExpiresAt.In(loc).Format("15:04 MST")
	summary := clean(d.Summary)
	if summary == "" {
		summary = "run " + tool
	}
	shown := capitalise(summary)
	if d.Estimated {
		shown += " (planner estimate)"
	}
	required := max(d.Required, 1)
	v := approvalView{
		Subject:   agent + " is waiting for your countersign",
		Preheader: tool + " on " + org + " · " + summary + " · expires " + clock,
		Agent:     agent, OrgName: org, Tool: tool, Rule: clean(d.RuleID), Reason: clean(d.Reason), Summary: shown, Detail: clean(d.Detail),
		Needs:   needs(required, len(d.CountersignedBy)),
		Expires: clock + " · in " + untilWords(d.ExpiresAt.Sub(now)),
		Self:    d.Self, Link: d.Link, ButtonLabel: "Review and countersign",
		Fineprint:     "This link is yours alone. It works once, until " + clock + ". Nothing runs unless you approve.",
		Count:         itoaInt(max(required-len(d.CountersignedBy), 1)) + " of " + itoaInt(required) + " " + word(required, "countersign") + " needed",
		ApprovalShort: shortID(clean(d.ApprovalID)),
	}
	if d.ReviewOnly {
		v.ButtonLabel = "Review the action"
		v.Fineprint = "This link is yours alone and works until " + clock + ". Your org decides approvals in the Console."
	}
	for _, c := range d.CountersignedBy {
		left := required - len(d.CountersignedBy)
		more := "One more countersign is needed."
		if left > 1 {
			more = itoaInt(left) + " more countersigns are needed."
		}
		v.Countersigns = append(v.Countersigns, clean(c.Name)+" countersigned at "+c.At.In(loc).Format("15:04 MST")+". "+more)
	}
	return render("approval", v, v.Subject, v.Preheader)
}

// ApprovalMessage renders the approval email as a Message of kind "approval" to d.Email. The
// caller sets IdempotencyKey (approval/<apr>/<token id>), UserID and RequestID.
func ApprovalMessage(d ApprovalData, now time.Time) (Message, error) {
	r, err := RenderApproval(d, now)
	if err != nil {
		return Message{}, err
	}
	return Message{To: clean(d.Email), Subject: r.Subject, HTML: r.HTML, Text: r.Text, Kind: "approval"}, nil
}

func itoaInt(n int) string { return strconv.Itoa(n) }

// untilWords is "27 min", "1 h 52 min", "under a minute".
func untilWords(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if m == 0 {
		return strconv.Itoa(h) + " h"
	}
	return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
}

// shortID is an id cut for people: "apr_01j9zw6q…".
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12] + "…"
}

// needs is the context table's Needs row: "1 person", "2 people · 1 countersigned".
func needs(required, done int) string {
	s := "1 person"
	if required != 1 {
		s = itoaInt(required) + " people"
	}
	if done > 0 {
		s += " · " + itoaInt(done) + " countersigned"
	}
	return s
}

func word(n int, w string) string {
	if n == 1 {
		return w
	}
	return w + "s"
}

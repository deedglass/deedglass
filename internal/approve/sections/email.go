// Package sections holds approval context sections other than undo (S40): email_preview, what a
// person reads before countersigning an email. Pure: it reads the action's arguments and the
// connector's facts, never the network, and stays under approve.MaxSectionBytes.
package sections

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/gate"
)

// EmailPreviewVersion is the section's version.
const EmailPreviewVersion = 1

// MaxPreviewBody is how much of the body the section carries (the section limit is 8 KiB).
const MaxPreviewBody = 6 << 10

// EmailPreview is the "email_preview" section.
type EmailPreview struct {
	V           int          `json:"v"`
	From        string       `json:"from"`
	To          string       `json:"to"`
	Subject     string       `json:"subject"`
	BodyText    string       `json:"body_text"`
	BodyClipped bool         `json:"body_clipped,omitempty"`
	Words       int          `json:"words"`
	Links       int          `json:"links"`
	Questions   int          `json:"questions"`
	Step        int          `json:"step"`
	Threaded    bool         `json:"threaded"`
	Unsubscribe *Unsubscribe `json:"unsubscribe,omitempty"`
	Window      *WindowText  `json:"window,omitempty"`
	Review      *Review      `json:"review,omitempty"`
	Summary     string       `json:"summary"`
}

// Unsubscribe says which unsubscribe headers the message carries.
type Unsubscribe struct {
	OneClick bool `json:"one_click"`
	Mailto   bool `json:"mailto"`
}

// WindowText is the send window in the recipient's time and in IST.
type WindowText struct {
	Local string `json:"local"`
	IST   string `json:"ist"`
}

// Review is what the sender attached for the person reading (args.review, args.signal).
type Review struct {
	Company       string `json:"company,omitempty"`
	SignalSummary string `json:"signal_summary,omitempty"`
	SignalURL     string `json:"signal_url,omitempty"`
	SignalDate    string `json:"signal_date,omitempty"`
	Arm           string `json:"arm,omitempty"`
	LintSummary   string `json:"lint_summary,omitempty"`
	JudgeSummary  string `json:"judge_summary,omitempty"`
	CTA           string `json:"cta,omitempty"`
}

// Matches reports the tools the section covers: outbound_mail's send and Gmail's create_draft.
func Matches(tool, connectorType string) bool {
	_, name, ok := strings.Cut(tool, ".")
	if !ok {
		return false
	}
	return (connectorType == "outbound_mail" && name == "send") || (connectorType == "gmail" && name == "create_draft")
}

type mailArgs struct {
	To          json.RawMessage `json:"to"` // a string (outbound_mail) or a list (gmail)
	Subject     string          `json:"subject"`
	BodyText    string          `json:"body_text"`
	Step        int             `json:"step"`
	Thread      json.RawMessage `json:"thread"`
	ThreadID    string          `json:"thread_id"`
	InReplyTo   string          `json:"in_reply_to"`
	Unsubscribe *struct {
		Token string `json:"token"`
	} `json:"unsubscribe"`
	Window *struct {
		NotBefore time.Time `json:"not_before"`
		NotAfter  time.Time `json:"not_after"`
		TZ        string    `json:"tz"`
	} `json:"window"`
	Arm    string `json:"arm"`
	Review *struct {
		Company       string `json:"company"`
		SignalSummary string `json:"signal_summary"`
		LintSummary   string `json:"lint_summary"`
		JudgeSummary  string `json:"judge_summary"`
		CTA           string `json:"cta"`
	} `json:"review"`
	Signal *struct {
		URL        string `json:"url"`
		ObservedAt string `json:"observed_at"`
	} `json:"signal"`
}

// Build is the section for one action (approve.SectionFunc).
func Build(in gate.ApprovalInput) (json.RawMessage, error) {
	var a mailArgs
	if err := json.Unmarshal(in.Args, &a); err != nil {
		return nil, err
	}
	p := EmailPreview{V: EmailPreviewVersion, From: in.Request.Mail.From, Subject: clip(a.Subject, 200), Step: a.Step}
	var to string
	if json.Unmarshal(a.To, &to) != nil {
		var list []string
		_ = json.Unmarshal(a.To, &list)
		to = strings.Join(list, ", ")
	}
	p.To = clip(to, 320)
	body := a.BodyText
	if len(body) > MaxPreviewBody {
		body, p.BodyClipped = clipBytes(body, MaxPreviewBody), true
	}
	p.BodyText = body
	p.Words = words(a.BodyText, "Arjun Prabhakaran")
	if mf := in.Request.Mail; mf.Enriched {
		p.Words, p.Links, p.Questions = int(mf.Words), int(mf.Links), int(mf.Questions)
		p.Threaded = mf.Threaded
	} else {
		p.Links = strings.Count(a.BodyText, "http://") + strings.Count(a.BodyText, "https://")
		p.Questions = strings.Count(a.BodyText, "?")
		p.Threaded = a.ThreadID != "" || a.InReplyTo != "" || (len(a.Thread) > 0 && string(a.Thread) != "null")
	}
	if a.Unsubscribe != nil && a.Unsubscribe.Token != "" {
		p.Unsubscribe = &Unsubscribe{OneClick: true, Mailto: true}
	}
	if w := a.Window; w != nil && !w.NotBefore.IsZero() {
		p.Window = windowText(w.NotBefore, w.NotAfter, w.TZ)
	}
	if a.Review != nil || a.Signal != nil || a.Arm != "" {
		r := &Review{Arm: clip(a.Arm, 16)}
		if a.Review != nil {
			r.Company, r.SignalSummary = clip(a.Review.Company, 120), clip(a.Review.SignalSummary, 300)
			r.LintSummary, r.JudgeSummary, r.CTA = clip(a.Review.LintSummary, 40), clip(a.Review.JudgeSummary, 60), clip(a.Review.CTA, 32)
		}
		if a.Signal != nil {
			if strings.HasPrefix(a.Signal.URL, "https://") {
				r.SignalURL = clip(a.Signal.URL, 300)
			}
			if len(a.Signal.ObservedAt) >= 10 {
				r.SignalDate = a.Signal.ObservedAt[:10]
			}
		}
		p.Review = r
	}
	p.Summary = summary(p)
	return json.Marshal(p)
}

func summary(p EmailPreview) string {
	s := "Email to " + p.To
	if p.Review != nil && p.Review.Company != "" {
		s = "Email to " + p.Review.Company
	}
	s += ": " + p.Subject
	if p.Window != nil {
		s += " · sends " + p.Window.IST
	}
	return clip(s, 270)
}

func windowText(nb, na time.Time, tz string) *WindowText {
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		ist = time.FixedZone("IST", 5*3600+1800)
	}
	l1, l2 := nb.In(loc), na.In(loc)
	i1, i2 := nb.In(ist), na.In(ist)
	return &WindowText{Local: l1.Format("Mon 2 Jan, 15:04") + " to " + l2.Format("15:04 MST"),
		IST: i1.Format("15:04") + " to " + i2.Format("15:04") + " IST"}
}

func words(body, sig string) int {
	n := 0
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if sig != "" && strings.TrimSpace(line) == sig {
			break
		}
		n += len(strings.Fields(line))
	}
	return n
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

var once sync.Once

// RegisterEmail registers email_preview once per process.
func RegisterEmail() {
	once.Do(func() { approve.RegisterSection("email_preview", Matches, Build) })
}

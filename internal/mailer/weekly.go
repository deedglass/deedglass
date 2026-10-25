package mailer

import (
	"errors"
	"strings"
)

// WeeklyItem is one caught thing in the Monday email: its sentence, its free way back and, when
// the server can't undo sessions, the one Pro line.
type WeeklyItem struct {
	Line string // "the L2 check of postgres.execute doesn't match what ops-agent said."
	Free string // "Undo it yourself, free: deedglass undo preview act_…"
	Pro  bool
}

// WeeklyData fills "What your agents did this week" (S52 §3.1). internal/weekly words every line;
// this file only lays them out. The email goes from the user's own server to its own owners.
type WeeklyData struct {
	Email      string // To
	Subject    string // "What your agents did this week · 412 actions · 1 mistake caught"
	Headline   string // "What your agents did this week · acme · 13 to 19 Oct"
	Numbers    string
	Check      string
	CaughtHead string // "Deedglass caught 1 thing"; "" when nothing was caught
	Nothing    string // "Nothing caught this week." when CaughtHead is ""
	Items      []WeeklyItem
	ProLine    string // "Undo the whole session in one step"
	Chain      string // "Records chain intact."
	Console    string // the user's own Console (http or https)
	Off        string // the signed one-click "stop these emails" page (http or https)
}

type weeklyView struct {
	Subject, Preheader, Headline, Numbers, Check, CaughtHead, Nothing, ProLine, Chain, Console, Off string
	Single                                                                                          bool
	Items                                                                                           []WeeklyItem
}

// RenderWeekly renders it. Errors on a missing recipient, subject or headline, or a bad link.
func RenderWeekly(d WeeklyData) (Rendered, error) {
	switch {
	case clean(d.Email) == "":
		return Rendered{}, errors.New("mailer: the weekly email needs a recipient; set WeeklyData.Email")
	case clean(d.Subject) == "" || clean(d.Headline) == "" || clean(d.Numbers) == "":
		return Rendered{}, errors.New("mailer: the weekly email needs its subject, headline and numbers")
	}
	if err := checkLink(d.Console); err != nil {
		return Rendered{}, err
	}
	if err := checkLink(d.Off); err != nil {
		return Rendered{}, err
	}
	v := weeklyView{Subject: clean(d.Subject), Headline: clean(d.Headline), Numbers: clean(d.Numbers), Check: clean(d.Check),
		CaughtHead: clean(d.CaughtHead), Nothing: clean(d.Nothing), ProLine: clean(d.ProLine), Chain: clean(d.Chain),
		Console: d.Console, Off: d.Off, Single: len(d.Items) == 1}
	for _, it := range d.Items {
		v.Items = append(v.Items, WeeklyItem{Line: clean(it.Line), Free: clean(it.Free), Pro: it.Pro && v.ProLine != ""})
	}
	v.Preheader = v.Numbers
	if v.CaughtHead != "" && len(v.Items) > 0 {
		v.Preheader = v.CaughtHead + ": " + strings.TrimSuffix(v.Items[0].Line, ".") + "."
	}
	return render("weekly", v, v.Subject, v.Preheader)
}

// WeeklyMessage renders it as a Message of kind "weekly".
func WeeklyMessage(d WeeklyData) (Message, error) {
	r, err := RenderWeekly(d)
	if err != nil {
		return Message{}, err
	}
	return Message{To: clean(d.Email), Subject: r.Subject, HTML: r.HTML, Text: r.Text, Kind: "weekly"}, nil
}

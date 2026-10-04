package mailer

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// DigestData fills the weekly Insights digest (S28 §3.9). Numbers only: never payloads, customer refs or emails besides To.
type DigestData struct {
	OrgName          string    // "Acme"
	Email            string    // the recipient (To)
	Role             string    // "owner" or "admin": the footer says why they get it
	Year, Week       int       // ISO year and week, e.g. 2026, 41
	Monday           time.Time // the week's Monday (org-local date; only Y-M-D are used)
	Actions          int64
	Verified         int64 // VERIFIED verdicts among the week's actions
	Witnessed        int64 // actions with any verdict (the verified percentage is Verified/Witnessed, one decimal, "88.9%"; "no verdicts" when 0)
	Denied           int64
	Mismatches       int64
	SuggestionsOpen  int
	SuggestionTitles []string // the top two titles by impact (may be fewer)
	AnomaliesOpen    int
	AnomalyTitle     string // the newest open anomaly's title, "" when none

	AnomaliesOpened, AnomaliesResolved int

	ShadowLine   string       // one sentence about the live shadow, or "" when none
	TopDenyRules []DigestRule // at most 3
	Link         string       // DEEDGLASS_BASE_URL + "/app/insights"; must be http(s)
}

// DigestRule is one denying rule and its count.
type DigestRule struct {
	Rule  string
	Count int64
}

// digestRule is one formatted DigestRule.
type digestRule struct{ Rule, Count string }

// digestView is what the digest templates see; every field is already formatted.
type digestView struct {
	Subject, Preheader, OrgName, Week, Range, Numbers string
	NumberParts                                       []string // the numbers line's parts, each kept on one line in HTML
	Review                                            []string // the "To review" lines; empty means nothing to review
	Anomalies, Shadow                                 string
	Rules                                             []digestRule
	Link, RoleArticle                                 string
}

// RenderDigest renders it. Errors on a missing org, recipient, role other than owner/admin, a bad link, or a zero week.
func RenderDigest(d DigestData) (Rendered, error) {
	org, email, role := clean(d.OrgName), clean(d.Email), strings.ToLower(clean(d.Role))
	switch {
	case org == "":
		return Rendered{}, errors.New("mailer: the digest needs an org name; set DigestData.OrgName")
	case email == "":
		return Rendered{}, errors.New("mailer: the digest needs a recipient; set DigestData.Email")
	case role != "owner" && role != "admin":
		return Rendered{}, errors.New(`mailer: the digest goes to owners and admins only; set DigestData.Role to "owner" or "admin"`)
	case d.Year < 1 || d.Week < 1 || d.Week > 53 || d.Monday.IsZero():
		return Rendered{}, errors.New("mailer: the digest needs its ISO year, week (1 to 53) and Monday; set DigestData.Year, Week and Monday")
	}
	if err := checkLink(d.Link); err != nil {
		return Rendered{}, err
	}
	numbers := digestNumbers(d)
	v := digestView{
		Subject:     DigestSubject(d),
		Preheader:   numbers,
		OrgName:     org,
		Week:        strconv.Itoa(d.Week),
		Range:       weekRange(d.Monday),
		Numbers:     numbers,
		NumberParts: strings.Split(numbers, " · "),
		Shadow:      clean(d.ShadowLine),
		Link:        d.Link,
		RoleArticle: article(role) + " " + role,
	}
	if s := suggestionLine(d.SuggestionsOpen, d.SuggestionTitles); s != "" {
		v.Review = append(v.Review, s)
	}
	if s := anomalyLine(d.AnomaliesOpen, d.AnomalyTitle); s != "" {
		v.Review = append(v.Review, s)
	}
	if o, r := max(d.AnomaliesOpened, 0), max(d.AnomaliesResolved, 0); o+r > 0 {
		v.Anomalies = thousands(int64(o)) + " opened · " + thousands(int64(r)) + " resolved"
	}
	for _, r := range d.TopDenyRules {
		if len(v.Rules) == 3 {
			break
		}
		if name := clean(r.Rule); name != "" {
			v.Rules = append(v.Rules, digestRule{Rule: name, Count: thousands(r.Count)})
		}
	}
	return render("digest", v, v.Subject, v.Preheader)
}

// DigestSubject is the subject line: "Acme · week 41 · 5,612 agent actions, 2 suggestions to review"
// (no suggestions: "Acme · week 41 · 5,612 agent actions"; 1: "1 suggestion to review").
func DigestSubject(d DigestData) string {
	s := clean(d.OrgName) + " · week " + strconv.Itoa(d.Week) + " · " + countOf(d.Actions, "agent action", "agent actions")
	if n := d.SuggestionsOpen; n > 0 {
		s += ", " + countOf(int64(n), "suggestion", "suggestions") + " to review"
	}
	return s
}

// digestNumbers is the numbers line: "5,612 agent actions · 88.9% verified · 311 denied · 9 mismatches".
func digestNumbers(d DigestData) string {
	verified := "no verdicts"
	if d.Witnessed > 0 {
		verified = percent(d.Verified, d.Witnessed) + " verified"
	}
	return countOf(d.Actions, "agent action", "agent actions") + " · " + verified + " · " +
		thousands(d.Denied) + " denied · " + countOf(d.Mismatches, "mismatch", "mismatches")
}

// suggestionLine is `2 suggestions: "<first title>" and 1 more`, `1 suggestion: "<title>"`, or ""
// when none are open. Only the first (highest-impact) title is named, so the line stays short.
func suggestionLine(n int, titles []string) string {
	if n <= 0 {
		return ""
	}
	s := countOf(int64(n), "suggestion", "suggestions")
	for _, t := range titles {
		if t = clean(t); t != "" {
			s += `: "` + t + `"`
			if n > 1 {
				s += " and " + thousands(int64(n-1)) + " more"
			}
			break
		}
	}
	return s
}

// anomalyLine is "1 open anomaly: <title>", "3 open anomalies, newest: <title>", or "" when none
// are open.
func anomalyLine(n int, title string) string {
	if n <= 0 {
		return ""
	}
	s := countOf(int64(n), "open anomaly", "open anomalies")
	if t := clean(title); t != "" {
		if n == 1 {
			s += ": " + t
		} else {
			s += ", newest: " + t
		}
	}
	return s
}

// weekRange is "5 to 11 Oct 2026", "28 Sep to 4 Oct 2026" or "29 Dec 2025 to 4 Jan 2026", from
// the Monday's year, month and day only.
func weekRange(monday time.Time) string {
	y, m, dd := monday.Date()
	from := time.Date(y, m, dd, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 6)
	switch {
	case from.Year() != to.Year():
		return from.Format("2 Jan 2006") + " to " + to.Format("2 Jan 2006")
	case from.Month() != to.Month():
		return from.Format("2 Jan") + " to " + to.Format("2 Jan 2006")
	}
	return strconv.Itoa(from.Day()) + " to " + to.Format("2 Jan 2006")
}

// percent is part/whole as a percentage with one decimal, rounded half up: "88.9%".
func percent(part, whole int64) string {
	tenths := (part*1000 + whole/2) / whole
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10) + "%"
}

// countOf is "1 suggestion" or "5,612 agent actions".
func countOf(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return thousands(n) + " " + many
}

// thousands writes n with comma thousands separators: 5612 is "5,612".
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	sign := ""
	if s[0] == '-' {
		sign, s = "-", s[1:]
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return sign + b.String()
}

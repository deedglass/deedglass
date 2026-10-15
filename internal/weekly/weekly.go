// Package weekly is "What your agents did this week" (S52 §3.1, ADR-101): the summary of one org's
// week or month, built from its own records. It is free and pure: Build turns Facts a store read
// into a Summary, and the renderers (Markdown, HTML, JSON, the Monday email) word it. A Summary
// holds integers, ids, dates and the names of the org's own agents, tools and rules, plus each
// rule's own reason: never action arguments, results, row values, emails or secrets (the canary
// test in pgsource reads every rendering).
//
// The Monday job (monday.go) sends the week to the org's owners through the mailer the server
// already uses; deedglass.com sees nothing.
package weekly

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/caught"
)

// Object and version of the summary's JSON.
const (
	Object  = "weekly_summary"
	Version = 1
)

// Period kinds.
const (
	KindWeek  = "week"
	KindMonth = "month"
)

// Limits.
const (
	TopDenials   = 3  // denials by rule: the top 3
	TopAgents    = 8  // actions by agent shown
	TopTools     = 8  // actions by tool shown
	CaughtShown  = 10 // caught items listed; CaughtTotal counts them all
	MaxRuleChars = 200
)

// Period is a week or a month in the org's time zone.
type Period struct {
	Kind     string    `json:"kind"` // week | month
	Key      string    `json:"key"`  // 2026-W43 | 2026-10
	From     time.Time `json:"from"` // inclusive, UTC
	To       time.Time `json:"to"`   // exclusive, UTC
	Timezone string    `json:"timezone"`
	Label    string    `json:"label"` // "19 to 25 Oct 2026" | "October 2026"
}

// ErrPeriod is a period that can't be read.
var ErrPeriod = errors.New("weekly: a period is a week like 2026-W43 or a month like 2026-10")

// ParsePeriod reads "2026-W43" (an ISO week) or "2026-10" (a month) in loc.
func ParsePeriod(s string, loc *time.Location) (Period, error) {
	if loc == nil {
		loc = time.UTC
	}
	s = strings.TrimSpace(s)
	if y, w, ok := strings.Cut(s, "-W"); ok {
		year, err1 := strconv.Atoi(y)
		week, err2 := strconv.Atoi(w)
		if err1 != nil || err2 != nil || len(y) != 4 || len(w) != 2 || week < 1 || week > 53 {
			return Period{}, ErrPeriod
		}
		mon := isoMonday(year, week, loc)
		if gy, gw := mon.ISOWeek(); gy != year || gw != week {
			return Period{}, ErrPeriod // week 53 of a 52-week year
		}
		return WeekOf(mon, loc), nil
	}
	if y, m, ok := strings.Cut(s, "-"); ok && len(y) == 4 && len(m) == 2 {
		year, err1 := strconv.Atoi(y)
		month, err2 := strconv.Atoi(m)
		if err1 != nil || err2 != nil || month < 1 || month > 12 || year < 2000 || year > 2999 {
			return Period{}, ErrPeriod
		}
		return MonthOf(time.Date(year, time.Month(month), 1, 0, 0, 0, 0, loc), loc), nil
	}
	return Period{}, ErrPeriod
}

// isoMonday is the Monday 00:00 (loc) of ISO week w of year y.
func isoMonday(y, w int, loc *time.Location) time.Time {
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, loc)
	d := (int(jan4.Weekday()) + 6) % 7
	return jan4.AddDate(0, 0, -d+7*(w-1))
}

// WeekOf is the ISO week containing t, in loc.
func WeekOf(t time.Time, loc *time.Location) Period {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	d := (int(l.Weekday()) + 6) % 7
	y, m, day := l.AddDate(0, 0, -d).Date()
	from := time.Date(y, m, day, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 7)
	iy, iw := from.ISOWeek()
	return Period{Kind: KindWeek, Key: fmt.Sprintf("%d-W%02d", iy, iw), From: from.UTC(), To: to.UTC(), Timezone: loc.String(),
		Label: weekLabel(from)}
}

// LastWeek is the last complete ISO week before now, in loc (what the Monday email covers).
func LastWeek(now time.Time, loc *time.Location) Period {
	this := WeekOf(now, loc)
	return WeekOf(this.From.Add(-time.Hour), loc)
}

// MonthOf is the calendar month containing t, in loc.
func MonthOf(t time.Time, loc *time.Location) Period {
	if loc == nil {
		loc = time.UTC
	}
	l := t.In(loc)
	from := time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 1, 0)
	return Period{Kind: KindMonth, Key: from.Format("2006-01"), From: from.UTC(), To: to.UTC(), Timezone: loc.String(),
		Label: from.Format("January 2006")}
}

// LastMonth is the last complete month before now, in loc.
func LastMonth(now time.Time, loc *time.Location) Period {
	this := MonthOf(now, loc)
	return MonthOf(this.From.Add(-time.Hour), loc)
}

// weekLabel is "13 to 19 Oct 2026", "28 Sep to 4 Oct 2026" or "29 Dec 2025 to 4 Jan 2026".
func weekLabel(monday time.Time) string {
	to := monday.AddDate(0, 0, 6)
	switch {
	case monday.Year() != to.Year():
		return monday.Format("2 Jan 2006") + " to " + to.Format("2 Jan 2006")
	case monday.Month() != to.Month():
		return monday.Format("2 Jan") + " to " + to.Format("2 Jan 2006")
	}
	return strconv.Itoa(monday.Day()) + " to " + to.Format("2 Jan 2006")
}

// ShortLabel drops the year from a week's label ("13 to 19 Oct"), for subjects and the CLI.
func (p Period) ShortLabel() string {
	if p.Kind != KindWeek {
		return p.Label
	}
	if i := strings.LastIndex(p.Label, " "); i > 0 && strings.Count(p.Label, " 20") == 1 {
		return p.Label[:i]
	}
	return p.Label
}

// Org names the org.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Count is one named total.
type Count struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// Actions are the period's actions by decision, agent and tool.
type Actions struct {
	Total    int64   `json:"total"`
	Allowed  int64   `json:"allowed"`
	Waited   int64   `json:"waited_for_a_person"`
	Blocked  int64   `json:"blocked_by_rules"`
	Other    int64   `json:"other"` // no decision yet (received, refused before the rules)
	ByAgent  []Count `json:"by_agent"`
	ByTool   []Count `json:"by_tool"`
	Agents   int     `json:"agents"` // agents with at least one action
	Previous *int64  `json:"previous"`
}

// Check is the period's Check results (the API's VERIFIED, MISMATCH, UNVERIFIABLE).
type Check struct {
	Matches     int64 `json:"matches"`
	DoesntMatch int64 `json:"doesnt_match"`
	CantCheck   int64 `json:"cant_check"`
	NotChecked  int64 `json:"not_checked"`
}

// Denial is one rule and how often it blocked.
type Denial struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// Approvals are the period's approval requests.
type Approvals struct {
	Requested     int64  `json:"requested"`
	Approved      int64  `json:"approved"`
	Denied        int64  `json:"denied"`
	Expired       int64  `json:"expired"`
	Pending       int64  `json:"pending"`
	MedianSeconds *int64 `json:"median_seconds_to_decide"`
}

// Undos are the period's undos.
type Undos struct {
	Actions  int64 `json:"actions"`
	Sessions int64 `json:"sessions"`
}

// Stops are the period's Stop events.
type Stops struct {
	Agent int64 `json:"agent"`
	All   int64 `json:"all"`
}

// Sessions are the period's watched sessions (S50).
type Sessions struct {
	Watched int64 `json:"watched"`
	Caught  int64 `json:"caught"`
}

// Chain statuses.
const (
	ChainIntact     = "intact"
	ChainBroken     = "broken"
	ChainNotChecked = "not_checked"
	ChainNoRecords  = "no_records"
)

// Chain is the record chain check over the period.
type Chain struct {
	Status      string `json:"status"`
	Checked     int64  `json:"checked"`
	FirstBroken string `json:"first_broken,omitempty"`
}

// Pack is the newest security review pack built in the period (S51), when one was.
type Pack struct {
	BuiltAt time.Time `json:"built_at"`
	Folder  string    `json:"folder"`
}

// Summary is the weekly_summary object.
type Summary struct {
	Object       string        `json:"object"`
	Version      int           `json:"version"`
	Org          Org           `json:"org"`
	Period       Period        `json:"period"`
	Actions      Actions       `json:"actions"`
	Check        Check         `json:"check"`
	Denials      []Denial      `json:"denials"`
	Approvals    Approvals     `json:"approvals"`
	Undos        Undos         `json:"undos"`
	Stops        Stops         `json:"stops"`
	Sessions     Sessions      `json:"sessions"`
	Caught       []caught.Item `json:"caught"`
	CaughtTotal  int           `json:"caught_total"`
	Chain        Chain         `json:"chain"`
	SecurityPack *Pack         `json:"security_pack"`
	GeneratedAt  time.Time     `json:"generated_at"`
}

// AgentDecision is one (agent, decision) total from the store.
type AgentDecision struct {
	AgentID, Name, Decision string // decision: allow, deny, require_approval, "" (none yet)
	Count                   int64
}

// Facts are what a store read for one org and period. Build words nothing; it only sums, sorts and
// trims.
type Facts struct {
	Org       Org
	Period    Period
	Now       time.Time
	Decisions []AgentDecision
	Tools     []Count
	Verdicts  map[string]int64 // VERIFIED, MISMATCH, UNVERIFIABLE; "" for not checked
	Previous  *int64
	Denials   []Denial // by count, any length
	Approvals Approvals
	Undos     Undos
	Stops     Stops
	Sessions  Sessions
	// SessionCaught are the caught items sealed into the period's watched sessions (S50 summaries).
	SessionCaught []caught.Item
	// Actions are the period's actions the action detectors look at (a Doesn't match, an undo).
	Actions []caught.Action
	Chain   Chain
	Pack    *Pack
}

// Build turns facts into the summary.
func Build(f Facts) Summary {
	s := Summary{Object: Object, Version: Version, Org: f.Org, Period: f.Period, GeneratedAt: f.Now.UTC(),
		Denials: []Denial{}, Caught: []caught.Item{}, Undos: f.Undos, Stops: f.Stops, Sessions: f.Sessions,
		Approvals: f.Approvals, Chain: f.Chain, SecurityPack: f.Pack}
	if s.Chain.Status == "" {
		s.Chain.Status = ChainNotChecked
	}
	agents := map[string]*Count{}
	for _, d := range f.Decisions {
		s.Actions.Total += d.Count
		switch d.Decision {
		case "allow":
			s.Actions.Allowed += d.Count
		case "require_approval":
			s.Actions.Waited += d.Count
		case "deny":
			s.Actions.Blocked += d.Count
		default:
			s.Actions.Other += d.Count
		}
		c := agents[d.AgentID]
		if c == nil {
			c = &Count{ID: d.AgentID, Name: d.Name}
			agents[d.AgentID] = c
		}
		c.Count += d.Count
	}
	for _, c := range agents {
		if c.Count > 0 {
			s.Actions.ByAgent = append(s.Actions.ByAgent, *c)
		}
	}
	s.Actions.Agents = len(s.Actions.ByAgent)
	s.Actions.ByAgent = topCounts(s.Actions.ByAgent, TopAgents)
	s.Actions.ByTool = topCounts(append([]Count(nil), f.Tools...), TopTools)
	s.Actions.Previous = f.Previous
	s.Check = Check{Matches: f.Verdicts["VERIFIED"], DoesntMatch: f.Verdicts["MISMATCH"], CantCheck: f.Verdicts["UNVERIFIABLE"]}
	s.Check.NotChecked = s.Actions.Total - s.Check.Matches - s.Check.DoesntMatch - s.Check.CantCheck
	if s.Check.NotChecked < 0 {
		s.Check.NotChecked = 0
	}
	den := append([]Denial(nil), f.Denials...)
	sort.SliceStable(den, func(i, j int) bool {
		if den[i].Count != den[j].Count {
			return den[i].Count > den[j].Count
		}
		return den[i].Rule < den[j].Rule
	})
	for _, d := range den {
		if len(s.Denials) == TopDenials {
			break
		}
		if d.Count <= 0 {
			continue
		}
		d.Rule, d.Reason = trimRunes(oneLine(d.Rule), MaxRuleChars), trimRunes(oneLine(d.Reason), MaxRuleChars)
		s.Denials = append(s.Denials, d)
	}
	items := append([]caught.Item(nil), f.SessionCaught...)
	items = append(items, caught.Actions(f.Actions)...)
	s.CaughtTotal = len(items)
	if len(items) > CaughtShown {
		items = items[:CaughtShown]
	}
	if len(items) > 0 {
		s.Caught = items
	}
	return s
}

func topCounts(cs []Count, n int) []Count {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Count != cs[j].Count {
			return cs[i].Count > cs[j].Count
		}
		return cs[i].Name < cs[j].Name
	})
	if len(cs) > n {
		cs = cs[:n]
	}
	if cs == nil {
		return []Count{}
	}
	return cs
}

// oneLine replaces control characters with spaces, so a name can't break a line or a table.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
}

func trimRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// Median is the median of xs (the lower middle for an even count), or nil when empty.
func Median(xs []int64) *int64 {
	if len(xs) == 0 {
		return nil
	}
	c := append([]int64(nil), xs...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	m := c[(len(c)-1)/2]
	return &m
}

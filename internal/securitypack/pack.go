package securitypack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Object names and the format version.
const (
	ObjectPack      = "security_pack"
	ObjectReadiness = "security_pack_readiness"
	PackVersion     = 1
)

// Summary counts answers by status.
type Summary struct {
	InPlace       int `json:"in_place"`
	Partly        int `json:"partly"`
	NotSetUp      int `json:"not_set_up"`
	NotApplicable int `json:"not_applicable"`
	CantTell      int `json:"cant_tell"`
}

func (s *Summary) add(st Status) {
	switch st {
	case InPlace:
		s.InPlace++
	case Partly:
		s.Partly++
	case NotSetUp:
		s.NotSetUp++
	case NotApplicable:
		s.NotApplicable++
	case CantTell:
		s.CantTell++
	}
}

// Line is the summary as people read it: "17 in place · 4 partly · 2 not set up · 1 not applicable".
// Zero counts are left out except "in place".
func (s Summary) Line() string {
	parts := []string{formatCount(int64(s.InPlace)) + " in place"}
	add := func(n int, w string) {
		if n > 0 {
			parts = append(parts, formatCount(int64(n))+" "+w)
		}
	}
	add(s.Partly, "partly")
	add(s.NotSetUp, "not set up")
	add(s.NotApplicable, "not applicable")
	add(s.CantTell, "can't tell")
	return strings.Join(parts, " · ")
}

// PeriodJSON is the period as RFC 3339 UTC strings.
type PeriodJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Answer is one question's computed answer.
type Answer struct {
	ID       string   `json:"id"`
	Group    string   `json:"group"`
	Question string   `json:"question"`
	Status   Status   `json:"status"`
	Answer   string   `json:"answer"`
	Evidence Evidence `json:"evidence"`
	Check    string   `json:"check"`
	Refs     []Ref    `json:"refs"`
}

// Chain is the deed chain's state when the pack was built.
type Chain struct {
	Intact bool   `json:"intact"`
	Head   string `json:"head"`
}

// RecordsRef describes records.tar.gz, or is null when the pack carries no records.
type RecordsRef struct {
	File   string `json:"file"`
	Deeds  int64  `json:"deeds"`
	SHA256 string `json:"sha256"`
}

// Pack is answers.json: security_pack v1, a closed schema (ParsePack refuses unknown fields).
type Pack struct {
	Object           string      `json:"object"`
	V                int         `json:"v"`
	Org              string      `json:"org"`
	OrgName          string      `json:"org_name"`
	Period           PeriodJSON  `json:"period"`
	GeneratedAt      string      `json:"generated_at"`
	DeedglassVersion string      `json:"deedglass_version"`
	Summary          Summary     `json:"summary"`
	Answers          []Answer    `json:"answers"`
	Chain            Chain       `json:"chain"`
	Records          *RecordsRef `json:"records"`
}

// Options are what Build needs beyond the facts.
type Options struct {
	// Records is records.tar.gz's description when the pack carries it.
	Records *RecordsRef
	// Aliases, when set, replace agent, connector and rule names after the rules were probed
	// with the real ones (connector-scoped rules see their real connector).
	Aliases *Aliases
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Build computes every answer from f. It never fails because of what the facts say (that is a
// status); it fails only when the bank or a template is broken, which the tests catch.
func Build(ctx context.Context, f *Facts, o Options) (*Pack, error) {
	b, err := Questions()
	if err != nil {
		return nil, err
	}
	e := &env{f: f}
	if v := f.Rules.Enforce; v != nil && f.Errors[CollectorRules] == "" {
		rf, err := deriveRules(ctx, v, toolName(f, "postgres", "execute"), toolName(f, "stripe", "create_refund"))
		if err != nil {
			f.Fail(CollectorRules, err.Error())
		} else {
			e.rf = rf
		}
	}
	if o.Aliases != nil {
		o.Aliases.Apply(f)
	}
	p := &Pack{Object: ObjectPack, V: PackVersion, Org: f.Org.ID, OrgName: f.Org.Name,
		Period:      PeriodJSON{From: rfc3339(f.Period.From), To: rfc3339(f.Period.To)},
		GeneratedAt: rfc3339(f.Now), DeedglassVersion: f.Version,
		Chain:   Chain{Intact: f.Errors[CollectorRecords] == "" && f.Records.ChainIntact, Head: f.Records.ChainHead},
		Records: o.Records, Answers: []Answer{}}
	for _, q := range b.Questions {
		a, err := answer(q, e)
		if err != nil {
			return nil, err
		}
		p.Summary.add(a.Status)
		p.Answers = append(p.Answers, a)
	}
	return p, nil
}

// toolName is "<connector>.<tool>" for the org's first connector of type t, or "<t>.<tool>".
func toolName(f *Facts, t, tool string) string {
	names := []string{}
	for _, c := range f.Connectors {
		if c.Type == t {
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0] + "." + tool
	}
	return t + "." + tool
}

// missing is the first collector q needs that couldn't be read, with its reason.
func missing(q Question, f *Facts) (string, string) {
	for _, n := range q.Needs {
		if r, ok := f.Errors[n]; ok {
			return n, r
		}
	}
	return "", ""
}

func answer(q Question, e *env) (Answer, error) {
	a := Answer{ID: q.ID, Group: q.Group, Question: q.Question, Check: q.Check, Refs: q.Refs, Evidence: Evidence{}}
	if a.Refs == nil {
		a.Refs = []Ref{}
	}
	if c, reason := missing(q, e.f); c != "" {
		a.Status = CantTell
		a.Evidence = Evidence{"unreadable": c}
		a.Answer = CantTellAnswer(c, reason)
		return a, nil
	}
	st, ev := rules[q.ID](e)
	a.Status, a.Evidence = st, ev
	if st == CantTell {
		a.Answer = CantTellAnswer(q.Needs[0], "the facts it read don't fit what Deedglass writes")
		return a, nil
	}
	tpl, ok := q.Answer[st]
	if !ok {
		return a, fmt.Errorf("securitypack: %s has no answer for %s", q.ID, st)
	}
	text, err := fill(tpl, ev)
	if err != nil {
		return a, fmt.Errorf("securitypack: %s (%s): %w", q.ID, st, err)
	}
	a.Answer = text
	return a, nil
}

// CantTellAnswer is a Can't tell answer: what couldn't be read and why, never a guess.
func CantTellAnswer(collector, reason string) string {
	reason = strings.TrimSuffix(strings.TrimSpace(reason), ".")
	return fmt.Sprintf("Deedglass couldn't read the %s (%s), so this question is left unanswered rather than guessed.", collector, reason)
}

// Fix is what to do about an answer, for the readiness view ("" when there is nothing to do).
func Fix(a Answer) (string, error) {
	b, err := Questions()
	if err != nil {
		return "", err
	}
	if a.Status == CantTell {
		return "Check that deedglass serve can read its database, then run the readiness check again.", nil
	}
	for _, q := range b.Questions {
		if q.ID == a.ID {
			tpl, ok := q.Fix[a.Status]
			if !ok {
				return "", nil
			}
			return fill(tpl, a.Evidence)
		}
	}
	return "", fmt.Errorf("securitypack: unknown question %q", a.ID)
}

// ReadinessRow is one question in the free readiness view: status and fix, no evidence.
type ReadinessRow struct {
	ID         string `json:"id"`
	Group      string `json:"group"`
	GroupTitle string `json:"group_title"`
	Question   string `json:"question"`
	Status     Status `json:"status"`
	Fix        string `json:"fix"`
}

// Readiness is the free view: the same questions with statuses and fixes, and no document.
type Readiness struct {
	Object  string         `json:"object"`
	Org     string         `json:"org"`
	OrgName string         `json:"org_name"`
	Period  PeriodJSON     `json:"period"`
	Summary Summary        `json:"summary"`
	Rows    []ReadinessRow `json:"rows"`
}

// ReadinessOf is the readiness view of a pack.
func ReadinessOf(p *Pack) (*Readiness, error) {
	b, err := Questions()
	if err != nil {
		return nil, err
	}
	r := &Readiness{Object: ObjectReadiness, Org: p.Org, OrgName: p.OrgName, Period: p.Period, Summary: p.Summary, Rows: []ReadinessRow{}}
	for _, a := range p.Answers {
		fix, err := Fix(a)
		if err != nil {
			return nil, err
		}
		r.Rows = append(r.Rows, ReadinessRow{ID: a.ID, Group: a.Group, GroupTitle: b.GroupTitle(a.Group), Question: a.Question, Status: a.Status, Fix: fix})
	}
	return r, nil
}

// ParsePack reads answers.json strictly: unknown fields, a wrong object or version, trailing
// data, an unknown status or a summary that doesn't add up are refused.
func ParsePack(r io.Reader) (*Pack, error) {
	dec := json.NewDecoder(io.LimitReader(r, MaxAnswersBytes+1))
	dec.DisallowUnknownFields()
	var p Pack
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("answers.json isn't a security_pack v1: %w", err)
	}
	if dec.More() {
		return nil, errors.New("answers.json has data after the pack")
	}
	if p.Object != ObjectPack || p.V != PackVersion {
		return nil, fmt.Errorf("answers.json is %q v%d, not security_pack v1", p.Object, p.V)
	}
	var s Summary
	for _, a := range p.Answers {
		if a.Status.Word() == string(a.Status) {
			return nil, fmt.Errorf("answers.json: %s has an unknown status %q", a.ID, a.Status)
		}
		s.add(a.Status)
	}
	if s != p.Summary {
		return nil, errors.New("answers.json: the summary doesn't match the answers")
	}
	return &p, nil
}

// MaxAnswersBytes bounds answers.json.
const MaxAnswersBytes = 1 << 20

// MarshalPack is answers.json's bytes: indented, with a final newline, the same on every machine.
func MarshalPack(p *Pack) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

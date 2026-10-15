package securitypack

import (
	"fmt"
	"strings"
	"time"
)

// EvidenceItem is one evidence field as a document shows it.
type EvidenceItem struct {
	Key   string
	Label string
	Value string
}

// EvidenceItems lists an answer's evidence in the bank's order, skipping empty values.
func EvidenceItems(a Answer) []EvidenceItem {
	b, err := Questions()
	if err != nil {
		return nil
	}
	var order []string
	for _, q := range b.Questions {
		if q.ID == a.ID {
			order = q.Evidence
		}
	}
	if a.Status == CantTell {
		order = []string{"unreadable"}
	}
	var out []EvidenceItem
	for _, k := range order {
		v, ok := a.Evidence[k]
		if !ok {
			continue
		}
		s := display(k, v)
		if s == "" {
			continue
		}
		out = append(out, EvidenceItem{Key: k, Label: strings.ReplaceAll(strings.TrimSuffix(k, "_on"), "_", " "), Value: s})
	}
	return out
}

// EvidenceLine is the evidence as one line: "label value · label value".
func EvidenceLine(a Answer) string {
	items := EvidenceItems(a)
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it.Label+" "+it.Value)
	}
	return strings.Join(parts, " · ")
}

// RefLine is the refs as a reviewer reads them: "OWASP LLM06 Excessive Agency · NIST AI RMF MANAGE 2.4".
func RefLine(refs []Ref) string {
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		switch r.Framework {
		case "OWASP LLM 2025":
			parts = append(parts, strings.TrimSpace("OWASP "+r.ID+" "+r.Title))
		case "NIST AI RMF 1.0":
			parts = append(parts, "NIST AI RMF "+r.ID)
		default:
			parts = append(parts, r.Framework+" "+r.ID)
		}
	}
	return strings.Join(parts, " · ")
}

// GroupAnswers is one group with its answers, numbered from 1.
type GroupAnswers struct {
	Number  int
	ID      string
	Title   string
	Answers []Answer
}

// Groups is the pack's answers by group, in the bank's order.
func (p *Pack) Groups() []GroupAnswers {
	b, err := Questions()
	if err != nil {
		return nil
	}
	var out []GroupAnswers
	for i, g := range b.Groups {
		ga := GroupAnswers{Number: i + 1, ID: g.ID, Title: g.Title}
		for _, a := range p.Answers {
			if a.Group == g.ID {
				ga.Answers = append(ga.Answers, a)
			}
		}
		out = append(out, ga)
	}
	return out
}

// SummaryItems is the summary as (count, status) pairs with zero counts left out.
func (s Summary) Items() []struct {
	N      int
	Status Status
} {
	var out []struct {
		N      int
		Status Status
	}
	for _, st := range Statuses {
		n := map[Status]int{InPlace: s.InPlace, Partly: s.Partly, NotSetUp: s.NotSetUp, NotApplicable: s.NotApplicable, CantTell: s.CantTell}[st]
		if n > 0 || st == InPlace {
			out = append(out, struct {
				N      int
				Status Status
			}{n, st})
		}
	}
	return out
}

// Count is n with thousands separators.
func Count(n int64) string { return formatCount(n) }

// Short is the first 12 hex digits of a sha256:… digest.
func Short(d string) string { return short(d) }

// PeriodWords is "20 July to 18 October 2026" (both dates inclusive as a person reads them).
func PeriodWords(p PeriodJSON) string {
	from, to := parseRFC(p.From), parseRFC(p.To)
	if from.IsZero() || to.IsZero() {
		return p.From + " to " + p.To
	}
	if from.Year() == to.Year() {
		return fmt.Sprintf("%d %s to %d %s %d", from.Day(), from.Month(), to.Day(), to.Month(), to.Year())
	}
	return fmt.Sprintf("%d %s %d to %d %s %d", from.Day(), from.Month(), from.Year(), to.Day(), to.Month(), to.Year())
}

func parseRFC(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

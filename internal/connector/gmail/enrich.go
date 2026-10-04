package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
)

var reDraftID = regexp.MustCompile(patDraftID)

func matchDraftID(s string) bool { return reDraftID.MatchString(s) }

// Facts is request.email (policy.EmailFacts has the same JSON).
type Facts struct {
	RecipientCount  int64    `json:"recipient_count"`
	Domains         []string `json:"domains"`
	ExternalDomains []string `json:"external_domains"`
	HasExternal     bool     `json:"has_external"`
	HasBcc          bool     `json:"has_bcc"`
	IsReply         bool     `json:"is_reply"`
	Enriched        bool     `json:"enriched"`
}

func (f Facts) asMap() map[string]any {
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// facts reads the recipients without any call: domains, which are outside internal_domains.
func facts(a args, internal []string) Facts {
	f := Facts{Domains: []string{}, ExternalDomains: []string{}, HasBcc: len(a.Bcc) > 0, IsReply: a.InReplyTo != "" || a.ThreadID != ""}
	all := append(append(append([]string{}, a.To...), a.Cc...), a.Bcc...)
	for _, s := range all {
		ad, err := mail.ParseAddress(s)
		if err != nil {
			return Facts{Domains: []string{}, ExternalDomains: []string{}}
		}
		f.RecipientCount++
		_, d, _ := strings.Cut(strings.ToLower(ad.Address), "@")
		if !slices.Contains(f.Domains, d) {
			f.Domains = append(f.Domains, d)
		}
		if !slices.Contains(internal, d) && !slices.Contains(f.ExternalDomains, d) {
			f.ExternalDomains = append(f.ExternalDomains, d)
		}
	}
	slices.Sort(f.Domains)
	slices.Sort(f.ExternalDomains)
	f.HasExternal = len(f.ExternalDomains) > 0
	f.Enriched = true
	return f
}

// Enrich implements connector.Enricher: {"email": {…}} for drafts, from the arguments and the
// config (no call to Google).
func (c *Connector) Enrich(_ context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	if t.Name != ToolCreateDraft && t.Name != ToolUpdateDraft {
		return nil, nil
	}
	st, err := parse(inst)
	if err != nil {
		return map[string]any{"email": Facts{Domains: []string{}, ExternalDomains: []string{}}.asMap()}, nil
	}
	a, err := decodeArgs(raw)
	if err != nil {
		return map[string]any{"email": Facts{Domains: []string{}, ExternalDomains: []string{}}.asMap()}, nil
	}
	return map[string]any{"email": facts(a, st.cfg.InternalDomains).asMap()}, nil
}

// Summarize implements connector.Summarizer: "support-bot wants to draft an email to 2 people, 1
// outside acme.com: "Re: Order 4121"." The subject is the one agent-written part, quoted.
func (c *Connector) Summarize(in connector.SummaryInput) connector.Summary {
	if in.Tool.Name != ToolCreateDraft && in.Tool.Name != ToolUpdateDraft {
		if _, t, _ := connector.Split(in.Qualified); t != ToolCreateDraft && t != ToolUpdateDraft {
			return nil
		}
	}
	a, err := decodeArgs(in.Args)
	if err != nil || strings.ContainsAny(a.Subject, "\r\n") {
		return nil
	}
	var f Facts
	if m, ok := in.Facts["email"].(map[string]any); ok {
		b, _ := json.Marshal(m)
		_ = json.Unmarshal(b, &f)
	}
	if !f.Enriched {
		f = facts(a, nil)
		f.ExternalDomains = nil
	}
	agent := in.Agent
	if agent == "" {
		agent = "An agent"
	}
	n := f.RecipientCount
	who := fmt.Sprintf("%d %s", n, map[bool]string{true: "person", false: "people"}[n == 1])
	s := connector.Summary{{Text: agent, Style: connector.SummaryStrong}, {Text: " wants to draft an email to " + who}}
	internal := internalOf(f)
	if len(f.ExternalDomains) > 0 && internal != "" {
		s = append(s, connector.SummaryPart{Text: fmt.Sprintf(", %d outside %s", countExternal(a, f), internal)})
	}
	subj := []rune(a.Subject)
	if len(subj) > 80 {
		subj = append(subj[:79], '…')
	}
	return append(s, connector.SummaryPart{Text: ": \""}, connector.SummaryPart{Text: string(subj), Style: connector.SummaryStrong}, connector.SummaryPart{Text: "\"."})
}

// internalOf is the first internal domain among the recipients' domains, for the sentence.
func internalOf(f Facts) string {
	for _, d := range f.Domains {
		if !slices.Contains(f.ExternalDomains, d) {
			return d
		}
	}
	return ""
}

func countExternal(a args, f Facts) int {
	n := 0
	for _, s := range append(append(append([]string{}, a.To...), a.Cc...), a.Bcc...) {
		if ad, err := mail.ParseAddress(s); err == nil {
			_, d, _ := strings.Cut(strings.ToLower(ad.Address), "@")
			if slices.Contains(f.ExternalDomains, d) {
				n++
			}
		}
	}
	return n
}

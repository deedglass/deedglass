package learn

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Optional wording (S28 §3.7). Every suggestion is written first by Deedglass's own templates,
// complete on their own. When an org turns llm_phrasing on and the operator set
// DEEDGLASS_ANTHROPIC_API_KEY, a Phraser may reword the title and rationale from the allow-listed
// PhrasingInput. It runs after the suggestion is stored, never blocks it, runs one call per org
// at a time and at most PhrasingPerDay a day, and stops at the org's monthly cap. A reply that
// fails ValidatePhrasing is thrown away and the template stands.

// PhrasingPerDay is the most phrasing calls one org makes in a day.
const PhrasingPerDay = 20

// PhraseResult is a phraser's answer.
type PhraseResult struct {
	Title, Rationale string
	Model            string
	InputTokens      int
	OutputTokens     int
	Duration         time.Duration
}

// Phraser rewords one suggestion. An error means "keep the template"; its PhraseError reason is
// stored as phrasing.reason.
type Phraser interface {
	Phrase(ctx context.Context, in *PhrasingInput) (*PhraseResult, error)
	Model() string
}

// PhraseError is a phrasing failure with its stored reason (refusal, max_tokens, timeout, http_429, …).
type PhraseError struct {
	Reason string
	Result *PhraseResult // set when tokens were spent (they count against the cap)
}

func (e *PhraseError) Error() string { return "learn: phrasing: " + e.Reason }

// TemplatePhraser keeps the template wording (the phraser when no key is set).
type TemplatePhraser struct{}

// Phrase returns the template text unchanged.
func (TemplatePhraser) Phrase(_ context.Context, in *PhrasingInput) (*PhraseResult, error) {
	return &PhraseResult{Title: in.TemplateTitle, Rationale: in.TemplateRationale, Model: "template"}, nil
}

// Model is "template".
func (TemplatePhraser) Model() string { return "template" }

// phraseLater rewords the stored rows in the background when the org allows it.
func (s *Suggester) phraseLater(org ids.OrgID, info OrgInfo, rows []NewSuggestion, inserted map[string]bool) {
	if s.Phraser == nil || !info.Settings.LLMPhrasing {
		return
	}
	if _, ok := s.Phraser.(TemplatePhraser); ok {
		return
	}
	var todo []NewSuggestion
	for _, r := range rows {
		if inserted[r.ID] && r.PhrasingInput != nil {
			todo = append(todo, r)
		}
	}
	if len(todo) == 0 {
		return
	}
	s.phrase.Add(1)
	go func() {
		defer s.phrase.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.PhraseRows(ctx, org, info.Settings, todo)
	}()
}

// PhraseRows phrases rows one at a time (one call per org at a time: the caller runs it once per
// pass), checking the daily count and the monthly cap before each call.
func (s *Suggester) PhraseRows(ctx context.Context, org ids.OrgID, set Settings, rows []NewSuggestion) {
	m, _ := s.phrasing.LoadOrStore(org.String(), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	for _, r := range rows {
		now := s.now()
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		day := now.Truncate(24 * time.Hour)
		spent, today, err := s.Store.PhrasingUsage(ctx, org, month, day)
		if err != nil {
			s.log().Warn("learn: phrasing usage unreadable; keeping the template", "org_id", org.String(), "err", err)
			return
		}
		if today >= PhrasingPerDay || spent >= int64(set.LLMMonthlyCapCents)*10_000 {
			s.log().Info("insights.phrase", "org_id", org.String(), "outcome", "cap_reached", "calls_today", today, "cost_usd_micros_month", spent)
			return
		}
		p := s.phraseOne(ctx, r.PhrasingInput)
		at := s.now().Format(time.RFC3339)
		p.At = &at
		var title, rationale *string
		if p.Source == "claude" {
			t, ra := r.PhrasingInput.resolve(p.TemplateTitle), r.PhrasingInput.resolve(p.TemplateReason)
			title, rationale = &t, &ra
			p.TemplateTitle, p.TemplateReason = r.Title, ""
		} else {
			p.TemplateTitle = ""
		}
		if err := s.Store.SetPhrasing(ctx, org, r.ID, title, rationale, p); err != nil {
			s.log().Warn("learn: storing phrasing failed", "org_id", org.String(), "suggestion", r.ID, "err", err)
		}
	}
}

// phraseOne calls the phraser and validates the reply. On success TemplateTitle and TemplateReason
// carry the reply (with placeholders, resolved by the caller).
func (s *Suggester) phraseOne(ctx context.Context, in *PhrasingInput) Phrasing {
	t0 := time.Now()
	res, err := s.Phraser.Phrase(ctx, in)
	p := Phrasing{Source: "template", Model: s.Phraser.Model()}
	var pe *PhraseError
	switch {
	case errors.As(err, &pe):
		p.Reason = pe.Reason
		res = pe.Result
	case err != nil:
		p.Reason = "error"
	}
	if res != nil {
		p.InputTokens, p.OutputTokens = res.InputTokens, res.OutputTokens
		p.CostUSDMicros = CostMicros(s.Phraser.Model(), res.InputTokens, res.OutputTokens)
	}
	p.DurationMS = time.Since(t0).Milliseconds()
	if err == nil {
		if ok, why := ValidatePhrasing(in, res.Title, res.Rationale); !ok {
			p.Reason = why
		} else {
			p.Source, p.TemplateTitle, p.TemplateReason = "claude", res.Title, res.Rationale
		}
	}
	s.log().Info("insights.phrase", "model", p.Model, "input_tokens", p.InputTokens, "output_tokens", p.OutputTokens,
		"cost_usd_micros", p.CostUSDMicros, "duration_ms", p.DurationMS, "outcome", outcomeOf(p))
	return p
}

func outcomeOf(p Phrasing) string {
	if p.Source == "claude" {
		return "claude"
	}
	return "template:" + p.Reason
}

// resolve puts the org's names back into a reply.
func (in *PhrasingInput) resolve(s string) string {
	return placeholderRE.ReplaceAllStringFunc(s, func(p string) string {
		if v, ok := in.values[p]; ok {
			return v
		}
		return p
	})
}

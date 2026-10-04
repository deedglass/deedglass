package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/policy"
)

// S28: suggestions. The PolicyWriter is the one door from Insights into policies, and it is
// shadow-only by construction: the mode below is a constant, and learn has no other way to
// activate anything. Promoting a shadow to enforce stays a person's act through the policy routes.

// suggestedPolicies implements learn.PolicyWriter over S12's policy.Service.
type suggestedPolicies struct{ svc *policy.Service }

var _ learn.PolicyWriter = suggestedPolicies{}

func policyCallerOf(p learn.Principal) policy.Caller {
	return policy.Caller{Org: p.Org.String(), Actor: deed.Ref{Type: p.Actor.Type, ID: p.Actor.ID}, RequestID: p.RequestID}
}

// AddSuggestedVersion pushes the suggestion's source as a new version with origin suggestion (the
// existing version when the content is identical).
func (w suggestedPolicies) AddSuggestedVersion(ctx context.Context, p learn.Principal, policyID string, src []byte, sug, notes string) (*policy.Version, bool, error) {
	return w.svc.AddVersion(ctx, policyCallerOf(p), policyID, policy.PushRequest{Source: src, Notes: notes, Origin: policy.OriginSuggestion, SuggestionID: sug})
}

// ActivateShadow runs version in shadow. The mode is fixed here; it is never a parameter.
func (w suggestedPolicies) ActivateShadow(ctx context.Context, p learn.Principal, policyID string, version int, reason string) (*policy.Activation, error) {
	return w.svc.Activate(ctx, policyCallerOf(p), policyID, policy.VersionRef{Number: version},
		policy.ActivateRequest{Mode: policy.ModeShadow, Reason: reason})
}

// phraserOf is the Claude phraser when the operator set a key, else nil (template wording only:
// no client is built and no request can be made).
func phraserOf(cfg *config.Config) learn.Phraser {
	if cfg == nil || !cfg.AnthropicAPIKey.IsSet() || cfg.AnthropicAPIKey.Reveal() == "" {
		return nil
	}
	model := cfg.AnthropicModel
	if model == "" {
		model = learn.DefaultPhrasingModel
	}
	return &learn.ClaudePhraser{APIKey: cfg.AnthropicAPIKey.Reveal(), ModelID: model,
		HTTP: egress.FirstParty(egress.FirstPartyOptions{Timeout: 10 * time.Second})}
}

// wireSuggestions adds S28's suggestion service and schedule to the Insights wiring.
func (ins *insights) wireSuggestions(cfg *config.Config, pol *policies, mail learn.MailSender, log *slog.Logger) {
	ph := phraserOf(cfg)
	sug := &learn.Suggester{Store: ins.store, Engine: &learn.Engine{Src: ins.store.Inputs}, Phraser: ph, Log: log}
	var writer learn.PolicyWriter
	if pol != nil && pol.svc != nil {
		writer = suggestedPolicies{svc: pol.svc}
	}
	ins.sugSvc = &learn.SuggestionService{Store: ins.store, Suggester: sug, Writer: writer, Log: log, PhrasingKeySet: ph != nil}
	ins.svc.PhrasingKeySet = ph != nil
	base := baseURLString(cfg)
	ins.digest = &learn.Digester{Store: ins.store, Insights: ins.svc, Mail: mail, BaseURL: base, Log: log}
	ins.schedule = &learn.Scheduler{Store: ins.store, Suggestions: ins.sugSvc, Digest: ins.digest, Log: log}
}

// baseURLString is DEEDGLASS_BASE_URL as text ("" when unset).
func baseURLString(cfg *config.Config) string {
	if cfg == nil || cfg.BaseURL == nil {
		return ""
	}
	return cfg.BaseURL.String()
}

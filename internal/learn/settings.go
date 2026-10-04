package learn

import (
	"encoding/json"
)

// Settings are an org's Insights settings (S27 §3.1), stored in orgs.settings -> 'insights'. S28
// adds llm_phrasing and digest to the same object; writers merge, so keys they don't know survive.
type Settings struct {
	// AnomalyFacts lets this org's policies read anomaly.* (S27 §3.8). Off by default: then every
	// policy sees anomaly.active false, with no database work.
	AnomalyFacts bool `json:"anomaly_facts"`
	// AnomalyActiveMinutes is how long an episode stays active after its last finding (60).
	AnomalyActiveMinutes int `json:"anomaly_active_minutes"`
	// AnomalyStaleAfterMinutes: with the facts on, a detector whose last successful run is older
	// than this makes every agent anomalous (detector_stale), because unknown is not usual (15).
	AnomalyStaleAfterMinutes int `json:"anomaly_stale_after_minutes"`
	// LLMPhrasing lets Deedglass ask Claude to reword suggestions (S28 §3.7). Off by default; it
	// also needs the operator's DEEDGLASS_ANTHROPIC_API_KEY. The template wording always stands on
	// its own.
	LLMPhrasing bool `json:"llm_phrasing"`
	// LLMMonthlyCapCents caps the month's phrasing spend for the org (200 = $2.00).
	LLMMonthlyCapCents int `json:"llm_monthly_cap_cents"`
	// Digest is the weekly summary email (S28 §3.9).
	Digest DigestSettings `json:"digest"`
}

// DigestSettings says who gets the weekly digest: owners (default), admins (owners and admins) or off.
type DigestSettings struct {
	Recipients string `json:"recipients"`
}

// Digest recipients.
const (
	DigestOwners = "owners"
	DigestAdmins = "admins"
	DigestOff    = "off"
)

// Phrasing settings bounds.
const (
	DefaultLLMCapCents = 200
	MaxLLMCapCents     = 10_000
)

// Settings bounds.
const (
	DefaultActiveMinutes = 60
	DefaultStaleMinutes  = 15
	MinActiveMinutes     = 5
	MaxActiveMinutes     = 1440
	MinStaleMinutes      = 10 // two detect passes
	MaxStaleMinutes      = 240
)

// DefaultSettings are the settings of an org that never changed them.
func DefaultSettings() Settings {
	return Settings{AnomalyActiveMinutes: DefaultActiveMinutes, AnomalyStaleAfterMinutes: DefaultStaleMinutes,
		LLMMonthlyCapCents: DefaultLLMCapCents, Digest: DigestSettings{Recipients: DigestOwners}}
}

// ParseSettings reads the insights object out of orgs.settings (the whole column), with defaults
// for anything missing or out of range. It never fails: a bad value reads as the default.
func ParseSettings(orgSettings []byte) Settings {
	s := DefaultSettings()
	var all struct {
		Insights *struct {
			AnomalyFacts             *bool `json:"anomaly_facts"`
			AnomalyActiveMinutes     *int  `json:"anomaly_active_minutes"`
			AnomalyStaleAfterMinutes *int  `json:"anomaly_stale_after_minutes"`
			LLMPhrasing              *bool `json:"llm_phrasing"`
			LLMMonthlyCapCents       *int  `json:"llm_monthly_cap_cents"`
			Digest                   *struct {
				Recipients *string `json:"recipients"`
			} `json:"digest"`
		} `json:"insights"`
	}
	if len(orgSettings) == 0 || json.Unmarshal(orgSettings, &all) != nil || all.Insights == nil {
		return s
	}
	in := all.Insights
	if in.AnomalyFacts != nil {
		s.AnomalyFacts = *in.AnomalyFacts
	}
	if v := in.AnomalyActiveMinutes; v != nil && *v >= MinActiveMinutes && *v <= MaxActiveMinutes {
		s.AnomalyActiveMinutes = *v
	}
	if v := in.AnomalyStaleAfterMinutes; v != nil && *v >= MinStaleMinutes && *v <= MaxStaleMinutes {
		s.AnomalyStaleAfterMinutes = *v
	}
	if in.LLMPhrasing != nil {
		s.LLMPhrasing = *in.LLMPhrasing
	}
	if v := in.LLMMonthlyCapCents; v != nil && *v >= 0 && *v <= MaxLLMCapCents {
		s.LLMMonthlyCapCents = *v
	}
	if in.Digest != nil && in.Digest.Recipients != nil && validRecipients(*in.Digest.Recipients) {
		s.Digest.Recipients = *in.Digest.Recipients
	}
	return s
}

func validRecipients(r string) bool { return r == DigestOwners || r == DigestAdmins || r == DigestOff }

// SettingsPatch is PATCH /v1/insights/settings: the fields sent, and the reason sealed in the deed.
type SettingsPatch struct {
	AnomalyFacts             *bool `json:"anomaly_facts"`
	AnomalyActiveMinutes     *int  `json:"anomaly_active_minutes"`
	AnomalyStaleAfterMinutes *int  `json:"anomaly_stale_after_minutes"`
	LLMPhrasing              *bool `json:"llm_phrasing"`
	LLMMonthlyCapCents       *int  `json:"llm_monthly_cap_cents"`
	Digest                   *struct {
		Recipients *string `json:"recipients"`
	} `json:"digest"`
	Reason string `json:"reason"`
}

// Apply returns s with the patch applied.
func (p SettingsPatch) Apply(s Settings) Settings {
	if p.AnomalyFacts != nil {
		s.AnomalyFacts = *p.AnomalyFacts
	}
	if p.AnomalyActiveMinutes != nil {
		s.AnomalyActiveMinutes = *p.AnomalyActiveMinutes
	}
	if p.AnomalyStaleAfterMinutes != nil {
		s.AnomalyStaleAfterMinutes = *p.AnomalyStaleAfterMinutes
	}
	if p.LLMPhrasing != nil {
		s.LLMPhrasing = *p.LLMPhrasing
	}
	if p.LLMMonthlyCapCents != nil {
		s.LLMMonthlyCapCents = *p.LLMMonthlyCapCents
	}
	if p.Digest != nil && p.Digest.Recipients != nil {
		s.Digest.Recipients = *p.Digest.Recipients
	}
	return s
}

// SettingsObject is the insights_settings object.
type SettingsObject struct {
	Object string `json:"object"` // insights_settings
	Settings
	// ActivePolicyReadsAnomaly: the enforced (or shadow) policy reads anomaly.* (policy.ReadsAnomaly).
	ActivePolicyReadsAnomaly bool    `json:"active_policy_reads_anomaly"`
	Notice                   *string `json:"notice"` // the opt-in-off sentence when a policy reads anomalies but the facts are off
	// PhrasingAvailable: the operator set DEEDGLASS_ANTHROPIC_API_KEY, so llm_phrasing can be on.
	PhrasingAvailable bool `json:"phrasing_available"`
	// Simulated: the org's history came from deedglass dev simulate (the Console shows a banner).
	Simulated bool    `json:"simulated"`
	Deed      *string `json:"deed,omitempty"`
}

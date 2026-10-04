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
}

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
	return Settings{AnomalyActiveMinutes: DefaultActiveMinutes, AnomalyStaleAfterMinutes: DefaultStaleMinutes}
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
	return s
}

// SettingsPatch is PATCH /v1/insights/settings: the fields sent, and the reason sealed in the deed.
type SettingsPatch struct {
	AnomalyFacts             *bool  `json:"anomaly_facts"`
	AnomalyActiveMinutes     *int   `json:"anomaly_active_minutes"`
	AnomalyStaleAfterMinutes *int   `json:"anomaly_stale_after_minutes"`
	Reason                   string `json:"reason"`
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
	return s
}

// SettingsObject is the insights_settings object.
type SettingsObject struct {
	Object string `json:"object"` // insights_settings
	Settings
	// ActivePolicyReadsAnomaly: the enforced (or shadow) policy reads anomaly.* (policy.ReadsAnomaly).
	ActivePolicyReadsAnomaly bool    `json:"active_policy_reads_anomaly"`
	Notice                   *string `json:"notice"` // the opt-in-off sentence when a policy reads anomalies but the facts are off
	Deed                     *string `json:"deed,omitempty"`
}

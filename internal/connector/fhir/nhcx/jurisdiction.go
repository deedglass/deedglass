package nhcx

import (
	"fmt"
	"strings"
)

// Supported jurisdiction identifiers.
const (
	JurisdictionIndia = "india"
	JurisdictionUS    = "us"
	JurisdictionUK    = "uk"
	JurisdictionSG    = "sg"
)

// Namespace prefixes by jurisdiction.
const (
	PrefixIndiaNRCeS = "https://nrces.in/ndhm/fhir/r4/"
	PrefixIndiaABDM  = "https://abdm.gov.in/fhir/"
	PrefixUSDaVinci  = "http://hl7.org/fhir/us/davinci"
	PrefixUSCore     = "http://hl7.org/fhir/us/core"
	PrefixUKNHS      = "https://fhir.nhs.uk/"
	PrefixSGNEHR     = "https://nehr.moh.gov.sg/fhir/"
)

// AssertJurisdiction prevents multi-jurisdiction schema collision (Hazard CH-17).
// It verifies that resources routed to a regional gateway match the expected jurisdiction namespace.
func AssertJurisdiction(bundle map[string]any, targetJurisdiction string) error {
	norm := strings.ToLower(strings.TrimSpace(targetJurisdiction))
	profiles := extractAllProfiles(bundle)

	for _, p := range profiles {
		switch norm {
		case JurisdictionIndia, "nhcx", "abdm":
			if strings.HasPrefix(p, PrefixUSDaVinci) || strings.HasPrefix(p, PrefixUSCore) {
				return fmt.Errorf("CH-17 jurisdiction collision: US Da Vinci profile %q dispatched to India NHCX route", p)
			}
			if strings.HasPrefix(p, PrefixUKNHS) {
				return fmt.Errorf("CH-17 jurisdiction collision: UK NHS profile %q dispatched to India NHCX route", p)
			}
			if strings.HasPrefix(p, PrefixSGNEHR) {
				return fmt.Errorf("CH-17 jurisdiction collision: Singapore NEHR profile %q dispatched to India NHCX route", p)
			}
		case JurisdictionUS, "cms-0057-f", "davinci":
			if strings.HasPrefix(p, PrefixIndiaNRCeS) || strings.HasPrefix(p, PrefixIndiaABDM) {
				return fmt.Errorf("CH-17 jurisdiction collision: India NRCeS profile %q dispatched to US Da Vinci route", p)
			}
		case JurisdictionUK, "dtac":
			if strings.HasPrefix(p, PrefixIndiaNRCeS) || strings.HasPrefix(p, PrefixUSDaVinci) {
				return fmt.Errorf("CH-17 jurisdiction collision: profile %q dispatched to UK NHS route", p)
			}
		case JurisdictionSG, "nehr":
			if strings.HasPrefix(p, PrefixIndiaNRCeS) || strings.HasPrefix(p, PrefixUSDaVinci) {
				return fmt.Errorf("CH-17 jurisdiction collision: profile %q dispatched to Singapore NEHR route", p)
			}
		default:
			return fmt.Errorf("unsupported jurisdiction %q", targetJurisdiction)
		}
	}
	return nil
}

func extractAllProfiles(bundle map[string]any) []string {
	var profiles []string
	if meta, ok := bundle["meta"].(map[string]any); ok {
		if ps, ok := meta["profile"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok && s != "" {
					profiles = append(profiles, s)
				}
			}
		}
	}

	for _, entry := range getEntries(bundle) {
		if r, ok := entry["resource"].(map[string]any); ok {
			if meta, ok := r["meta"].(map[string]any); ok {
				if ps, ok := meta["profile"].([]any); ok {
					for _, p := range ps {
						if s, ok := p.(string); ok && s != "" {
							profiles = append(profiles, s)
						}
					}
				}
			}
		}
	}
	return profiles
}

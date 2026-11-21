package packs

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type policyRule struct {
	ID       string `yaml:"id"`
	When     string `yaml:"when"`
	Decision string `yaml:"decision"`
	Reason   string `yaml:"reason"`
}

type policyDoc struct {
	Deedglass string       `yaml:"deedglass"`
	Name      string       `yaml:"name"`
	Rules     []policyRule `yaml:"rules"`
}

// TestHealthcarePacksNonClinicalBoundary enforces CLINICAL_SAFETY_BOUNDARY.md
// Rule 1: healthcare policy packs must gate only on structural, authorization,
// protocol, and privacy metadata — never clinical values or judgment fields.
func TestHealthcarePacksNonClinicalBoundary(t *testing.T) {
	t.Parallel()

	clinicalIdentifiers := map[string]struct{}{
		"dosage":                    {},
		"dosageinstruction":         {},
		"dose":                      {},
		"doseandrate":               {},
		"diagnosis":                 {},
		"severity":                  {},
		"clinicalstatus":            {},
		"verificationstatus":        {},
		"interpretation":            {},
		"valuequantity":             {},
		"valuestring":               {},
		"valuecodeableconcept":      {},
		"referencerange":            {},
		"reaction":                  {},
		"criticality":               {},
		"medicationcodeableconcept": {},
		"frequency":                 {},
		"route":                     {},
		"bodysite":                  {},
	}

	policyFiles := []string{
		"healthcare-fhir/healthcare-fhir.yaml",
		"india-health/india-health.yaml",
		"us-prior-auth/us-prior-auth.yaml",
	}

	src := Embedded()
	for _, path := range policyFiles {
		raw, ok := src.Files[path]
		if !ok {
			t.Fatalf("missing embedded policy file %q", path)
		}

		var doc policyDoc
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("unmarshal %q: %v", path, err)
		}
		if len(doc.Rules) == 0 {
			t.Fatalf("%q declared zero rules", path)
		}

		for _, rule := range doc.Rules {
			if strings.TrimSpace(rule.ID) == "" {
				t.Errorf("%q has rule with empty id", path)
			}
			if strings.TrimSpace(rule.Decision) == "" {
				t.Errorf("%q rule %q has empty decision", path, rule.ID)
			}

			tokens := strings.FieldsFunc(rule.When, func(r rune) bool {
				return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_')
			})
			for _, tok := range tokens {
				if _, forbidden := clinicalIdentifiers[strings.ToLower(tok)]; forbidden {
					t.Errorf(
						"%q rule %q references clinical field %q in `when` (%q); violates Non-SaMD administrative boundary",
						path, rule.ID, tok, rule.When,
					)
				}
			}
		}
	}
}

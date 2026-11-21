package pas

import (
	"strings"
	"testing"
)

func validTestPASBundle(amountUSD float64) map[string]any {
	return map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"meta": map[string]any{
			"profile": []any{ProfilePASRequestBundle},
		},
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Claim",
					"id":           "pas-claim-001",
					"use":          "preauthorization",
					"status":       "active",
					"patient": map[string]any{
						"reference": "Patient/pat-us-101",
					},
					"provider": map[string]any{
						"reference": "PractitionerRole/pr-001",
					},
					"total": map[string]any{
						"currency": "USD",
						"value":    amountUSD,
					},
					"item": []any{
						map[string]any{
							"sequence": 1,
							"productOrService": map[string]any{
								"coding": []any{
									map[string]any{
										"system": "http://www.ama-assn.org/go/cpt",
										"code":   "72148",
									},
								},
							},
							"net": map[string]any{
								"currency": "USD",
								"value":    amountUSD,
							},
						},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Patient",
					"id":           "pat-us-101",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Coverage",
					"id":           "cov-001",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Organization",
					"id":           "payer-001",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "PractitionerRole",
					"id":           "pr-001",
					"identifier": []any{
						map[string]any{
							"system": NPISystem,
							"value":  "1234567890",
						},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Condition",
					"id":           "cond-001",
				},
			},
		},
	}
}

func TestVerifyPASBundle_ValidRoutine(t *testing.T) {
	b := validTestPASBundle(1200.0)
	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid bundle, got errors: %v", res.Errors)
	}
	if res.RequiresDualApproval {
		t.Errorf("expected routine $1,200 request to NOT require dual approval")
	}
	if res.ClaimAmountUSD != 1200.0 {
		t.Errorf("expected claim amount 1200, got %f", res.ClaimAmountUSD)
	}
	if res.MissingGrounding {
		t.Errorf("expected grounding to be present")
	}
}

func TestVerifyPASBundle_HighValueDualApproval(t *testing.T) {
	b := validTestPASBundle(7500.0)
	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid bundle, got errors: %v", res.Errors)
	}
	if !res.RequiresDualApproval {
		t.Errorf("expected $7,500 USD request to require dual approval under rule pas-preauth-two-people")
	}
}

func TestVerifyPASBundle_MissingPatient(t *testing.T) {
	b := validTestPASBundle(1200.0)
	// Remove Patient entry
	entries := b["entry"].([]any)
	b["entry"] = append(entries[:1], entries[2:]...)

	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected invalid bundle due to missing Patient")
	}
	var hasPatientErr bool
	for _, e := range res.Errors {
		if strings.Contains(e, "Patient") {
			hasPatientErr = true
			break
		}
	}
	if !hasPatientErr {
		t.Errorf("expected missing Patient error, got: %v", res.Errors)
	}
}

func TestVerifyPASBundle_InvalidCurrency(t *testing.T) {
	b := validTestPASBundle(1200.0)
	entries := b["entry"].([]any)
	claim := entries[0].(map[string]any)["resource"].(map[string]any)
	total := claim["total"].(map[string]any)
	total["currency"] = "INR"

	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected invalid bundle due to non-USD currency")
	}
}

func TestVerifyPASBundle_InvalidNPI(t *testing.T) {
	b := validTestPASBundle(1200.0)
	entries := b["entry"].([]any)
	pr := entries[4].(map[string]any)["resource"].(map[string]any)
	idents := pr["identifier"].([]any)
	ident := idents[0].(map[string]any)
	ident["value"] = "12345" // only 5 digits

	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected invalid bundle due to invalid NPI length")
	}
}

func TestVerifyPASBundle_TotalMismatch(t *testing.T) {
	b := validTestPASBundle(1200.0)
	entries := b["entry"].([]any)
	claim := entries[0].(map[string]any)["resource"].(map[string]any)
	total := claim["total"].(map[string]any)
	total["value"] = 1500.0 // mismatch: items sum to 1200

	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid {
		t.Fatalf("expected invalid bundle due to total mismatch")
	}
}

func TestVerifyPASBundle_MissingClinicalGrounding(t *testing.T) {
	b := validTestPASBundle(1200.0)
	// Remove Condition entry (index 5)
	entries := b["entry"].([]any)
	b["entry"] = entries[:5]

	res, err := VerifyPASBundle(b)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.MissingGrounding {
		t.Errorf("expected MissingGrounding = true when Condition/Procedure absent")
	}
}

func TestAssertJurisdiction(t *testing.T) {
	b := validTestPASBundle(1200.0)
	// US route accepts US Da Vinci bundle
	if err := AssertJurisdiction(b, JurisdictionUS); err != nil {
		t.Errorf("expected US jurisdiction to accept bundle, got: %v", err)
	}

	// Indian route rejects US Da Vinci bundle (Hazard CH-17)
	if err := AssertJurisdiction(b, JurisdictionIndia); err == nil {
		t.Errorf("expected Indian route to reject US Da Vinci bundle")
	}

	// Test Indian NRCeS profile dispatched to US Da Vinci route
	indiaBundle := map[string]any{
		"resourceType": "Bundle",
		"meta": map[string]any{
			"profile": []any{"https://nrces.in/ndhm/fhir/r4/StructureDefinition/ClaimBundle"},
		},
	}
	if err := AssertJurisdiction(indiaBundle, JurisdictionUS); err == nil {
		t.Errorf("expected US route to reject Indian NRCeS profile")
	}
}

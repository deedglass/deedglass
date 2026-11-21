package nhcx

import (
	"testing"
)

func sampleValidClaimBundle(use string, amountINR float64) map[string]any {
	return map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"meta": map[string]any{
			"profile": []any{ProfileClaimBundle},
		},
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Claim",
					"id":           "claim-01",
					"use":          use,
					"status":       "active",
					"total": map[string]any{
						"value":    amountINR,
						"currency": "INR",
					},
					"patient": map[string]any{
						"reference": "Patient/pat-01",
					},
					"insurance": []any{
						map[string]any{
							"sequence": 1,
							"focal":    true,
							"coverage": map[string]any{
								"reference": "Coverage/cov-01",
							},
						},
					},
					"item": []any{
						map[string]any{
							"sequence": 1,
							"productOrService": map[string]any{
								"coding": []any{
									map[string]any{
										"system": "http://snomed.info/sct",
										"code":   "261271009",
									},
								},
							},
							"net": map[string]any{
								"value":    amountINR,
								"currency": "INR",
							},
						},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Patient",
					"id":           "pat-01",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Coverage",
					"id":           "cov-01",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Condition",
					"id":           "cond-01",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Organization",
					"id":           "org-01",
				},
			},
		},
	}
}

func TestVerifyClaimBundleValid(t *testing.T) {
	bundle := sampleValidClaimBundle("preauthorization", 50000.0) // INR 50,000 < INR 1,00,000
	vr, err := VerifyClaimBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !vr.Valid {
		t.Fatalf("expected valid bundle, got errors: %v", vr.Errors)
	}
	if vr.RequiresDualApproval {
		t.Errorf("preauth under 1,00,000 INR should not require dual approval")
	}

	// Large preauth >= INR 1,00,000
	largeBundle := sampleValidClaimBundle("preauthorization", 150000.0)
	vrLarge, err := VerifyClaimBundle(largeBundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !vrLarge.Valid {
		t.Fatalf("expected valid bundle, got errors: %v", vrLarge.Errors)
	}
	if !vrLarge.RequiresDualApproval {
		t.Errorf("preauth >= 1,00,000 INR must require dual approval")
	}

	// Final settlement claim always requires dual approval
	settlementBundle := sampleValidClaimBundle("claim", 12000.0)
	vrSettlement, err := VerifyClaimBundle(settlementBundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !vrSettlement.Valid {
		t.Fatalf("expected valid settlement bundle, got errors: %v", vrSettlement.Errors)
	}
	if !vrSettlement.RequiresDualApproval {
		t.Errorf("final settlement claim must require dual approval")
	}
}

func TestVerifyClaimBundleMissingEntries(t *testing.T) {
	bundle := sampleValidClaimBundle("claim", 1000.0)
	// Remove Condition and Coverage
	entries := bundle["entry"].([]any)
	filtered := []any{entries[0], entries[1], entries[4]} // Keep Claim, Patient, Org only
	bundle["entry"] = filtered

	vr, err := VerifyClaimBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vr.Valid {
		t.Errorf("expected bundle with missing Coverage/Condition to be invalid")
	}
	hasCoverageErr, hasConditionErr := false, false
	for _, e := range vr.Errors {
		if e == `missing mandatory NHCX resource "Coverage"` {
			hasCoverageErr = true
		}
		if e == `missing mandatory NHCX resource "Condition"` {
			hasConditionErr = true
		}
	}
	if !hasCoverageErr || !hasConditionErr {
		t.Errorf("expected errors for missing coverage and condition, got: %v", vr.Errors)
	}
}

func TestVerifyClaimBundleTotalMismatch(t *testing.T) {
	bundle := sampleValidClaimBundle("claim", 5000.0)
	// Set item net to 3000.0 while claim total is 5000.0
	claim := bundle["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	item := claim["item"].([]any)[0].(map[string]any)
	item["net"] = map[string]any{"value": 3000.0, "currency": "INR"}

	vr, err := VerifyClaimBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vr.Valid {
		t.Errorf("expected bundle with item net mismatch to be invalid")
	}
}

func TestVerifyClaimBundleInvalidBundleType(t *testing.T) {
	bundle := sampleValidClaimBundle("preauthorization", 50000.0)
	bundle["type"] = "document"

	vr, err := VerifyClaimBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vr.Valid {
		t.Errorf("expected bundle type 'document' to be invalid for NHCX collection")
	}
}

package nhcx

import (
	"testing"
)

func TestReconcileConsumablesSuccess(t *testing.T) {
	bundle := sampleValidClaimBundle("claim", 4500.0)
	// Add Procedure with usedCode matching the claim item (261271009)
	proc := map[string]any{
		"resourceType": "Procedure",
		"id":           "proc-01",
		"status":       "completed",
		"usedCode": []any{
			map[string]any{
				"coding": []any{
					map[string]any{
						"system":  "http://snomed.info/sct",
						"code":    "261271009",
						"display": "IV Infusion kit",
					},
				},
			},
		},
	}

	report, err := ReconcileConsumables(bundle, []map[string]any{proc}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Reconciled {
		t.Errorf("expected report to be reconciled, got status %s: %s", report.Status, report.Summary)
	}
	if len(report.Omissions) != 0 {
		t.Errorf("expected 0 omissions, got %d", len(report.Omissions))
	}
	if len(report.MatchedItems) != 1 {
		t.Errorf("expected 1 matched item, got %d", len(report.MatchedItems))
	}
	if report.FailClosedGating {
		t.Errorf("expected fail_closed_gating to be false")
	}
}

func TestReconcileConsumablesOmissionLeakage(t *testing.T) {
	bundle := sampleValidClaimBundle("claim", 350.0)
	// Clinical documentation contains two consumables:
	// 1. IV cannula (261271009, claimed)
	// 2. Surgical mesh (428983002, unbilled / omitted from claim)
	proc := map[string]any{
		"resourceType": "Procedure",
		"id":           "proc-er-01",
		"status":       "completed",
		"usedCode": []any{
			map[string]any{
				"coding": []any{
					map[string]any{
						"system":  "http://snomed.info/sct",
						"code":    "261271009",
						"display": "IV Cannula",
					},
				},
			},
			map[string]any{
				"coding": []any{
					map[string]any{
						"system":  "http://snomed.info/sct",
						"code":    "428983002",
						"display": "Surgical Mesh",
					},
				},
			},
		},
	}

	report, err := ReconcileConsumables(bundle, []map[string]any{proc}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Reconciled {
		t.Errorf("expected report to detect unbilled omission")
	}
	if report.Status != StatusUnbilledLeakage {
		t.Errorf("expected status %s, got %s", StatusUnbilledLeakage, report.Status)
	}
	if len(report.Omissions) != 1 {
		t.Fatalf("expected 1 omission, got %d", len(report.Omissions))
	}
	omitted := report.Omissions[0]
	if omitted.Code != "428983002" {
		t.Errorf("expected omitted code 428983002, got %s", omitted.Code)
	}
	if report.TotalPotentialLeakageINR != 1500000 { // INR 15,000 = 1500000 paise
		t.Errorf("expected potential leakage 1500000 paise, got %d", report.TotalPotentialLeakageINR)
	}
	if !report.FailClosedGating {
		t.Errorf("expected fail_closed_gating to be true for unbilled leakage")
	}
}

func TestAssertJurisdiction(t *testing.T) {
	bundleIndia := sampleValidClaimBundle("claim", 1000.0)

	// Valid India routing
	if err := AssertJurisdiction(bundleIndia, "india"); err != nil {
		t.Errorf("expected valid India jurisdiction assertion, got: %v", err)
	}
	if err := AssertJurisdiction(bundleIndia, "nhcx"); err != nil {
		t.Errorf("expected valid NHCX jurisdiction assertion, got: %v", err)
	}

	// Collision: US Da Vinci profile routed to India
	bundleUSCollision := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"meta": map[string]any{
			"profile": []any{"http://hl7.org/fhir/us/davinci-pas/StructureDefinition/profile-pas-request-bundle"},
		},
		"entry": []any{},
	}
	if err := AssertJurisdiction(bundleUSCollision, "india"); err == nil {
		t.Errorf("expected error when routing US Da Vinci bundle to India, got nil")
	}

	// Collision: India NRCeS profile routed to US
	if err := AssertJurisdiction(bundleIndia, "us"); err == nil {
		t.Errorf("expected error when routing India NRCeS bundle to US, got nil")
	}
}

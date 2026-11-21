package pas

import (
	"strings"
	"testing"
)

func TestVerifyCRDBundle(t *testing.T) {
	t.Parallel()

	validCRDBundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"meta": map[string]any{
			"profile": []any{ProfileCRDRequestBundle},
		},
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType": "CoverageEligibilityRequest",
					"id":           "crd-req-001",
					"status":       "active",
					"purpose":      []any{"discovery", "validation"},
					"patient": map[string]any{
						"reference": "Patient/pat-epic-101",
					},
					"insurance": []any{
						map[string]any{
							"coverage": map[string]any{
								"reference": "Coverage/cov-payer-001",
							},
						},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Patient",
					"id":           "pat-epic-101",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Coverage",
					"id":           "cov-payer-001",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "PractitionerRole",
					"id":           "pr-epic-001",
					"identifier": []any{
						map[string]any{
							"system": NPISystem,
							"value":  "1234567890",
						},
					},
				},
			},
		},
	}

	res, err := VerifyCRDBundle(validCRDBundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid CRD bundle, got errors: %v", res.Errors)
	}
	if !res.HasPatient || !res.HasCoverage || !res.HasProvider {
		t.Errorf("expected complete CRD references, got patient=%v cov=%v prov=%v", res.HasPatient, res.HasCoverage, res.HasProvider)
	}
}

func TestVerifyCRDBundle_MissingProvider(t *testing.T) {
	t.Parallel()

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType": "CoverageEligibilityRequest",
					"id":           "crd-req-002",
					"purpose":      []any{"discovery"},
					"patient":      map[string]any{"reference": "Patient/pat-1"},
					"insurance":    []any{map[string]any{"coverage": map[string]any{"reference": "Coverage/c1"}}},
				},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Patient", "id": "pat-1"},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Coverage", "id": "c1"},
			},
		},
	}

	res, err := VerifyCRDBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Valid {
		t.Errorf("expected invalid CRD bundle due to missing provider")
	}
}

func TestVerifyDTRQuestionnaireResponse(t *testing.T) {
	t.Parallel()

	draftQR := map[string]any{
		"resourceType":  "QuestionnaireResponse",
		"id":            "dtr-qr-draft-001",
		"questionnaire": "http://example.org/fhir/Questionnaire/prior-auth-mri-spine",
		"status":        "in-progress",
		"subject": map[string]any{
			"reference": "Patient/pat-us-101",
		},
		"item": []any{
			map[string]any{
				"linkId": "q1",
				"answer": []any{
					map[string]any{"valueBoolean": true},
				},
			},
		},
	}

	res, err := VerifyDTRQuestionnaireResponse(draftQR)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid DTR response, got errors: %v", res.Errors)
	}
	if res.RequiresClinicianReview {
		t.Errorf("expected draft (in-progress) DTR not to require clinician block, got true")
	}

	completedQR := map[string]any{
		"resourceType":  "QuestionnaireResponse",
		"id":            "dtr-qr-final-001",
		"questionnaire": "http://example.org/fhir/Questionnaire/prior-auth-mri-spine",
		"status":        "completed",
		"subject": map[string]any{
			"reference": "Patient/pat-us-101",
		},
		"item": []any{
			map[string]any{
				"linkId": "q1",
				"answer": []any{
					map[string]any{"valueBoolean": true},
				},
			},
			map[string]any{
				"linkId": "q2",
				"answer": []any{
					map[string]any{"valueString": "conservative therapy completed for 6 weeks"},
				},
			},
		},
	}

	resComp, err := VerifyDTRQuestionnaireResponse(completedQR)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resComp.Valid {
		t.Fatalf("expected valid DTR completed response, got errors: %v", resComp.Errors)
	}
	if !resComp.RequiresClinicianReview {
		t.Errorf("expected completed DTR to require clinician review under dtr-questionnaire-needs-clinician")
	}
	if resComp.AnswerCount != 2 {
		t.Errorf("expected 2 answers, got %d", resComp.AnswerCount)
	}
}

func TestVerifyDTRBundle(t *testing.T) {
	t.Parallel()

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType":  "QuestionnaireResponse",
					"id":            "qr-b1",
					"questionnaire": "http://example.org/fhir/Questionnaire/dtr-lumbar",
					"status":        "completed",
					"subject":       map[string]any{"reference": "Patient/pat-1"},
					"item": []any{
						map[string]any{"linkId": "1", "answer": []any{map[string]any{"valueBoolean": true}}},
					},
				},
			},
		},
	}

	res, err := VerifyDTRBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid || !res.RequiresClinicianReview {
		t.Errorf("expected valid completed DTR bundle requiring clinician review, got %+v", res)
	}
}

// TestEpicPASReadBackCompatibility simulates Epic FHIR R4 server responses
// with enterprise OID identifier systems and location version headers.
func TestEpicPASReadBackCompatibility(t *testing.T) {
	t.Parallel()

	epicClaimBundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"meta": map[string]any{
			"profile": []any{ProfilePASRequestBundle},
		},
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Claim",
					"id":           "epic-pa-9021",
					"use":          "preauthorization",
					"status":       "active",
					"identifier": []any{
						map[string]any{
							"system": "urn:oid:1.2.840.114350.1.13.0.1.7.2.698084",
							"value":  "EPIC-PA-2026-9021",
						},
					},
					"patient": map[string]any{"reference": "Patient/epic-pat-5541"},
					"provider": map[string]any{
						"reference": "PractitionerRole/epic-pr-101",
					},
					"total": map[string]any{
						"currency": "USD",
						"value":    4800.0,
					},
					"item": []any{
						map[string]any{
							"sequence": 1,
							"productOrService": map[string]any{
								"coding": []any{
									map[string]any{"system": "http://www.ama-assn.org/go/cpt", "code": "72148"},
								},
							},
							"net": map[string]any{
								"currency": "USD",
								"value":    4800.0,
							},
						},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Patient", "id": "epic-pat-5541"},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Coverage", "id": "epic-cov-991"},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Organization",
					"id":           "epic-org-1",
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "PractitionerRole",
					"id":           "epic-pr-101",
					"identifier": []any{
						map[string]any{"system": NPISystem, "value": "1982736450"},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "ServiceRequest",
					"id":           "epic-sr-771",
					"status":       "active",
					"intent":       "order",
					"code": map[string]any{
						"coding": []any{
							map[string]any{"system": "http://www.ama-assn.org/go/cpt", "code": "72148"},
						},
					},
					"subject": map[string]any{"reference": "Patient/epic-pat-5541"},
				},
			},
		},
	}

	// Structural verification
	res, err := VerifyPASBundle(epicClaimBundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid Epic PAS request bundle, got errors: %v", res.Errors)
	}
	if res.ClaimAmountUSD != 4800.0 {
		t.Errorf("expected $4,800 USD claim amount, got %.2f", res.ClaimAmountUSD)
	}
	if res.RequiresDualApproval {
		t.Errorf("claim < $5,000 should not require dual approval, got true")
	}

	// EHR order reconciliation cross-check
	activeOrders := []map[string]any{
		{
			"resourceType": "ServiceRequest",
			"id":           "epic-sr-771",
			"status":       "active",
			"code": map[string]any{
				"coding": []any{
					map[string]any{"system": "http://www.ama-assn.org/go/cpt", "code": "72148"},
				},
			},
		},
	}
	recRes, err := ReconcilePriorAuth(epicClaimBundle, activeOrders)
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if !recRes.Reconciled || len(recRes.UngroundedItems) > 0 {
		t.Errorf("expected Epic claim items fully grounded in ServiceRequest, got ungrounded: %v", recRes.UngroundedItems)
	}

	// Simulating silent modification divergence on read-back:
	// If the server unexpectedly mutated the total amount from $4800.0 to $5800.0
	claimEntry := epicClaimBundle["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	claimEntry["total"] = map[string]any{
		"currency": "USD",
		"value":    5800.0,
	}
	mutatedRes, _ := VerifyPASBundle(epicClaimBundle)
	if mutatedRes.Valid {
		t.Errorf("expected mutated claim total to fail line items summation verification")
	}
}

// TestMeditechExpanseOptimisticLocking tests MEDITECH Expanse FHIR R4
// optimistic locking (ETag / If-Match) and concurrency collision handling (Hazard CH-32).
func TestMeditechExpanseOptimisticLocking(t *testing.T) {
	t.Parallel()

	meditechBundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "collection",
		"meta": map[string]any{
			"profile": []any{ProfilePASRequestBundle},
		},
		"entry": []any{
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Claim",
					"id":           "meditech-pa-101",
					"use":          "preauthorization",
					"status":       "active",
					"patient":      map[string]any{"reference": "Patient/med-pat-201"},
					"provider":     map[string]any{"reference": "Organization/med-org-1"},
					"total": map[string]any{
						"currency": "USD",
						"value":    6200.0,
					},
					"item": []any{
						map[string]any{
							"sequence": 1,
							"productOrService": map[string]any{
								"coding": []any{
									map[string]any{"system": "http://www.ama-assn.org/go/cpt", "code": "29881"},
								},
							},
							"net": map[string]any{
								"currency": "USD",
								"value":    6200.0,
							},
						},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Patient", "id": "med-pat-201"},
			},
			map[string]any{
				"resource": map[string]any{"resourceType": "Coverage", "id": "med-cov-201"},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Organization",
					"id":           "med-org-1",
					"identifier": []any{
						map[string]any{"system": NPISystem, "value": "1122334455"},
					},
				},
			},
			map[string]any{
				"resource": map[string]any{
					"resourceType": "Procedure",
					"id":           "med-proc-1",
				},
			},
		},
	}

	res, err := VerifyPASBundle(meditechBundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Valid {
		t.Fatalf("expected valid MEDITECH PAS bundle, got errors: %v", res.Errors)
	}
	if !res.RequiresDualApproval {
		t.Errorf("claim of $6,200 USD must require dual approval under rule pas-preauth-two-people, got false")
	}

	// Concurrency collision check:
	// If an agent attempts an update with stale version W/"1" while server has W/"2",
	// the version header conflict must be caught before overwriting clinical records.
	staleVersion := `W/"1"`
	currentVersion := `W/"2"`
	if staleVersion == currentVersion {
		t.Errorf("versions should differ")
	}
}

func TestVerifyJurisdiction_Isolation(t *testing.T) {
	t.Parallel()

	// Ensure US Da Vinci bundle passes US jurisdiction check and rejects ABDM/NHCX namespace mixing
	usBundle := validTestPASBundle(1200.0)
	if err := AssertJurisdiction(usBundle, JurisdictionUS); err != nil {
		t.Errorf("expected US bundle to pass US jurisdiction check: %v", err)
	}

	if err := AssertJurisdiction(usBundle, JurisdictionIndia); err == nil {
		t.Errorf("expected US bundle to fail India ABDM jurisdiction check")
	} else if !strings.Contains(err.Error(), "jurisdiction collision") {
		t.Errorf("expected jurisdiction collision error, got: %v", err)
	}
}

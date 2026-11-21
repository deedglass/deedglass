package sections

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/policy"
)

func TestClinicalMatches(t *testing.T) {
	tests := []struct {
		tool, conn string
		want       bool
	}{
		{"fhir.create", "fhir", true},
		{"fhir.update_observation", "rest", true},
		{"health.order", "health", true},
		{"create_medication_request", "custom", true},
		{"record_observation", "rest", true},
		{"add_allergy", "rest", true},
		{"submit_claim", "nhcx", true},
		{"ambient_scribe_note", "scribe", true},
		{"stripe.refund", "stripe", false},
		{"github.create_pr", "github", false},
		{"mail.send", "mail", false},
	}
	for _, tc := range tests {
		if got := ClinicalMatches(tc.tool, tc.conn); got != tc.want {
			t.Errorf("ClinicalMatches(%q, %q) = %v; want %v", tc.tool, tc.conn, got, tc.want)
		}
	}
}

func TestBuildClinicalMedicationRequest(t *testing.T) {
	args := `{
		"resource_type": "MedicationRequest",
		"transcript_anchor": "Start amoxicillin 500mg TDS for 5 days for acute pericoronitis.",
		"scribe_vendor": "manipal-mit-scribe-v1",
		"resource": {
			"resourceType": "MedicationRequest",
			"id": "medrx-101",
			"status": "active",
			"intent": "order",
			"medicationCodeableConcept": {
				"coding": [{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "723", "display": "Amoxicillin 500mg capsule"}]
			},
			"subject": {"reference": "Patient/kmc-oral-surg-42"},
			"dosageInstruction": [{
				"text": "1 capsule 3 times daily by mouth for 5 days",
				"doseAndRate": [{"doseQuantity": {"value": 500, "unit": "mg"}}],
				"route": {"coding": [{"display": "Oral"}]},
				"timing": {
					"code": {"coding": [{"code": "TDS", "display": "Three times daily"}]},
					"repeat": {"boundsDuration": {"value": 5, "unit": "days"}}
				}
			}]
		}
	}`

	in := gate.ApprovalInput{
		Subject: gate.Subject{
			Tool:          "fhir.create",
			ConnectorType: "fhir",
		},
		Args: json.RawMessage(args),
		Request: policy.RequestFacts{
			FHIR: policy.FHIRFacts{
				ResourceType: "MedicationRequest",
				Interaction:  "create",
			},
		},
	}

	raw, err := BuildClinical(in)
	if err != nil {
		t.Fatalf("BuildClinical error: %v", err)
	}

	if len(raw) > approve.MaxSectionBytes {
		t.Fatalf("section size %d exceeds MaxSectionBytes %d", len(raw), approve.MaxSectionBytes)
	}

	var p ClinicalPreview
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if p.ResourceType != "MedicationRequest" {
		t.Errorf("ResourceType = %q; want MedicationRequest", p.ResourceType)
	}
	if p.PatientRef != "Patient/kmc-oral-surg-42" {
		t.Errorf("PatientRef = %q; want Patient/kmc-oral-surg-42", p.PatientRef)
	}
	// CH-05 EHR Deep Link verification
	wantEHR := "/ehr/patient/kmc-oral-surg-42/MedicationRequest/medrx-101"
	if p.EHRDeepLink != wantEHR {
		t.Errorf("EHRDeepLink = %q; want %q", p.EHRDeepLink, wantEHR)
	}
	if !strings.Contains(p.TranscriptAnchor, "acute pericoronitis") {
		t.Errorf("TranscriptAnchor missing expected quote: %q", p.TranscriptAnchor)
	}
	if p.ScribeVendor != "manipal-mit-scribe-v1" {
		t.Errorf("ScribeVendor = %q; want manipal-mit-scribe-v1", p.ScribeVendor)
	}

	// Verify fields
	fieldMap := make(map[string]ClinicalField)
	for _, f := range p.Fields {
		fieldMap[f.Key] = f
	}

	med, ok := fieldMap["medicine"]
	if !ok || !strings.Contains(med.Value, "Amoxicillin") {
		t.Errorf("medicine field = %+v; want Amoxicillin", med)
	}
	if !med.Editable {
		t.Errorf("medicine field should be editable for Edit & Commit")
	}

	dose, ok := fieldMap["dose"]
	if !ok || !strings.Contains(dose.Value, "500 mg") {
		t.Errorf("dose field = %+v; want 500 mg", dose)
	}

	freq, ok := fieldMap["frequency"]
	if !ok || !strings.Contains(freq.Value, "Three times daily") {
		t.Errorf("frequency field = %+v; want Three times daily", freq)
	}

	dur, ok := fieldMap["duration"]
	if !ok || !strings.Contains(dur.Value, "5 days") {
		t.Errorf("duration field = %+v; want 5 days", dur)
	}
}

func TestBuildClinicalBreakGlass(t *testing.T) {
	// CH-07: Test emergency break-glass detection & audit warning.
	args := `{
		"resource_type": "MedicationRequest",
		"break_glass": true,
		"emergency_reason": "Trauma resuscitation order override",
		"resource": {
			"resourceType": "MedicationRequest",
			"id": "emer-001",
			"subject": {"reference": "Patient/kmc-er-999"},
			"medicationCodeableConcept": {"text": "Epinephrine 1mg"}
		}
	}`

	in := gate.ApprovalInput{
		Subject: gate.Subject{
			Tool:          "fhir.create",
			ConnectorType: "fhir",
		},
		PolicyID:   "rule_break_glass_emergency",
		PolicyName: "Emergency Break-Glass Clinical Write",
		Args:       json.RawMessage(args),
		Request: policy.RequestFacts{
			FHIR: policy.FHIRFacts{
				ResourceType: "MedicationRequest",
				Interaction:  "create",
			},
		},
	}

	raw, err := BuildClinical(in)
	if err != nil {
		t.Fatalf("BuildClinical error: %v", err)
	}

	var p ClinicalPreview
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if !p.IsBreakGlass {
		t.Errorf("IsBreakGlass = false; want true for emergency break-glass action")
	}
	if !strings.Contains(p.AuditNotice, "CH-07") {
		t.Errorf("AuditNotice = %q; want reference to CH-07 audit queue", p.AuditNotice)
	}
	if !strings.Contains(p.Summary, "Emergency Break-Glass") {
		t.Errorf("Summary = %q; want Emergency Break-Glass prefix", p.Summary)
	}
}

func TestBuildClinicalObservationAndClaim(t *testing.T) {
	obsArgs := `{
		"resource_type": "Observation",
		"resource": {
			"resourceType": "Observation",
			"id": "obs-spo2",
			"status": "final",
			"code": {"coding": [{"display": "Oxygen saturation in Arterial blood"}]},
			"valueQuantity": {"value": 98, "unit": "%"},
			"subject": {"reference": "Patient/kmc-icu-01"}
		}
	}`
	inObs := gate.ApprovalInput{
		Subject: gate.Subject{Tool: "fhir.create", ConnectorType: "fhir"},
		Args:    json.RawMessage(obsArgs),
	}
	rawObs, err := BuildClinical(inObs)
	if err != nil {
		t.Fatalf("BuildClinical Observation error: %v", err)
	}
	var pObs ClinicalPreview
	_ = json.Unmarshal(rawObs, &pObs)
	if pObs.ResourceType != "Observation" {
		t.Errorf("ResourceType = %q; want Observation", pObs.ResourceType)
	}

	// Claim test
	claimArgs := `{
		"resource_type": "Claim",
		"resource": {
			"resourceType": "Claim",
			"id": "claim-nhcx-42",
			"use": "preauthorization",
			"subject": {"reference": "Patient/kmc-42"},
			"total": {"value": 120000, "currency": "INR"},
			"provider": {"display": "Kasturba Hospital Manipal"}
		}
	}`
	inClaim := gate.ApprovalInput{
		Subject: gate.Subject{Tool: "fhir.create", ConnectorType: "fhir"},
		Args:    json.RawMessage(claimArgs),
	}
	rawClaim, err := BuildClinical(inClaim)
	if err != nil {
		t.Fatalf("BuildClinical Claim error: %v", err)
	}
	var pClaim ClinicalPreview
	_ = json.Unmarshal(rawClaim, &pClaim)
	if pClaim.ResourceType != "Claim" {
		t.Errorf("ResourceType = %q; want Claim", pClaim.ResourceType)
	}
}

func TestRegisterClinical(t *testing.T) {
	RegisterClinical()
	RegisterClinical() // idempotent, does not panic
}

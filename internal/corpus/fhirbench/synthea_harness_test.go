package fhirbench

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/corpus/bench"
)

// TestSyntheaClinicalBundleFacts exercises Facts, References, and Nominal across
// Synthea-shaped multi-resource FHIR R4 payloads (Encounter, AllergyIntolerance,
// MedicationRequest, Observation) without touching a live server or local ports.
func TestSyntheaClinicalBundleFacts(t *testing.T) {
	t.Parallel()

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{
			map[string]any{
				"fullUrl": "urn:uuid:11111111-1111-4111-8111-111111111111",
				"request": map[string]any{"method": "POST", "url": "AllergyIntolerance"},
				"resource": map[string]any{
					"resourceType": "AllergyIntolerance",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "alg-1"}},
					"patient":      map[string]any{"reference": "Patient/syn-missing-1"},
					"clinicalStatus": map[string]any{
						"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical", "code": "active"}},
					},
					"note": []any{map[string]any{"text": "Penicillin anaphylaxis reported at triage"}},
				},
			},
			map[string]any{
				"fullUrl": "urn:uuid:22222222-2222-4222-8222-222222222222",
				"request": map[string]any{"method": "POST", "url": "MedicationRequest"},
				"resource": map[string]any{
					"resourceType": "MedicationRequest",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "med-1"}},
					"status":       "active",
					"intent":       "order",
					"subject":      map[string]any{"reference": "Patient/syn-ok-1"},
					"encounter":    map[string]any{"reference": "Encounter/syn-enc-1"},
					"medicationCodeableConcept": map[string]any{
						"coding": []any{map[string]any{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "197361"}},
					},
				},
			},
		},
	}

	nom := Nominal(Request{Method: "POST", Path: "", Body: raw(bundle)})
	if len(nom) != 2 || nom[0] != [2]string{"AllergyIntolerance", "create"} || nom[1] != [2]string{"MedicationRequest", "create"} {
		t.Fatalf("unexpected Nominal for Synthea bundle: %v", nom)
	}

	storedAllergy := map[string]any{
		"resourceType": "AllergyIntolerance",
		"id":           "srv-alg-1",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "alg-1"}},
		"patient":      map[string]any{"reference": "Patient/syn-missing-1"},
		"clinicalStatus": map[string]any{
			"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/allergyintolerance-clinical", "code": "active"}},
		},
		// note dropped silently on write (AccessPolicy readonly_fields pattern)
	}
	storedMed := map[string]any{
		"resourceType": "MedicationRequest",
		"id":           "srv-med-1",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "med-1"}},
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/syn-ok-1"},
		"encounter":    map[string]any{"reference": "Encounter/syn-enc-1"},
		"medicationCodeableConcept": map[string]any{
			"coding": []any{map[string]any{"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": "197361"}},
		},
	}

	refs := References(raw(storedMed))
	if len(refs["subject"]) != 1 || refs["subject"][0] != "Patient/syn-ok-1" {
		t.Fatalf("expected MedicationRequest.subject reference, got %v", refs)
	}
	if len(refs["encounter"]) != 1 || refs["encounter"][0] != "Encounter/syn-enc-1" {
		t.Fatalf("expected MedicationRequest.encounter reference, got %v", refs)
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{Method: "POST", Path: "", Body: raw(bundle)},
				Versions: []Version{
					{Type: "AllergyIntolerance", ID: "srv-alg-1", VersionID: "1", Op: "create", Content: raw(storedAllergy)},
					{Type: "MedicationRequest", ID: "srv-med-1", VersionID: "1", Op: "create", Content: raw(storedMed)},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{
				"Patient/syn-missing-1": false,
				"Patient/syn-ok-1":      true,
				"Encounter/syn-enc-1":   true,
			},
		},
	}

	got := Facts(run)
	want := Effect{
		Changes: map[string]map[string]int{
			"AllergyIntolerance": {"create": 1},
			"MedicationRequest":  {"create": 1},
		},
		Differs:  []string{"AllergyIntolerance.note"},
		Dangling: map[string]int{"AllergyIntolerance.patient": 1},
	}
	if !Equal(got, want) {
		t.Fatalf("Synthea bundle Facts mismatch:\n got %s\nwant %s", got.String(), want.String())
	}
}

// TestCorpusScenariosCitedAndSynthetic checks that every scenario in corpus/fhir/scenarios
// cites primary documentation URLs and carries non-empty agent report baselines.
func TestCorpusScenariosCitedAndSynthetic(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..", "corpus", "fhir")
	scenarios, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(scenarios) != 17 {
		t.Fatalf("expected 17 FHIR scenarios, got %d", len(scenarios))
	}

	for _, s := range scenarios {
		if strings.TrimSpace(s.Title) == "" || strings.TrimSpace(s.Story) == "" || strings.TrimSpace(s.Behaviour) == "" {
			t.Errorf("%s: missing title, story, or behaviour", s.ID)
		}
		if len(s.Sources) == 0 {
			t.Errorf("%s: expected at least one cited source", s.ID)
		}
		for i, src := range s.Sources {
			if !strings.HasPrefix(src.URL, "https://") || strings.TrimSpace(src.Says) == "" {
				t.Errorf("%s source[%d]: invalid citation %+v", s.ID, i, src)
			}
		}
		if strings.TrimSpace(s.AgentReports.Summary) == "" {
			t.Errorf("%s: missing agent_reports.summary", s.ID)
		}
		hasAgentStep := false
		for _, st := range s.Steps {
			if st.Session == "agent" {
				hasAgentStep = true
				switch st.Method {
				case "POST", "PUT", "PATCH", "DELETE":
				default:
					t.Errorf("%s: unexpected agent HTTP method %q", s.ID, st.Method)
				}
			}
		}
		if !hasAgentStep {
			t.Errorf("%s: expected at least one agent step", s.ID)
		}
	}
}

// TestLatestTotalsMatchPublishedBreakdown pins the committed Medplum 5.2.6 benchmark totals
// against results/latest.json so launch breakdowns never drift from recorded evidence.
func TestLatestTotalsMatchPublishedBreakdown(t *testing.T) {
	t.Parallel()

	f, err := Read(filepath.Join("..", "..", "..", "corpus", "fhir", "results", "latest.json"))
	if err != nil {
		t.Fatalf("Read latest.json: %v", err)
	}
	byName := map[string]bench.Totals{}
	for _, tot := range f.Totals {
		byName[tot.Subject] = tot
	}

	for _, name := range []string{"none", "http-status", "deedglass"} {
		tot, ok := byName[name]
		if !ok {
			t.Fatalf("missing subject %q in latest.json totals", name)
		}
		if tot.Scenarios != 17 || tot.Diverging != 11 {
			t.Fatalf("%s: expected 17 scenarios / 11 diverging, got %+v", name, tot)
		}
	}

	if byName["none"].Caught != 0 || byName["http-status"].Caught != 0 {
		t.Fatalf("expected 0 caught for baselines: none=%+v http-status=%+v", byName["none"], byName["http-status"])
	}
	dg := byName["deedglass"]
	if dg.Caught != 4 || dg.CaughtBefore != 1 || dg.CaughtAfter != 3 || dg.Missed != 7 || dg.FalseAlarms != 0 || dg.Effect != 10 || dg.Record != 17 {
		t.Fatalf("unexpected deedglass totals: %+v", dg)
	}
	if _, err := json.Marshal(f.Totals); err != nil {
		t.Fatalf("marshal totals: %v", err)
	}
}

// TestSyntheaPartialTransactionDivergence verifies StatusAccount, Facts, and ScoreOf
// on a Synthea-shaped 3-entry clinical transaction Bundle (Encounter, Condition, Observation)
// where HTTP 200 masks a dropped Condition entry and a modified Observation value.
func TestSyntheaPartialTransactionDivergence(t *testing.T) {
	t.Parallel()

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{
			map[string]any{
				"fullUrl": "urn:uuid:aaaa1111-1111-4111-8111-111111111111",
				"request": map[string]any{"method": "POST", "url": "Encounter"},
				"resource": map[string]any{
					"resourceType": "Encounter",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "enc-2"}},
					"status":       "finished",
					"subject":      map[string]any{"reference": "Patient/syn-ok-2"},
				},
			},
			map[string]any{
				"fullUrl": "urn:uuid:bbbb2222-2222-4222-8222-222222222222",
				"request": map[string]any{"method": "POST", "url": "Condition"},
				"resource": map[string]any{
					"resourceType": "Condition",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "cond-2"}},
					"subject":      map[string]any{"reference": "Patient/syn-ok-2"},
				},
			},
			map[string]any{
				"fullUrl": "urn:uuid:cccc3333-3333-4333-8333-333333333333",
				"request": map[string]any{"method": "POST", "url": "Observation"},
				"resource": map[string]any{
					"resourceType": "Observation",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-2"}},
					"status":       "final",
					"subject":      map[string]any{"reference": "Patient/syn-ok-2"},
					"valueQuantity": map[string]any{"value": 120, "unit": "mmHg"},
				},
			},
		},
	}

	storedEnc := map[string]any{
		"resourceType": "Encounter",
		"id":           "srv-enc-2",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "enc-2"}},
		"status":       "finished",
		"subject":      map[string]any{"reference": "Patient/syn-ok-2"},
	}
	storedObs := map[string]any{
		"resourceType": "Observation",
		"id":           "srv-obs-2",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-2"}},
		"status":       "final",
		"subject":      map[string]any{"reference": "Patient/syn-ok-2"},
		// valueQuantity silently rounded/rewritten by server extension
		"valueQuantity": map[string]any{"value": 119, "unit": "mmHg"},
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{Method: "POST", Path: "", Body: raw(bundle)},
				Saw:     Exchange{Status: 200},
				Versions: []Version{
					{Type: "Encounter", ID: "srv-enc-2", VersionID: "1", Op: "create", Content: raw(storedEnc)},
					{Type: "Observation", ID: "srv-obs-2", VersionID: "1", Op: "create", Content: raw(storedObs)},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-ok-2": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Encounter":   {"create": 1},
			"Observation": {"create": 1},
		},
		Differs: []string{"Observation.valueQuantity"},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("partial transaction Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught || statusScore.Effect || statusScore.Record {
		t.Fatalf("http-status should miss partial transaction divergence: %+v", statusScore)
	}

	flagScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagAfter,
		Effect: &stored,
		Record: &bench.Record{Checked: true},
	})
	if !flagScore.Caught || flagScore.Flag != bench.FlagAfter || !flagScore.Effect || !flagScore.Record {
		t.Fatalf("expected after-write catch with true effect and checked record: %+v", flagScore)
	}
}

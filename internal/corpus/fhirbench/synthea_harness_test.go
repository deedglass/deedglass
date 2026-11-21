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
					"resourceType":  "Observation",
					"identifier":    []any{map[string]any{"system": "urn:example:synthea", "value": "obs-2"}},
					"status":        "final",
					"subject":       map[string]any{"reference": "Patient/syn-ok-2"},
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

// TestSyntheaConditionalCreateNoOpFacts tests that a conditional create on a Synthea
// Observation matching an existing record (HTTP 200, 0 versions created) correctly
// reflects no changes stored, while http-status misses the discrepancy.
func TestSyntheaConditionalCreateNoOpFacts(t *testing.T) {
	t.Parallel()

	obsPayload := map[string]any{
		"resourceType": "Observation",
		"status":       "final",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-hr-existing"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-1"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
	}

	// The agent sends POST Observation with If-None-Exist; server returns 200 with the existing record,
	// so 0 new versions are written to the database.
	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method:  "POST",
					Path:    "Observation",
					Headers: map[string]string{"If-None-Exist": "identifier=urn:example:synthea|obs-hr-existing"},
					Body:    raw(obsPayload),
				},
				Saw: Exchange{
					Status: 200,
					Body: raw(map[string]any{
						"resourceType": "Observation",
						"id":           "obs-existing-id",
						"status":       "preliminary",
					}),
				},
				Versions: nil,
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-patient-1": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("conditional create Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught {
		t.Fatalf("http-status should not catch conditional create match no-op: %+v", statusScore)
	}

	dgScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagAfter,
		Effect: &stored,
		Record: &bench.Record{Checked: true},
	})
	if !dgScore.Caught || dgScore.Flag != bench.FlagAfter || !dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to catch conditional create match: %+v", dgScore)
	}
}

// TestSyntheaScribeDischargeEncounterFacts simulates an AI Scribe encounter discharge bundle
// (Encounter, Condition, MedicationRequest) where an AccessPolicy silently strips the
// Condition note and a MedicationRequest references an unpersisted Patient ID.
func TestSyntheaScribeDischargeEncounterFacts(t *testing.T) {
	t.Parallel()

	bundle := map[string]any{
		"resourceType": "Bundle",
		"type":         "transaction",
		"entry": []any{
			map[string]any{
				"fullUrl": "urn:uuid:33333333-3333-4333-8333-333333333333",
				"request": map[string]any{"method": "POST", "url": "Encounter"},
				"resource": map[string]any{
					"resourceType": "Encounter",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "enc-scribe-1"}},
					"status":       "finished",
					"subject":      map[string]any{"reference": "Patient/syn-patient-ok"},
				},
			},
			map[string]any{
				"fullUrl": "urn:uuid:44444444-4444-4444-8444-444444444444",
				"request": map[string]any{"method": "POST", "url": "Condition"},
				"resource": map[string]any{
					"resourceType": "Condition",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "cond-scribe-1"}},
					"subject":      map[string]any{"reference": "Patient/syn-patient-ok"},
					"note":         []any{map[string]any{"text": "Confirmed Type 2 Diabetes"}},
				},
			},
			map[string]any{
				"fullUrl": "urn:uuid:55555555-5555-4555-8555-555555555555",
				"request": map[string]any{"method": "POST", "url": "MedicationRequest"},
				"resource": map[string]any{
					"resourceType": "MedicationRequest",
					"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "med-scribe-1"}},
					"status":       "active",
					"intent":       "order",
					"subject":      map[string]any{"reference": "Patient/syn-patient-missing"},
					"encounter":    map[string]any{"reference": "Encounter/enc-scribe-1"},
				},
			},
		},
	}

	storedEnc := map[string]any{
		"resourceType": "Encounter",
		"id":           "srv-enc-scribe-1",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "enc-scribe-1"}},
		"status":       "finished",
		"subject":      map[string]any{"reference": "Patient/syn-patient-ok"},
	}
	storedCond := map[string]any{
		"resourceType": "Condition",
		"id":           "srv-cond-scribe-1",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "cond-scribe-1"}},
		"subject":      map[string]any{"reference": "Patient/syn-patient-ok"},
		// note field dropped by server AccessPolicy
	}
	storedMed := map[string]any{
		"resourceType": "MedicationRequest",
		"id":           "srv-med-scribe-1",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "med-scribe-1"}},
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/syn-patient-missing"},
		"encounter":    map[string]any{"reference": "Encounter/srv-enc-scribe-1"},
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{Method: "POST", Path: "", Body: raw(bundle)},
				Saw:     Exchange{Status: 200},
				Versions: []Version{
					{Type: "Encounter", ID: "srv-enc-scribe-1", VersionID: "1", Op: "create", Content: raw(storedEnc)},
					{Type: "Condition", ID: "srv-cond-scribe-1", VersionID: "1", Op: "create", Content: raw(storedCond)},
					{Type: "MedicationRequest", ID: "srv-med-scribe-1", VersionID: "1", Op: "create", Content: raw(storedMed)},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{
				"Patient/syn-patient-ok":      true,
				"Patient/syn-patient-missing": false,
				"Encounter/srv-enc-scribe-1":  true,
			},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Condition":         {"create": 1},
			"Encounter":         {"create": 1},
			"MedicationRequest": {"create": 1},
		},
		Differs:  []string{"Condition.note", "MedicationRequest.encounter"},
		Dangling: map[string]int{"MedicationRequest.subject": 1},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("Scribe discharge encounter Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught {
		t.Fatalf("http-status should not catch silent note drop or dangling ref: %+v", statusScore)
	}

	dgScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagAfter,
		Effect: &stored,
		Record: &bench.Record{Checked: true},
	})
	if !dgScore.Caught || dgScore.Flag != bench.FlagAfter || !dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to catch Scribe divergence: %+v", dgScore)
	}
}

// TestSyntheaConcurrentLostUpdateOverwrite tests an agent performing a PUT Observation
// without an If-Match header after an intermediate writer updated the resource.
// Facts detects the concurrent overwrite (e.Overwrote["Observation"] == 1), while
// naive http-status (200 OK) misses the divergence completely.
func TestSyntheaConcurrentLostUpdateOverwrite(t *testing.T) {
	t.Parallel()

	obsPayload := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-hr-1",
		"status":       "final",
		"subject":      map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
	}

	storedObs := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-hr-1",
		"status":       "final",
		"subject":      map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
	}

	// Agent read version 1 (72 bpm), nurse updated to version 2 (75 bpm),
	// agent overwrites at version 3 back to 72 bpm without If-Match.
	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method: "PUT",
					Path:   "Observation/obs-hr-1",
					Body:   raw(obsPayload),
				},
				Read:  "1",
				Prior: "2",
				Saw:   Exchange{Status: 200},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        "obs-hr-1",
						VersionID: "3",
						Op:        "update",
						Content:   raw(storedObs),
					},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-patient-ok": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"update": 1},
		},
		Overwrote: map[string]int{"Observation": 1},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("concurrent lost update Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught {
		t.Fatalf("http-status should not catch lost update overwrite without If-Match: %+v", statusScore)
	}
}

// TestSyntheaReadOnlyNoteStrippedFacts tests an agent creating an Observation with a clinical note
// where server AccessPolicy silently drops the read-only note element upon write (returning HTTP 201 Created).
// Facts detects the difference (Observation.note in Differs), while naive http-status (201 Created)
// misses the divergence.
func TestSyntheaReadOnlyNoteStrippedFacts(t *testing.T) {
	t.Parallel()

	obsPayload := map[string]any{
		"resourceType": "Observation",
		"status":       "final",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-note-stripped-1"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
		"note": []any{map[string]any{"text": "Patient resting comfortably in triage"}},
	}

	storedObs := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-note-stripped-srv",
		"status":       "final",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-note-stripped-1"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
		// Note dropped silently on write by server access policy
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method: "POST",
					Path:   "Observation",
					Body:   raw(obsPayload),
				},
				Saw: Exchange{
					Status:   201,
					Location: "Observation/obs-note-stripped-srv/_history/1",
				},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        "obs-note-stripped-srv",
						VersionID: "1",
						Op:        "create",
						Content:   raw(storedObs),
					},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-patient-ok": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"create": 1},
		},
		Differs: []string{"Observation.note"},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("read-only note stripped Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught {
		t.Fatalf("http-status should not catch silently dropped note on 201 Created: %+v", statusScore)
	}

	dgScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagAfter,
		Effect: &stored,
		Record: &bench.Record{Checked: true},
	})
	if !dgScore.Caught || dgScore.Flag != bench.FlagAfter || !dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to catch silently dropped note: %+v", dgScore)
	}
}

// TestSyntheaAuthorReplacedDivergenceFacts tests an AI scribe creating an Observation with clinician attribution
// in meta.author (Practitioner/dr-patel), but the server overwrites meta.author with the client application identity
// (ClientApplication/synthea-scribe-app) while answering HTTP 201 Created.
// Facts detects the difference (Observation.meta.author in Differs), while naive http-status (201 Created)
// misses the author substitution.
func TestSyntheaAuthorReplacedDivergenceFacts(t *testing.T) {
	t.Parallel()

	obsPayload := map[string]any{
		"resourceType": "Observation",
		"status":       "preliminary",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-author-replaced-1"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 78,
			"unit":  "beats/minute",
		},
		"meta": map[string]any{
			"author": map[string]any{
				"reference": "Practitioner/dr-patel",
			},
		},
	}

	storedObs := map[string]any{
		"resourceType": "Observation",
		"id":           "obs-author-replaced-srv",
		"status":       "preliminary",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-author-replaced-1"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 78,
			"unit":  "beats/minute",
		},
		"meta": map[string]any{
			"author": map[string]any{
				"reference": "ClientApplication/synthea-scribe-app",
			},
		},
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method: "POST",
					Path:   "Observation",
					Body:   raw(obsPayload),
				},
				Saw: Exchange{
					Status:   201,
					Location: "Observation/obs-author-replaced-srv/_history/1",
				},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        "obs-author-replaced-srv",
						VersionID: "1",
						Op:        "create",
						Content:   raw(storedObs),
					},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-patient-ok": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"create": 1},
		},
		Differs: []string{"Observation.meta.author"},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("author replaced Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught {
		t.Fatalf("http-status should not catch server-replaced meta.author on 201 Created: %+v", statusScore)
	}

	dgScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagAfter,
		Effect: &stored,
		Record: &bench.Record{Checked: true},
	})
	if !dgScore.Caught || dgScore.Flag != bench.FlagAfter || !dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to catch server-replaced meta.author: %+v", dgScore)
	}
}

// TestSyntheaDeleteSoftHistoryKeptFacts tests an agent issuing a DELETE for an Observation
// entered on a patient. In standard FHIR R4 / Medplum, a DELETE is a soft delete: earlier
// versions remain readable in version history (_history / vread).
// While the agent reports "deleted 1 Observation" and http-status sees 200/204 with FlagNone (caught=false, effect=false),
// Facts detects HistoryKept: map[string]int{"Observation": 1}.
// Deedglass catches this upfront before execution (FlagBefore) via policy refusal, leaving an audit record.
func TestSyntheaDeleteSoftHistoryKeptFacts(t *testing.T) {
	t.Parallel()

	runWithoutGateway := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method: "DELETE",
					Path:   "Observation/syn-obs-delete-1",
				},
				Saw: Exchange{
					Status: 204,
				},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        "syn-obs-delete-1",
						VersionID: "2",
						Op:        "delete",
						Content: raw(map[string]any{
							"resourceType": "Observation",
							"id":           "syn-obs-delete-1",
							"meta": map[string]any{
								"versionId": "2",
								"deleted":   true,
							},
						}),
					},
				},
			},
		},
		Lookups: Lookups{
			EarlierVersions: map[string]int{"Observation/syn-obs-delete-1": 1},
		},
	}

	storedWithoutGateway := Facts(runWithoutGateway)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"delete": 1},
		},
		HistoryKept: map[string]int{
			"Observation": 1,
		},
	}
	if !Equal(storedWithoutGateway, wantStored) {
		t.Fatalf("delete soft history kept Facts mismatch:\n got %s\nwant %s", storedWithoutGateway.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(runWithoutGateway)
	statusScore := ScoreOf(true, storedWithoutGateway, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught || statusScore.Effect {
		t.Fatalf("http-status should not catch soft delete with history kept: %+v", statusScore)
	}

	// Deedglass refuses clinical deletion before execution via policy (FlagBefore)
	emptyStored := Effect{Changes: map[string]map[string]int{}}
	dgScore := ScoreOf(true, emptyStored, Report{
		Flag:   bench.FlagBefore,
		Effect: &emptyStored,
		Record: &bench.Record{Checked: true},
	})
	if !dgScore.Caught || dgScore.Flag != bench.FlagBefore || !dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to catch deletion before write: %+v", dgScore)
	}
}

// TestSyntheaRetryDuplicateDivergenceFacts tests scenario 16 (retry duplicate):
// An agent posts an Observation (e.g., heart rate on a patient). The server creates the
// resource, but the network connection drops before the client receives the response (Saw.Lost = true).
// The agent's retry logic then posts the identical Observation again; the non-idempotent
// FHIR POST create succeeds with HTTP 201 Created and creates a second distinct Observation.
// While the agent reports charting 1 reading, and http-status sees 1 successful create with FlagNone (caught=false, effect=false),
// Facts detects two distinct created versions (create: 2).
// Deedglass misses the diverging intent (FlagNone, caught=false), but reports the true stored effect (2 creates, effect=true)
// and leaves an offline-checkable record (Record.Checked=true), matching P-285 ("Missed (effect yes)").
func TestSyntheaRetryDuplicateDivergenceFacts(t *testing.T) {
	t.Parallel()

	obsPayload := map[string]any{
		"resourceType": "Observation",
		"status":       "preliminary",
		"identifier": []any{
			map[string]any{"system": "urn:example:fhir-bench:obs", "value": "syn-obs-retry-16"},
		},
		"category": []any{
			map[string]any{
				"coding": []any{
					map[string]any{
						"system": "http://terminology.hl7.org/CodeSystem/observation-category",
						"code":   "vital-signs",
					},
				},
			},
		},
		"code": map[string]any{
			"coding": []any{
				map[string]any{
					"system":  "http://loinc.org",
					"code":    "8867-4",
					"display": "Heart rate",
				},
			},
		},
		"subject":           map[string]any{"reference": "Patient/syn-patient-16"},
		"effectiveDateTime": "2026-10-21T09:30:00Z",
		"valueQuantity": map[string]any{
			"value":  72,
			"unit":   "beats/minute",
			"system": "http://unitsofmeasure.org",
			"code":   "/min",
		},
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method: "POST",
					Path:   "Observation",
					Body:   raw(obsPayload),
				},
				Saw: Exchange{
					Lost: true,
				},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        "syn-obs-16-a",
						VersionID: "1",
						Op:        "create",
						Content:   raw(obsPayload),
					},
				},
			},
			{
				Session: "agent",
				Request: Request{
					Method: "POST",
					Path:   "Observation",
					Body:   raw(obsPayload),
				},
				Saw: Exchange{
					Status: 201,
				},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        "syn-obs-16-b",
						VersionID: "1",
						Op:        "create",
						Content:   raw(obsPayload),
					},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-patient-16": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"create": 2},
		},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("retry duplicate Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	wantStatusEff := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"create": 1},
		},
	}
	if !Equal(statusEff, wantStatusEff) {
		t.Fatalf("statusEff mismatch:\n got %s\nwant %s", statusEff.String(), wantStatusEff.String())
	}

	// http-status misses: caught=false, and effect=false (missed that 2 were created)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught || statusScore.Effect {
		t.Fatalf("http-status should miss duplicate create and fail effect check: %+v", statusScore)
	}

	// Deedglass misses the diverging intent (FlagNone), but reports true stored effect (effect=true) and checked record
	dgScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagNone,
		Effect: &stored,
		Record: &bench.Record{Checked: true},
	})
	if dgScore.Caught || !dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to report true effect and record despite missing flag: %+v", dgScore)
	}
}

// TestSyntheaClientIDReplacedDivergenceFacts tests an agent creating an Observation
// with a client-chosen id so it can link to it later (pattern 06 in corpus).
// Per FHIR R4 (http.html#create), if an id is provided in a create (POST), the server
// SHALL ignore it and assign its own.
// The agent believes Observation.id is its client-chosen id, but stored version has server-assigned UUID.
// Facts detects Differs: []string{"Observation.id"}.
// http-status misses divergence and fails effect check.
// Deedglass baseline (without pre-create client id comparator) misses flag (FlagNone) and
// effect (effect=false), but leaves checked audit record (record=true).
func TestSyntheaClientIDReplacedDivergenceFacts(t *testing.T) {
	t.Parallel()

	clientChosenID := "syn-obs-client-chosen-id"
	serverAssignedID := "obs-server-assigned-uuid-42"

	obsPayload := map[string]any{
		"resourceType": "Observation",
		"id":           clientChosenID,
		"status":       "preliminary",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-hr-client-id"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
	}

	storedObs := map[string]any{
		"resourceType": "Observation",
		"id":           serverAssignedID,
		"status":       "preliminary",
		"identifier":   []any{map[string]any{"system": "urn:example:synthea", "value": "obs-hr-client-id"}},
		"code": map[string]any{
			"coding": []any{map[string]any{"system": "http://loinc.org", "code": "8867-4", "display": "Heart rate"}},
		},
		"subject": map[string]any{"reference": "Patient/syn-patient-ok"},
		"valueQuantity": map[string]any{
			"value": 72,
			"unit":  "beats/minute",
		},
	}

	run := &Run{
		Steps: []StepRecord{
			{
				Session: "agent",
				Request: Request{
					Method: "POST",
					Path:   "Observation",
					Body:   raw(obsPayload),
				},
				Saw: Exchange{
					Status:   201,
					Location: "Observation/" + serverAssignedID + "/_history/1",
				},
				Versions: []Version{
					{
						Type:      "Observation",
						ID:        serverAssignedID,
						VersionID: "1",
						Op:        "create",
						Content:   raw(storedObs),
					},
				},
			},
		},
		Lookups: Lookups{
			Exists: map[string]bool{"Patient/syn-patient-ok": true},
		},
	}

	stored := Facts(run)
	wantStored := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"create": 1},
		},
		Differs: []string{"Observation.id"},
	}
	if !Equal(stored, wantStored) {
		t.Fatalf("client-id-replaced Facts mismatch:\n got %s\nwant %s", stored.String(), wantStored.String())
	}

	statusEff, _ := StatusAccount(run)
	statusScore := ScoreOf(true, stored, Report{Flag: bench.FlagNone, Effect: &statusEff})
	if statusScore.Caught || statusScore.Effect {
		t.Fatalf("http-status should miss client id replacement divergence: %+v", statusScore)
	}

	// Deedglass without pre-create id comparator misses flag and effect, but leaves checked record
	deedglassReportEff := Effect{
		Changes: map[string]map[string]int{
			"Observation": {"create": 1},
		},
	}
	dgScore := ScoreOf(true, stored, Report{
		Flag:   bench.FlagNone,
		Effect: &deedglassReportEff,
		Record: &bench.Record{Checked: true},
	})
	if dgScore.Caught || dgScore.Effect || !dgScore.Record {
		t.Fatalf("expected deedglass to record checked audit record despite missing divergence flag: %+v", dgScore)
	}
}

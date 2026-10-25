package attack

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/fhir"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/packs"
	"github.com/deedglass/deedglass/internal/policy"
)

// Group S, India's national profiles (HC-P15, ADR-119). Library-level cases run by plain go test,
// like groups Y, T and M: each request goes through the real fhir connector's Enrich (which reads
// meta.profile and what a bundle holds, internal/connector/fhir/igclass) and then the pack exactly
// as the binary embeds it. The requests start from the published examples of the NRCeS FHIR IG for
// ABDM 6.5.0 (vendored under CC0-1.0 in igclass/testdata). Expected outcomes are in
// attack_meta.json, declared before this code, and never change here.

const sIG = "../internal/connector/fhir/igclass/testdata/ndhm.in-6.5.0/"

func sExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sIG, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return sDecode(t, raw)
}

func sDecode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func sPack(t *testing.T, name string) *policy.Compiled {
	t.Helper()
	src, ok := packs.Embedded().Files[name+"/"+name+".yaml"]
	if !ok {
		t.Fatalf("the binary embeds no %s pack", name)
	}
	c, diags := policy.Compile(src, policy.Options{})
	if c == nil {
		t.Fatalf("%s: %v", name, diags)
	}
	return c
}

// sConnector is a fhir connector that allows Bundle, Claim and the clinical types the cases use.
func sConnector(t *testing.T) (*fhir.Connector, *connector.Instance) {
	t.Helper()
	c := fhir.New()
	env := connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	raw, _ := json.Marshal(map[string]any{"base_url": "http://127.0.0.1:9/fhir", "auth": map[string]any{"kind": "none"},
		"allowed_resource_types": []string{"Bundle", "Claim", "Patient", "Task"}, "allowed_interactions": fhir.Interactions})
	norm, errs := c.Validate(context.Background(), env, raw)
	if len(errs) > 0 {
		t.Fatalf("Validate: %v", errs)
	}
	org := ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	con := ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7d")
	return c, connector.NewInstance(con, org, "ehr", fhir.Type, norm, "sha256:test", env, nil)
}

// sDecide runs one call through Enrich and the pack: the decision the Gate would take.
func sDecide(t *testing.T, pack *policy.Compiled, phi bool, tool string, args map[string]any) policy.Decision {
	t.Helper()
	c, inst := sConnector(t)
	specs, _ := c.Tools(context.Background(), inst)
	var spec connector.ToolSpec
	for _, s := range specs {
		if s.Name == tool {
			spec = s
		}
	}
	raw, _ := json.Marshal(args)
	var facts policy.FHIRFacts
	if m, err := c.Enrich(context.Background(), inst, spec, raw); err == nil {
		b, _ := json.Marshal(m["fhir"])
		if err := json.Unmarshal(b, &facts); err != nil {
			t.Fatal(err)
		}
	}
	side := "write"
	if tool == "read" {
		side = "read"
	} else if tool == "delete" {
		side = "destructive"
	}
	in := policy.Input{V: policy.InputVersion, Args: raw,
		Agent:   policy.AgentInput{AgentFacts: policy.AgentFacts{ID: "agt_s", Name: "hospital-bot", Labels: map[string]string{}, Status: policy.StatusActive}},
		Tool:    policy.ToolInput{ToolFacts: policy.ToolFacts{Name: "ehr." + tool, Connector: "ehr", ConnectorType: fhir.Type, SideEffect: side, Type: fhir.Type, Op: tool}},
		Org:     policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_01k6c9kb2zcjsyn7b8g2k1vqvp", Timezone: "Asia/Kolkata", PHIMode: phi}},
		Request: policy.RequestFacts{Mode: "gateway", FHIR: facts}, Time: policy.DefaultCaseTime}
	return policy.Evaluate(context.Background(), pack, in, policy.EvalOptions{})
}

func sCreate(res map[string]any) map[string]any {
	return map[string]any{"resource_type": res["resourceType"], "resource": res}
}

func sWant(t *testing.T, d policy.Decision, outcome policy.Outcome, rule string, required int) {
	t.Helper()
	if d.Outcome != outcome || d.RuleID != rule {
		t.Fatalf("got %s by %q (%s), want %s by %s", d.Outcome, d.RuleID, d.Reason, outcome, rule)
	}
	if outcome == policy.RequireApproval && (d.Approval == nil || d.Approval.Required != required) {
		t.Fatalf("approval %+v, want %d people", d.Approval, required)
	}
}

func stripProfiles(v any) {
	switch x := v.(type) {
	case map[string]any:
		if m, ok := x["meta"].(map[string]any); ok {
			delete(m, "profile")
		}
		for _, e := range x {
			stripProfiles(e)
		}
	case []any:
		for _, e := range x {
			stripProfiles(e)
		}
	}
}

func TestS01SettlementClaimBundle(t *testing.T) {
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(sExample(t, "Bundle-ClaimBundle-settlement-example-01")))
	sWant(t, d, policy.RequireApproval, "claim-settlement-two-people", 2)
}

func TestS02SettlementPlainClaim(t *testing.T) {
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(sExample(t, "Claim-Claim-settlement-example-01")))
	sWant(t, d, policy.RequireApproval, "claim-settlement-two-people", 2)
}

func TestS03EligibilityLabelOnAClaim(t *testing.T) {
	b := sExample(t, "Bundle-ClaimBundle-settlement-example-01")
	b["meta"].(map[string]any)["profile"] = []any{"https://nrces.in/ndhm/fhir/r4/StructureDefinition/CoverageEligibilityRequestBundle"}
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(b))
	sWant(t, d, policy.Deny, "nhcx-profile-mismatch", 0)
}

func TestS04ProfilesStripped(t *testing.T) {
	b := sExample(t, "Bundle-ClaimBundle-settlement-example-01")
	stripProfiles(b)
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(b))
	sWant(t, d, policy.RequireApproval, "claim-settlement-two-people", 2)
}

func TestS05LargePreauthBehindASmallOne(t *testing.T) {
	b := sDecode(t, []byte(`{"resourceType":"Bundle","type":"collection","entry":[
		{"resource":{"resourceType":"Claim","use":"preauthorization","total":{"value":100,"currency":"INR"}}},
		{"resource":{"resourceType":"Claim","use":"preauthorization","total":{"value":250000,"currency":"INR"}}}]}`))
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(b))
	sWant(t, d, policy.RequireApproval, "preauth-large-two-people", 2)
}

func TestS06NestedAndContainedClaims(t *testing.T) {
	pack := sPack(t, "india-health")
	nested := sDecode(t, []byte(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Bundle","type":"collection","entry":[
		{"resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Claim","use":"claim","total":{"value":500,"currency":"INR"}}}]}}]}}]}`))
	sWant(t, sDecide(t, pack, true, "create", sCreate(nested)), policy.RequireApproval, "claim-settlement-two-people", 2)
	task := sDecode(t, []byte(`{"resourceType":"Task","status":"requested","intent":"order","contained":[{"resourceType":"Claim","use":"claim","total":{"value":500,"currency":"INR"}}]}`))
	sWant(t, sDecide(t, pack, true, "create", sCreate(task)), policy.RequireApproval, "claim-settlement-two-people", 2)
}

func TestS07UnreadableClaims(t *testing.T) {
	pack := sPack(t, "india-health")
	for _, js := range []string{
		`{"resourceType":"Claim","use":"preauthorization","total":{"value":"100","currency":"INR"}}`,
		`{"resourceType":"Claim","use":"preauthorization","total":{"value":100,"currency":"XTS"}}`,
		`{"resourceType":"Claim","use":"settlement","total":{"value":100,"currency":"INR"}}`,
	} {
		sWant(t, sDecide(t, pack, true, "create", sCreate(sDecode(t, []byte(js)))), policy.RequireApproval, "claim-unreadable-two-people", 2)
	}
}

func TestS08PrescriptionAsDraftWellness(t *testing.T) {
	b := sExample(t, "Bundle-Prescription-example-06")
	for _, e := range b["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		if r["resourceType"] == "Composition" {
			r["meta"].(map[string]any)["profile"] = []any{"https://nrces.in/ndhm/fhir/r4/StructureDefinition/WellnessRecord"}
			r["status"] = "preliminary"
			delete(r, "type")
		}
	}
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(b))
	sWant(t, d, policy.RequireApproval, "orders-need-clinician", 1)
}

func TestS09DischargeSummaryProfileStripped(t *testing.T) {
	b := sExample(t, "Bundle-DischargeSummary-example-04")
	stripProfiles(b)
	for _, e := range b["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		// The example's discharge medication is an order; leave only the record so the record rule decides.
		if r["resourceType"] == "MedicationRequest" || r["resourceType"] == "ServiceRequest" {
			r["resourceType"] = "Basic"
		}
	}
	d := sDecide(t, sPack(t, "india-health"), true, "create", sCreate(b))
	sWant(t, d, policy.RequireApproval, "abdm-record-needs-clinician", 1)
}

func TestS10PHIModeOff(t *testing.T) {
	d := sDecide(t, sPack(t, "india-health"), false, "read", map[string]any{"resource_type": "Patient", "id": "pat-synthetic-1"})
	sWant(t, d, policy.Deny, "phi-mode-first", 0)
}

func TestS11HealthcareFHIRClaimBundle(t *testing.T) {
	d := sDecide(t, sPack(t, "healthcare-fhir"), true, "create", sCreate(sExample(t, "Bundle-ClaimBundle-preauthorization-example-01")))
	sWant(t, d, policy.RequireApproval, "claims-four-eyes", 2)
}

func TestS12DeleteRecordBundle(t *testing.T) {
	d := sDecide(t, sPack(t, "india-health"), true, "delete", map[string]any{"resource_type": "Bundle", "id": "opconsult-1"})
	sWant(t, d, policy.Deny, "no-delete-clinical", 0)
}

// TestSIGExamplesUnderIndiaHealth: IG examples decided by the pack, plain Claims and bundles alike;
// none runs without a person except an eligibility request or a task. Records that carry an order
// (an ImmunizationRecommendation, a ServiceRequest) wait under orders-need-clinician.
func TestSIGExamplesUnderIndiaHealth(t *testing.T) {
	pack := sPack(t, "india-health")
	want := map[string]string{
		"Bundle-ClaimBundle-preauthorization-example-01":               "preauth-one-person",
		"Claim-Claim-preauthorization-example-01":                      "preauth-one-person",
		"Bundle-ClaimBundle-predetermination-example-01":               "preauth-one-person",
		"Claim-Claim-predetermination-example-01":                      "preauth-one-person",
		"Bundle-ClaimBundle-enhancement-example-01":                    "preauth-one-person",
		"Claim-Claim-enhancement-example-01":                           "preauth-one-person",
		"Bundle-ClaimBundle-settlement-example-01":                     "claim-settlement-two-people",
		"Claim-Claim-settlement-example-01":                            "claim-settlement-two-people",
		"Bundle-ClaimResponseBundle-settlement-example-01":             "payer-side-two-people",
		"Bundle-CoverageEligibilityRequestBundle-benefits-example-01":  "verify-levels",
		"Bundle-CoverageEligibilityResponseBundle-benefits-example-01": "coverage-read-only",
		"Bundle-InsurancePlanBundle-example-01":                        "coverage-read-only",
		"Bundle-TaskBundleForPaymentNoticeRequest-example-01":          "payer-side-two-people",
		"Bundle-TaskBundleForSearchRequest-example-01":                 "verify-levels",
		"Bundle-OPConsultNote-example-05":                              "orders-need-clinician",
		"Bundle-Prescription-example-06":                               "orders-need-clinician",
		"Bundle-WellnessRecord-example-01":                             "abdm-record-needs-clinician",
		"Bundle-ImmunizationRecord-example-07":                         "orders-need-clinician",
		"Bundle-DiagnosticReport-Lab-example-03":                       "orders-need-clinician",
		"Bundle-HealthDocumentRecord-example-01":                       "abdm-record-needs-clinician",
		"Bundle-InvoiceRecord-example-01":                              "billing-two-people",
	}
	for name, rule := range want {
		d := sDecide(t, pack, true, "create", sCreate(sExample(t, name)))
		if d.RuleID != rule {
			t.Errorf("%s: %s by %q, want %s", name, d.Outcome, d.RuleID, rule)
		}
	}
}

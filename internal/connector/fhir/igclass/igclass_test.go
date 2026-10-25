package igclass

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const fixtures = "testdata/ndhm.in-6.5.0"

func load(t testing.TB, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtures, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // as the fhir connector decodes arguments
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

type want struct {
	bundle, record, status, nhcx string
	claims                       int64
	use                          string
	uses                         []string
	amount                       int64
	currency                     string
}

// TestIGExamples: every published example of the guide (vendored under CC0-1.0, SOURCES.md) is
// classified as the guide names it.
func TestIGExamples(t *testing.T) {
	doc := func(record string) want { return want{bundle: "document", record: record, status: "final"} }
	pa := []string{"preauthorization"}
	cases := map[string]want{
		"Bundle-OPConsultNote-example-05":                  doc("OPConsultRecord"),
		"Bundle-DischargeSummary-example-04":               doc("DischargeSummaryRecord"),
		"Bundle-Prescription-example-06":                   doc("PrescriptionRecord"),
		"Bundle-DiagnosticReport-Lab-example-03":           doc("DiagnosticReportRecord"),
		"Bundle-DiagnosticReport-Imaging-DCM-example-01":   doc("DiagnosticReportRecord"),
		"Bundle-DiagnosticReport-Imaging-Media-example-02": doc("DiagnosticReportRecord"),
		"Bundle-ImmunizationRecord-example-07":             doc("ImmunizationRecord"),
		"Bundle-ImmunizationRecord-for-WHO-DDCC":           doc("ImmunizationRecord"),
		"Bundle-HealthDocumentRecord-example-01":           doc("HealthDocumentRecord"),
		"Bundle-WellnessRecord-example-01":                 doc("WellnessRecord"),
		"Bundle-InvoiceRecord-example-01":                  doc("InvoiceRecord"),

		"Bundle-ClaimBundle-preauthorization-example-01":         {bundle: "collection", nhcx: "ClaimBundle", claims: 1, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Bundle-ClaimBundle-predetermination-example-01":         {bundle: "collection", nhcx: "ClaimBundle", claims: 1, use: "predetermination", uses: []string{"predetermination"}, amount: 7000000, currency: "inr"},
		"Bundle-ClaimBundle-enhancement-example-01":              {bundle: "collection", nhcx: "ClaimBundle", claims: 2, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Bundle-ClaimBundle-settlement-example-01":               {bundle: "collection", nhcx: "ClaimBundle", claims: 3, use: "claim", uses: []string{"claim", "preauthorization"}, amount: 9000000, currency: "inr"},
		"Bundle-ClaimResponseBundle-preauthorization-example-01": {bundle: "collection", nhcx: "ClaimResponseBundle", claims: 1, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Bundle-ClaimResponseBundle-predetermination-example-01": {bundle: "collection", nhcx: "ClaimResponseBundle", claims: 1, use: "predetermination", uses: []string{"predetermination"}, amount: 7000000, currency: "inr"},
		"Bundle-ClaimResponseBundle-enhancement-example-01":      {bundle: "collection", nhcx: "ClaimResponseBundle", claims: 2, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Bundle-ClaimResponseBundle-settlement-example-01":       {bundle: "collection", nhcx: "ClaimResponseBundle", claims: 3, use: "claim", uses: []string{"claim", "preauthorization"}, amount: 9000000, currency: "inr"},

		"Bundle-CoverageEligibilityRequestBundle-auth-requirement-example-01":   {bundle: "collection", nhcx: "CoverageEligibilityRequestBundle"},
		"Bundle-CoverageEligibilityRequestBundle-benefits-example-01":           {bundle: "collection", nhcx: "CoverageEligibilityRequestBundle"},
		"Bundle-CoverageEligibilityRequestBundle-discovery-example-01":          {bundle: "collection", nhcx: "CoverageEligibilityRequestBundle"},
		"Bundle-CoverageEligibilityRequestBundle-validation-example-01":         {bundle: "collection", nhcx: "CoverageEligibilityRequestBundle"},
		"Bundle-CoverageEligibilityResponseBundle-auth-requirements-example-01": {bundle: "collection", nhcx: "CoverageEligibilityResponseBundle"},
		"Bundle-CoverageEligibilityResponseBundle-benefits-example-01":          {bundle: "collection", nhcx: "CoverageEligibilityResponseBundle"},
		"Bundle-CoverageEligibilityResponseBundle-discovery-example-01":         {bundle: "collection", nhcx: "CoverageEligibilityResponseBundle"},
		"Bundle-CoverageEligibilityResponseBundle-validation-example-01":        {bundle: "collection", nhcx: "CoverageEligibilityResponseBundle"},
		"Bundle-InsurancePlanBundle-example-01":                                 {bundle: "collection", nhcx: "InsurancePlanBundle"},
		"Bundle-TaskBundleForCommunicationRequest-example-01":                   {bundle: "collection", nhcx: "TaskBundle", claims: 1, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Bundle-TaskBundleForCommunicationResponse-example-01":                  {bundle: "collection", nhcx: "TaskBundle", claims: 1, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Bundle-TaskBundleForInsurancePlanRequest-example-01":                   {bundle: "collection", nhcx: "TaskBundle"},
		"Bundle-TaskBundleForPaymentNoticeRequest-example-01":                   {bundle: "collection", nhcx: "TaskBundle"},
		"Bundle-TaskBundleForPaymentNoticeResponse-example-01":                  {bundle: "collection", nhcx: "TaskBundle"},
		"Bundle-TaskBundleForReprocessRequest-example-01":                       {bundle: "collection", nhcx: "TaskBundle"},
		"Bundle-TaskBundleForReprocessResponse-example-01":                      {bundle: "collection", nhcx: "TaskBundle", claims: 3, use: "claim", uses: []string{"claim", "preauthorization"}, amount: 9000000, currency: "inr"},
		"Bundle-TaskBundleForSearchRequest-example-01":                          {bundle: "collection", nhcx: "TaskBundle"},
		"Bundle-TaskBundleForSearchResponse-example-01":                         {bundle: "collection", nhcx: "TaskBundle", claims: 3, use: "claim", uses: []string{"claim", "preauthorization"}, amount: 9000000, currency: "inr"},

		// Plain FHIR Claims, no bundle.
		"Claim-Claim-preauthorization-example-01": {claims: 1, use: "preauthorization", uses: pa, amount: 7000000, currency: "inr"},
		"Claim-Claim-predetermination-example-01": {claims: 1, use: "predetermination", uses: []string{"predetermination"}, amount: 7000000, currency: "inr"},
		"Claim-Claim-enhancement-example-01":      {claims: 1, use: "preauthorization", uses: pa, amount: 2000000, currency: "inr"},
		"Claim-Claim-settlement-example-01":       {claims: 1, use: "claim", uses: []string{"claim"}, amount: 9000000, currency: "inr"},
	}
	files, _ := filepath.Glob(filepath.Join(fixtures, "*.json"))
	if len(files) != len(cases) {
		t.Errorf("%d fixtures, %d cases: every vendored example needs a row", len(files), len(cases))
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			f := Classify(load(t, name))
			got := want{f.BundleType, f.Record, f.RecordStatus, f.NHCX, f.Claims, f.ClaimUse, f.ClaimUses, f.ClaimAmount, f.ClaimCurrency}
			if got.bundle != w.bundle || got.record != w.record || got.status != w.status || got.nhcx != w.nhcx ||
				got.claims != w.claims || got.use != w.use || !slices.Equal(got.uses, w.uses) || got.amount != w.amount || got.currency != w.currency {
				t.Errorf("got  %+v\nwant %+v", got, w)
			}
			if f.ProfileMismatch {
				t.Error("an IG example is never a profile mismatch")
			}
			if w.bundle != "" && len(f.EntryTypes) == 0 {
				t.Error("a bundle names what it holds")
			}
		})
	}
}

func TestEntryTypes(t *testing.T) {
	f := Classify(load(t, "Bundle-ClaimBundle-preauthorization-example-01"))
	if strings.Join(f.EntryTypes, ",") != "Claim,Coverage,Organization,Patient,Practitioner" {
		t.Fatal(f.EntryTypes)
	}
	f = Classify(load(t, "Bundle-Prescription-example-06"))
	if !slices.Contains(f.EntryTypes, "MedicationRequest") || !slices.Contains(f.EntryTypes, "Composition") {
		t.Fatal(f.EntryTypes)
	}
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestLabelsDontWin: what a bundle holds counts for more than its profile, and claims can't hide.
func TestLabelsDontWin(t *testing.T) {
	const p = `"meta":{"profile":["https://nrces.in/ndhm/fhir/r4/StructureDefinition/`
	cases := []struct {
		name, js string
		check    func(Facts) bool
	}{
		{"eligibility label on a claim", `{"resourceType":"Bundle","type":"collection",` + p + `CoverageEligibilityRequestBundle"]},"entry":[{"resource":{"resourceType":"Claim","use":"claim","total":{"value":500000,"currency":"INR"}}}]}`,
			func(f Facts) bool {
				return f.NHCX == "ClaimBundle" && f.ProfileMismatch && f.ClaimUse == "claim" && f.ClaimAmount == 50000000
			}},
		{"no profile, claim first", `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Claim","use":"preauthorization","total":{"value":10,"currency":"INR"}}}]}`,
			func(f Facts) bool { return f.NHCX == "ClaimBundle" && !f.ProfileMismatch }},
		{"claim bundle label, nothing NHCX inside", `{"resourceType":"Bundle","type":"collection",` + p + `ClaimBundle|6.5.0"]},"entry":[{"resource":{"resourceType":"Patient"}}]}`,
			func(f Facts) bool { return f.NHCX == "ClaimBundle" && f.ProfileMismatch && f.Claims == 0 }},
		{"a large claim behind a small one", `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Claim","use":"preauthorization","total":{"value":100,"currency":"INR"}}},{"resource":{"resourceType":"Claim","use":"claim","total":{"value":900000.5,"currency":"INR"}}}]}`,
			func(f Facts) bool {
				return f.ClaimUse == "preauthorization" && slices.Equal(f.ClaimUses, []string{"claim", "preauthorization"}) && f.ClaimAmount == 90000050
			}},
		{"a claim nested two bundles down", `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Claim","use":"claim","total":{"value":1,"currency":"INR"}}}]}}]}}]}`,
			func(f Facts) bool {
				return f.Claims == 1 && f.ClaimUse == "claim" && slices.Contains(f.EntryTypes, "Claim")
			}},
		{"a contained claim", `{"resourceType":"Task","contained":[{"resourceType":"Claim","use":"claim"}]}`,
			func(f Facts) bool { return f.Claims == 1 && f.ClaimAmount == -1 }},
		{"mixed currencies", `{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Claim","use":"claim","total":{"value":1,"currency":"INR"}}},{"resource":{"resourceType":"Claim","use":"claim","total":{"value":1,"currency":"USD"}}}]}`,
			func(f Facts) bool { return f.ClaimAmount == -1 && f.ClaimCurrency == "" }},
		{"unknown currency", `{"resourceType":"Claim","use":"claim","total":{"value":1,"currency":"XTS"}}`,
			func(f Facts) bool { return f.ClaimAmount == -1 }},
		{"total as a string", `{"resourceType":"Claim","use":"claim","total":{"value":"1","currency":"INR"}}`,
			func(f Facts) bool { return f.ClaimAmount == -1 }},
		{"negative total", `{"resourceType":"Claim","use":"claim","total":{"value":-5,"currency":"INR"}}`,
			func(f Facts) bool { return f.ClaimAmount == -1 }},
		{"odd use", `{"resourceType":"Claim","use":"settlement","total":{"value":1,"currency":"INR"}}`,
			func(f Facts) bool { return f.ClaimUse == "other" }},
		{"sub-paisa rounds up", `{"resourceType":"Claim","use":"claim","total":{"value":0.001,"currency":"INR"}}`,
			func(f Facts) bool { return f.ClaimAmount == 1 }},
		{"a record by its SNOMED type when the profile is missing", `{"resourceType":"Bundle","type":"document","entry":[{"resource":{"resourceType":"Composition","status":"preliminary","type":{"coding":[{"system":"http://snomed.info/sct","code":"373942005"}]}}}]}`,
			func(f Facts) bool {
				return f.Record == "DischargeSummaryRecord" && f.RecordStatus == "preliminary" && f.NHCX == ""
			}},
		{"a bare Composition", `{"resourceType":"Composition","status":"final",` + p + `OPConsultRecord"]}}`,
			func(f Facts) bool { return f.Record == "OPConsultRecord" && f.BundleType == "" }},
		{"another guide's URL", `{"resourceType":"Composition",` + `"meta":{"profile":["https://example.org/StructureDefinition/OPConsultRecord"]}}`,
			func(f Facts) bool { return f.Record == "" }},
	}
	for _, c := range cases {
		if f := Classify(mustJSON(t, c.js)); !c.check(f) {
			t.Errorf("%s: %+v", c.name, f)
		}
	}
	if f := Classify(nil); f.Claims != 0 || f.BundleType != "" {
		t.Fatal(f)
	}
}

func TestKind(t *testing.T) {
	for in, w := range map[string]string{
		"https://nrces.in/ndhm/fhir/r4/StructureDefinition/ClaimBundle":       "ClaimBundle",
		"https://nrces.in/ndhm/fhir/r4/StructureDefinition/ClaimBundle|6.5.0": "ClaimBundle",
		"http://nrces.in/ndhm/fhir/r4/StructureDefinition/ClaimBundle":        "",
		"https://nrces.in/ndhm/fhir/r4/StructureDefinition/":                  "",
		"https://nrces.in/ndhm/fhir/r4/StructureDefinition/a/b":               "",
	} {
		if g := Kind(in); g != w {
			t.Errorf("%s: %q", in, g)
		}
	}
}

// FuzzClassify: never panics, and stays within its limits on any JSON.
func FuzzClassify(f *testing.F) {
	f.Add(`{"resourceType":"Bundle","type":"collection","entry":[{"resource":{"resourceType":"Claim","use":"claim","total":{"value":1e300,"currency":"INR"}}}]}`)
	f.Add(`{"resourceType":"Claim","total":{"value":0.1,"currency":"jpy"}}`)
	f.Fuzz(func(t *testing.T, s string) {
		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()
		var m map[string]any
		if dec.Decode(&m) != nil {
			return
		}
		fc := Classify(m)
		if len(fc.EntryTypes) > maxEntryTypes || fc.ClaimAmount < -1 {
			t.Fatal(fc)
		}
	})
}

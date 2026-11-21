package pas

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Da Vinci Prior Authorization Support (PAS) profiles and thresholds (CMS-0057-F / STU2).
const (
	CanonBase                = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/"
	ProfilePASRequestBundle  = CanonBase + "profile-pas-request-bundle"
	ProfilePASClaim          = CanonBase + "profile-claim"
	ProfilePASClaimUpdate    = CanonBase + "profile-claim-update"
	ProfilePASResponseBundle = CanonBase + "profile-pas-response-bundle"
	ProfilePASClaimResponse  = CanonBase + "profile-claimresponse"

	// Da Vinci CRD and DTR profile constants (CMS-0057-F)
	CanonCRDBase                         = "http://hl7.org/fhir/us/davinci-crd/StructureDefinition/"
	ProfileCRDRequestBundle              = CanonCRDBase + "profile-coveragerequirement-discovery"
	ProfileCRDCoverageEligibilityRequest = CanonCRDBase + "profile-coverageeligibilityrequest"

	CanonDTRBase                    = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/"
	ProfileDTRQuestionnaireResponse = CanonDTRBase + "profile-dtr-questionnaireresponse"

	// ThresholdLargePreauthUSD is $5,000 USD matching policy rule pas-preauth-two-people.
	ThresholdLargePreauthUSD float64 = 5000.0

	NPISystem = "http://hl7.org/fhir/sid/us-npi"
)

// MandatoryPASBundleResources lists resources required in a complete Da Vinci PAS request bundle.
var MandatoryPASBundleResources = []string{"Patient", "Coverage", "Organization"}

// ClinicalGroundingResources lists resources providing clinical rationale (Hazard CH-34).
var ClinicalGroundingResources = []string{"Condition", "DocumentReference", "Procedure", "ServiceRequest"}

// VerifyResult holds the structural, administrative, and pre-submission audit verdict for a Da Vinci PAS bundle.
type VerifyResult struct {
	Valid                bool     `json:"valid"`
	BundleType           string   `json:"bundle_type"`
	ClaimUse             string   `json:"claim_use,omitempty"`
	ClaimAmountUSD       float64  `json:"claim_amount_usd,omitempty"`
	RequiresDualApproval bool     `json:"requires_dual_approval"`
	EntryCount           int      `json:"entry_count"`
	ItemCount            int      `json:"item_count,omitempty"`
	EntryTypes           []string `json:"entry_types"`
	MissingGrounding     bool     `json:"missing_grounding"`
	Errors               []string `json:"errors,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
}

// VerifyPASBundle verifies the structural conformance, mandatory entry completeness,
// NPI validity, currency adherence, line-item summation, and clinical grounding of a Da Vinci PAS request bundle.
func VerifyPASBundle(bundle map[string]any) (*VerifyResult, error) {
	if bundle == nil {
		return nil, errors.New("pas: nil bundle")
	}

	resType, _ := bundle["resourceType"].(string)
	if resType != "Bundle" {
		return &VerifyResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("expected resourceType Bundle, got %q", resType)},
		}, nil
	}

	bType, _ := bundle["type"].(string)
	result := &VerifyResult{
		Valid:      true,
		BundleType: bType,
		Errors:     make([]string, 0),
		Warnings:   make([]string, 0),
	}

	if bType != "collection" && bType != "transaction" {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("invalid bundle type %q; Da Vinci PAS requires 'collection' or 'transaction'", bType))
	}

	entries := getEntries(bundle)
	result.EntryCount = len(entries)
	if len(entries) == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, "bundle has no entries")
		return result, nil
	}

	firstEntry := entries[0]
	firstRes, _ := firstEntry["resource"].(map[string]any)
	firstType, _ := firstRes["resourceType"].(string)

	typeCounts := make(map[string]int)
	var hasProvider bool
	var hasGrounding bool

	for _, entry := range entries {
		r, ok := entry["resource"].(map[string]any)
		if ok {
			t, _ := r["resourceType"].(string)
			if t != "" {
				typeCounts[t]++
				if !slices.Contains(result.EntryTypes, t) {
					result.EntryTypes = append(result.EntryTypes, t)
				}
				if t == "Practitioner" || t == "PractitionerRole" {
					hasProvider = true
					validateNPI(r, result)
				}
				if slices.Contains(ClinicalGroundingResources, t) {
					hasGrounding = true
				}
			}
		}
	}
	slices.Sort(result.EntryTypes)

	if firstType != "Claim" {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("first entry in Da Vinci PAS request bundle must be Claim, got %q", firstType))
	} else {
		verifyClaimDetails(firstRes, result)
	}

	// Mandatory resources check
	for _, mand := range MandatoryPASBundleResources {
		if typeCounts[mand] == 0 {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("missing mandatory Da Vinci PAS resource %q", mand))
		}
	}

	if !hasProvider && typeCounts["Organization"] == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, "missing provider reference (expected Practitioner, PractitionerRole, or Organization)")
	}

	// Clinical grounding check (Hazard CH-34: phantom order detection)
	if !hasGrounding {
		result.MissingGrounding = true
		result.Warnings = append(result.Warnings, "CH-34: bundle lacks clinical grounding attachment (Condition, Procedure, or DocumentReference)")
	}

	if len(result.Errors) > 0 {
		result.Valid = false
	}
	return result, nil
}

func verifyClaimDetails(claim map[string]any, result *VerifyResult) {
	use, _ := claim["use"].(string)
	result.ClaimUse = use
	if use != "preauthorization" && use != "predetermination" {
		result.Errors = append(result.Errors, fmt.Sprintf("invalid claim use %q; Da Vinci PAS requires 'preauthorization' or 'predetermination'", use))
	}

	// Total amount validation
	total, ok := claim["total"].(map[string]any)
	if !ok || total == nil {
		result.Errors = append(result.Errors, "claim is missing total amount")
		result.RequiresDualApproval = true
		return
	}

	currency, _ := total["currency"].(string)
	if !strings.EqualFold(currency, "USD") {
		result.Errors = append(result.Errors, fmt.Sprintf("invalid claim currency %q; US Da Vinci PAS requires USD", currency))
	}

	totalVal, ok := parseAmountFloat(total["value"])
	if !ok || totalVal < 0 {
		result.Errors = append(result.Errors, "claim has invalid or negative total amount")
		result.RequiresDualApproval = true
		return
	}
	result.ClaimAmountUSD = totalVal

	// Threshold evaluation under policy rule pas-preauth-two-people ($5,000+ USD)
	if totalVal >= ThresholdLargePreauthUSD {
		result.RequiresDualApproval = true
	}

	// Line items verification
	items, _ := claim["item"].([]any)
	result.ItemCount = len(items)

	if len(items) == 0 {
		result.Errors = append(result.Errors, "Da Vinci PAS claim requires at least one item line")
		return
	}

	var computedTotal float64
	itemSeqs := make(map[int]bool)

	for i, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		seq, _ := parseSequence(item["sequence"])
		if seq <= 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("item[%d] missing positive sequence number", i))
		} else {
			if itemSeqs[seq] {
				result.Errors = append(result.Errors, fmt.Sprintf("duplicate item sequence %d", seq))
			}
			itemSeqs[seq] = true
		}

		svc, _ := item["productOrService"].(map[string]any)
		if svc == nil || len(svc) == 0 {
			result.Errors = append(result.Errors, fmt.Sprintf("item[%d] missing productOrService coding", i))
		}

		if net, ok := item["net"].(map[string]any); ok {
			if itemAmt, ok := parseAmountFloat(net["value"]); ok {
				computedTotal += itemAmt
			}
		} else if unitPrice, ok := item["unitPrice"].(map[string]any); ok {
			qtyVal := 1.0
			if qty, ok := item["quantity"].(map[string]any); ok {
				if q, ok := parseAmountFloat(qty["value"]); ok {
					qtyVal = q
				}
			}
			if up, ok := parseAmountFloat(unitPrice["value"]); ok {
				computedTotal += up * qtyVal
			}
		}
	}

	// Check if computed total matches declared total (allow 1 cent precision)
	if math.Abs(computedTotal-totalVal) > 0.01 {
		result.Errors = append(result.Errors, fmt.Sprintf("line items sum ($%.2f) does not match declared claim total ($%.2f)", computedTotal, totalVal))
	}
}

func validateNPI(providerRes map[string]any, result *VerifyResult) {
	idents, ok := providerRes["identifier"].([]any)
	if !ok {
		return
	}
	for _, raw := range idents {
		ident, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sys, _ := ident["system"].(string)
		val, _ := ident["value"].(string)
		if sys == NPISystem || strings.Contains(strings.ToLower(sys), "us-npi") {
			trimmed := strings.TrimSpace(val)
			if len(trimmed) != 10 || !isAllDigits(trimmed) {
				result.Errors = append(result.Errors, fmt.Sprintf("invalid National Provider Identifier (NPI) %q; must be 10 digits", val))
			}
		}
	}
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func parseAmountFloat(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		clean := strings.TrimSpace(strings.ReplaceAll(val, "$", ""))
		f, err := strconv.ParseFloat(clean, 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func parseSequence(v any) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

// CRDVerifyResult holds the verification verdict for a Da Vinci CRD bundle.
type CRDVerifyResult struct {
	Valid           bool     `json:"valid"`
	BundleType      string   `json:"bundle_type"`
	EntryCount      int      `json:"entry_count"`
	EntryTypes      []string `json:"entry_types"`
	HasPatient      bool     `json:"has_patient"`
	HasCoverage     bool     `json:"has_coverage"`
	HasProvider     bool     `json:"has_provider"`
	Purpose         []string `json:"purpose,omitempty"`
	AuthRequirement string   `json:"auth_requirement,omitempty"`
	Errors          []string `json:"errors,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

// DTRVerifyResult holds the verification verdict for a Da Vinci DTR documentation template.
type DTRVerifyResult struct {
	Valid                   bool     `json:"valid"`
	Status                  string   `json:"status"` // "in-progress", "completed", "amended"
	RequiresClinicianReview bool     `json:"requires_clinician_review"`
	QuestionnaireURI        string   `json:"questionnaire_uri,omitempty"`
	SubjectReference        string   `json:"subject_reference,omitempty"`
	AnswerCount             int      `json:"answer_count"`
	Errors                  []string `json:"errors,omitempty"`
	Warnings                []string `json:"warnings,omitempty"`
}

// VerifyCRDBundle checks a Da Vinci CRD (Coverage Requirements Discovery) bundle.
func VerifyCRDBundle(bundle map[string]any) (*CRDVerifyResult, error) {
	if bundle == nil {
		return nil, errors.New("crd: nil bundle")
	}

	resType, _ := bundle["resourceType"].(string)
	if resType != "Bundle" {
		return &CRDVerifyResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("expected resourceType Bundle, got %q", resType)},
		}, nil
	}

	bType, _ := bundle["type"].(string)
	res := &CRDVerifyResult{
		Valid:      true,
		BundleType: bType,
		Errors:     make([]string, 0),
		Warnings:   make([]string, 0),
	}

	entries := getEntries(bundle)
	res.EntryCount = len(entries)
	if len(entries) == 0 {
		res.Valid = false
		res.Errors = append(res.Errors, "bundle has no entries")
		return res, nil
	}

	var hasEligReq bool
	for _, entry := range entries {
		r, ok := entry["resource"].(map[string]any)
		if !ok {
			continue
		}
		t, _ := r["resourceType"].(string)
		if t != "" && !slices.Contains(res.EntryTypes, t) {
			res.EntryTypes = append(res.EntryTypes, t)
		}
		switch t {
		case "CoverageEligibilityRequest":
			hasEligReq = true
			if purposes, ok := r["purpose"].([]any); ok {
				for _, p := range purposes {
					if ps, ok := p.(string); ok {
						res.Purpose = append(res.Purpose, ps)
					}
				}
			}
			if patient, ok := r["patient"].(map[string]any); ok && patient["reference"] != nil {
				res.HasPatient = true
			}
			if ins, ok := r["insurance"].([]any); ok && len(ins) > 0 {
				res.HasCoverage = true
			}
		case "Patient":
			res.HasPatient = true
		case "Coverage":
			res.HasCoverage = true
		case "Practitioner", "PractitionerRole", "Organization":
			res.HasProvider = true
			validateCRDNPI(r, res)
		}
	}
	slices.Sort(res.EntryTypes)

	if !hasEligReq {
		res.Valid = false
		res.Errors = append(res.Errors, "missing CoverageEligibilityRequest resource")
	}
	if !res.HasPatient {
		res.Valid = false
		res.Errors = append(res.Errors, "missing Patient resource or reference")
	}
	if !res.HasCoverage {
		res.Valid = false
		res.Errors = append(res.Errors, "missing Coverage resource or insurance reference")
	}
	if !res.HasProvider {
		res.Valid = false
		res.Errors = append(res.Errors, "missing provider reference")
	}

	if len(res.Errors) > 0 {
		res.Valid = false
	}
	return res, nil
}

func validateCRDNPI(providerRes map[string]any, result *CRDVerifyResult) {
	idents, ok := providerRes["identifier"].([]any)
	if !ok {
		return
	}
	for _, raw := range idents {
		ident, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		sys, _ := ident["system"].(string)
		val, _ := ident["value"].(string)
		if sys == NPISystem || strings.Contains(strings.ToLower(sys), "us-npi") {
			trimmed := strings.TrimSpace(val)
			if len(trimmed) != 10 || !isAllDigits(trimmed) {
				result.Errors = append(result.Errors, fmt.Sprintf("invalid National Provider Identifier (NPI) %q; must be 10 digits", val))
			}
		}
	}
}

// VerifyDTRQuestionnaireResponse validates a Da Vinci DTR QuestionnaireResponse resource.
func VerifyDTRQuestionnaireResponse(qr map[string]any) (*DTRVerifyResult, error) {
	if qr == nil {
		return nil, errors.New("dtr: nil questionnaire response")
	}
	resType, _ := qr["resourceType"].(string)
	if resType != "QuestionnaireResponse" {
		return &DTRVerifyResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("expected resourceType QuestionnaireResponse, got %q", resType)},
		}, nil
	}

	status, _ := qr["status"].(string)
	qURI, _ := qr["questionnaire"].(string)
	subjectRef := ""
	if subj, ok := qr["subject"].(map[string]any); ok {
		subjectRef, _ = subj["reference"].(string)
	}

	res := &DTRVerifyResult{
		Valid:            true,
		Status:           status,
		QuestionnaireURI: qURI,
		SubjectReference: subjectRef,
		Errors:           make([]string, 0),
		Warnings:         make([]string, 0),
	}

	if qURI == "" {
		res.Valid = false
		res.Errors = append(res.Errors, "missing questionnaire canonical URI")
	}
	if subjectRef == "" {
		res.Valid = false
		res.Errors = append(res.Errors, "missing subject reference (expected Patient)")
	}

	items, _ := qr["item"].([]any)
	res.AnswerCount = countDTRAnswers(items)

	switch status {
	case "in-progress":
		res.RequiresClinicianReview = false
	case "completed", "amended":
		res.RequiresClinicianReview = true
		if res.AnswerCount == 0 {
			res.Warnings = append(res.Warnings, "completed questionnaire has 0 answered items")
		}
	case "entered-in-error", "stopped":
		res.RequiresClinicianReview = false
	default:
		res.Valid = false
		res.Errors = append(res.Errors, fmt.Sprintf("unrecognized QuestionnaireResponse status %q", status))
	}

	if len(res.Errors) > 0 {
		res.Valid = false
	}
	return res, nil
}

func countDTRAnswers(items []any) int {
	count := 0
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if answers, ok := m["answer"].([]any); ok {
			count += len(answers)
		}
		if subItems, ok := m["item"].([]any); ok {
			count += countDTRAnswers(subItems)
		}
	}
	return count
}

// VerifyDTRBundle extracts and validates a QuestionnaireResponse from a Da Vinci DTR bundle.
func VerifyDTRBundle(bundle map[string]any) (*DTRVerifyResult, error) {
	if bundle == nil {
		return nil, errors.New("dtr: nil bundle")
	}
	resType, _ := bundle["resourceType"].(string)
	if resType == "QuestionnaireResponse" {
		return VerifyDTRQuestionnaireResponse(bundle)
	}
	if resType != "Bundle" {
		return &DTRVerifyResult{
			Valid:  false,
			Errors: []string{fmt.Sprintf("expected resourceType Bundle or QuestionnaireResponse, got %q", resType)},
		}, nil
	}

	entries := getEntries(bundle)
	for _, entry := range entries {
		r, ok := entry["resource"].(map[string]any)
		if !ok {
			continue
		}
		if rType, _ := r["resourceType"].(string); rType == "QuestionnaireResponse" {
			return VerifyDTRQuestionnaireResponse(r)
		}
	}

	return &DTRVerifyResult{
		Valid:  false,
		Errors: []string{"bundle contains no QuestionnaireResponse resource"},
	}, nil
}

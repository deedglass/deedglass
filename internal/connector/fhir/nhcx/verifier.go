package nhcx

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/deedglass/deedglass/internal/connector/fhir/igclass"
)

// NRCeS NHCX Implementation Guide profiles and limits (NRCeS IG 6.5.0).
const (
	CanonBase              = igclass.IGCanonBase
	ProfileClaimBundle     = CanonBase + "ClaimBundle"
	ProfilePreauthBundle   = CanonBase + "ClaimBundle"
	ProfileEligibilityReq  = CanonBase + "CoverageEligibilityRequestBundle"
	ProfileEligibilityResp = CanonBase + "CoverageEligibilityResponseBundle"
	ProfileClaimResponse   = CanonBase + "ClaimResponseBundle"
	ProfileInsurancePlan   = CanonBase + "InsurancePlanBundle"
	ProfileTaskBundle      = CanonBase + "TaskBundle"

	// LargePreauthThresholdPaise is INR 1,00,000 in minor units (10000000 paise).
	LargePreauthThresholdPaise int64 = 100000 * 100
)

// AllowedClaimUses defines the valid FHIR R4 claim uses under NHCX.
var AllowedClaimUses = []string{"preauthorization", "predetermination", "claim"}

// MandatoryClaimBundleResources lists resource types required in an NHCX ClaimBundle.
var MandatoryClaimBundleResources = []string{"Patient", "Coverage", "Condition"}

// VerifyResult holds the structural and pre-submission audit verdict for an NHCX bundle.
type VerifyResult struct {
	Valid                bool     `json:"valid"`
	BundleType           string   `json:"bundle_type"`
	NHCXKind             string   `json:"nhcx_kind"`
	ClaimUse             string   `json:"claim_use,omitempty"`
	ClaimAmountINR       int64    `json:"claim_amount_inr,omitempty"`
	RequiresDualApproval bool     `json:"requires_dual_approval"`
	EntryCount           int      `json:"entry_count"`
	ItemCount            int      `json:"item_count,omitempty"`
	EntryTypes           []string `json:"entry_types"`
	Errors               []string `json:"errors,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
}

// VerifyClaimBundle verifies the structural integrity, mandatory entry completeness,
// currency adherence, and line-item summation of an NHCX claim bundle.
func VerifyClaimBundle(bundle map[string]any) (*VerifyResult, error) {
	if bundle == nil {
		return nil, errors.New("nhcx: nil bundle")
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

	if bType != "collection" {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("invalid bundle type %q; NHCX requires 'collection'", bType))
	}

	// Classify bundle with igclass.
	ig := igclass.Classify(bundle)
	result.NHCXKind = ig.NHCX
	if ig.ProfileMismatch {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("profile mismatch: bundle declared profile does not match contained resources (%s)", ig.NHCX))
	}

	entries := getEntries(bundle)
	result.EntryCount = len(entries)
	if len(entries) == 0 {
		result.Valid = false
		result.Errors = append(result.Errors, "bundle has no entries")
		return result, nil
	}

	// First entry verification: for ClaimBundle, must be Claim.
	firstEntry := entries[0]
	firstRes, _ := firstEntry["resource"].(map[string]any)
	firstType, _ := firstRes["resourceType"].(string)

	typeCounts := make(map[string]int)
	for _, entry := range entries {
		r, ok := entry["resource"].(map[string]any)
		if ok {
			t, _ := r["resourceType"].(string)
			if t != "" {
				typeCounts[t]++
				if !slices.Contains(result.EntryTypes, t) {
					result.EntryTypes = append(result.EntryTypes, t)
				}
			}
		}
	}
	slices.Sort(result.EntryTypes)

	if ig.NHCX == "ClaimBundle" || firstType == "Claim" {
		if firstType != "Claim" {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("first entry in ClaimBundle must be Claim, got %q", firstType))
		}

		// Check mandatory entries.
		for _, mand := range MandatoryClaimBundleResources {
			if typeCounts[mand] == 0 {
				result.Valid = false
				result.Errors = append(result.Errors, fmt.Sprintf("missing mandatory NHCX resource %q", mand))
			}
		}

		// Organization or Practitioner must be present.
		if typeCounts["Organization"] == 0 && typeCounts["Practitioner"] == 0 {
			result.Valid = false
			result.Errors = append(result.Errors, "missing provider reference (expected Organization or Practitioner)")
		}

		// Claim inspection.
		if firstType == "Claim" {
			verifyClaimDetails(firstRes, result)
		}
	} else if ig.NHCX == "CoverageEligibilityRequestBundle" || firstType == "CoverageEligibilityRequest" {
		if firstType != "CoverageEligibilityRequest" {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("first entry in CoverageEligibilityRequestBundle must be CoverageEligibilityRequest, got %q", firstType))
		}
		if typeCounts["Patient"] == 0 {
			result.Valid = false
			result.Errors = append(result.Errors, "missing mandatory resource 'Patient' in eligibility request")
		}
		if typeCounts["Coverage"] == 0 {
			result.Valid = false
			result.Errors = append(result.Errors, "missing mandatory resource 'Coverage' in eligibility request")
		}
	}

	if len(result.Errors) > 0 {
		result.Valid = false
	}
	return result, nil
}

func verifyClaimDetails(claim map[string]any, result *VerifyResult) {
	use, _ := claim["use"].(string)
	result.ClaimUse = use
	if !slices.Contains(AllowedClaimUses, use) {
		result.Errors = append(result.Errors, fmt.Sprintf("invalid claim use %q; expected preauthorization, predetermination, or claim", use))
	}

	// Total validation.
	total, ok := claim["total"].(map[string]any)
	if !ok || total == nil {
		result.Errors = append(result.Errors, "claim is missing total amount")
		result.RequiresDualApproval = true
		return
	}

	currency, _ := total["currency"].(string)
	if !strings.EqualFold(currency, "INR") {
		result.Errors = append(result.Errors, fmt.Sprintf("invalid claim currency %q; NHCX requires INR", currency))
	}

	totalAmount, ok := parseMinorAmount(total["value"])
	if !ok || totalAmount < 0 {
		result.Errors = append(result.Errors, "claim has invalid or negative total amount")
		result.RequiresDualApproval = true
		return
	}
	result.ClaimAmountINR = totalAmount

	// Four-eyes rule calculation:
	// A final claim always needs dual approval.
	// A preauthorization at or above INR 1,00,000 needs dual approval.
	if use == "claim" {
		result.RequiresDualApproval = true
	} else if use == "preauthorization" && totalAmount >= LargePreauthThresholdPaise {
		result.RequiresDualApproval = true
	}

	// Line items verification.
	items, _ := claim["item"].([]any)
	result.ItemCount = len(items)

	if use == "claim" {
		if len(items) == 0 {
			result.Errors = append(result.Errors, "final claim requires at least one item line")
			return
		}

		var computedTotal int64
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

			// Verify productOrService codeable concept.
			svc, _ := item["productOrService"].(map[string]any)
			if svc == nil || len(svc) == 0 {
				result.Errors = append(result.Errors, fmt.Sprintf("item[%d] missing productOrService coding", i))
			}

			// Item net amount.
			if net, ok := item["net"].(map[string]any); ok {
				if itemAmt, ok := parseMinorAmount(net["value"]); ok {
					computedTotal += itemAmt
				}
			} else if unitPrice, ok := item["unitPrice"].(map[string]any); ok {
				qtyVal := 1.0
				if qty, ok := item["quantity"].(map[string]any); ok {
					if q, ok := parseNumeric(qty["value"]); ok {
						qtyVal = q
					}
				}
				if up, ok := parseMinorAmount(unitPrice["value"]); ok {
					computedTotal += int64(float64(up) * qtyVal)
				}
			}
		}

		// Reconcile line item total with claim total (allow within 100 paise / INR 1 rounding).
		diff := computedTotal - totalAmount
		if diff < 0 {
			diff = -diff
		}
		if diff > 100 {
			result.Errors = append(result.Errors, fmt.Sprintf("reconciliation mismatch: line items total %d paise differs from claim total %d paise", computedTotal, totalAmount))
		}
	}
}

func getEntries(bundle map[string]any) []map[string]any {
	es, ok := bundle["entry"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(es))
	for _, e := range es {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func parseMinorAmount(v any) (int64, bool) {
	if v == nil {
		return 0, false
	}
	var s string
	switch val := v.(type) {
	case json.Number:
		s = val.String()
	case float64:
		s = big.NewFloat(val).Text('f', -1)
	case int:
		s = fmt.Sprintf("%d", val)
	case int64:
		s = fmt.Sprintf("%d", val)
	default:
		return 0, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return 0, false
	}
	// Multiply by 100 for paise.
	r.Mul(r, big.NewRat(100, 1))
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}

func parseSequence(v any) (int, bool) {
	switch val := v.(type) {
	case json.Number:
		i, err := val.Int64()
		return int(i), err == nil
	case float64:
		return int(val), true
	case int:
		return val, true
	case int64:
		return int(val), true
	}
	return 0, false
}

func parseNumeric(v any) (float64, bool) {
	switch val := v.(type) {
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	}
	return 0.0, false
}

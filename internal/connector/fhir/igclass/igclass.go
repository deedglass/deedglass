// Package igclass reads a FHIR resource's meta.profile and, for a Bundle, what the bundle holds,
// and names it in the terms of India's national FHIR profiles (HC-P15, ADR-119):
//
//   - the ABDM health records of the NRCeS FHIR Implementation Guide for ABDM (OPConsultRecord,
//     DischargeSummaryRecord, PrescriptionRecord, DiagnosticReportRecord, ImmunizationRecord,
//     HealthDocumentRecord, WellnessRecord, InvoiceRecord);
//   - the NHCX bundles the same guide carries (ClaimBundle, ClaimResponseBundle,
//     CoverageEligibilityRequestBundle, CoverageEligibilityResponseBundle, TaskBundle,
//     InsurancePlanBundle), and the Claim's use (claim, preauthorization, predetermination) and
//     total.
//
// The profile URLs are data, in the tables below, pinned to one guide version (IGVersion). A new
// guide version is a table and fixture update; nothing here judges clinical content. What a bundle
// holds counts for more than what it says it is: a bundle labelled one NHCX kind that holds another
// is reported as the kind it holds, with ProfileMismatch set.
package igclass

import (
	"encoding/json"
	"math/big"
	"slices"
	"strings"
)

// The guide the tables follow: FHIR Implementation Guide for ABDM 6.5.0 (package ndhm.in#6.5.0,
// FHIR 4.0.1, published by NRCeS, C-DAC Pune, 2025-05-08, licence CC0-1.0). It holds the NHCX
// profiles too (https://nrces.in/ndhm/fhir/r4/hcx-profile.html).
const (
	IGPackage   = "ndhm.in"
	IGVersion   = "6.5.0"
	IGCanonBase = "https://nrces.in/ndhm/fhir/r4/StructureDefinition/"
)

// Records are the ABDM health record kinds, each a Composition profile of the same name.
var Records = []string{"DiagnosticReportRecord", "DischargeSummaryRecord", "HealthDocumentRecord", "ImmunizationRecord",
	"InvoiceRecord", "OPConsultRecord", "PrescriptionRecord", "WellnessRecord"}

// recordCodes name a record by its Composition.type SNOMED CT code when the profile is missing,
// as the guide's own examples code them.
var recordCodes = map[string]string{
	"371530004":   "OPConsultRecord",        // Clinical consultation report
	"373942005":   "DischargeSummaryRecord", // Discharge summary
	"440545006":   "PrescriptionRecord",     // Prescription record
	"721981007":   "DiagnosticReportRecord", // Diagnostic studies report
	"41000179103": "ImmunizationRecord",     // Immunization record
	"419891008":   "HealthDocumentRecord",   // Record artifact
}

// NHCXBundles maps each NHCX bundle profile to the resource type its first entry holds.
var NHCXBundles = map[string]string{
	"ClaimBundle":                       "Claim",
	"ClaimResponseBundle":               "ClaimResponse",
	"CoverageEligibilityRequestBundle":  "CoverageEligibilityRequest",
	"CoverageEligibilityResponseBundle": "CoverageEligibilityResponse",
	"InsurancePlanBundle":               "InsurancePlan",
	"TaskBundle":                        "Task",
}

// Claim uses as FHIR R4 codes them (http://hl7.org/fhir/claim-use); anything else is "other".
var claimUses = []string{"claim", "preauthorization", "predetermination"}

// currencyExp is the minor unit of the currencies a claim is likely to carry. A currency outside it
// leaves the amount unknown (-1), which rules treat as large.
var currencyExp = map[string]int{"inr": 2, "usd": 2, "eur": 2, "gbp": 2, "aed": 2, "sgd": 2, "lkr": 2, "npr": 2, "bdt": 2, "jpy": 0}

// Limits on what one request is walked for.
const (
	maxDepth      = 4    // nested Bundles
	maxResources  = 2000 // resources visited
	maxEntryTypes = 64
)

// Facts are what the classifier read. The zero value means "not a bundle, no ABDM record, no
// claim".
type Facts struct {
	BundleType      string   // Bundle.type: document, collection, transaction, …; "" when not a Bundle
	EntryTypes      []string // the resource types the bundle holds (nested bundles included), sorted
	Record          string   // the ABDM record kind, or ""
	RecordStatus    string   // that record's Composition.status
	NHCX            string   // the NHCX bundle kind, or ""
	ProfileMismatch bool     // the bundle's NHCX profile names a different kind than it holds
	Claims          int64    // Claim resources in the request
	ClaimUse        string   // the focal Claim's use: claim, preauthorization, predetermination or other
	ClaimUses       []string // every Claim's use, sorted, distinct
	ClaimAmount     int64    // the largest Claim.total in minor units of ClaimCurrency; -1 when unknown
	ClaimCurrency   string   // lowercase ISO 4217; "" when the claims don't share one
}

// Kind strips the canonical base and any |version from a profile URL of this guide: "ClaimBundle"
// for "https://nrces.in/ndhm/fhir/r4/StructureDefinition/ClaimBundle|6.5.0". Other URLs give "".
func Kind(profile string) string {
	p, _, _ := strings.Cut(profile, "|")
	k, ok := strings.CutPrefix(p, IGCanonBase)
	if !ok || k == "" || strings.ContainsAny(k, "/?#") {
		return ""
	}
	return k
}

func kinds(res map[string]any) []string {
	meta, _ := res["meta"].(map[string]any)
	ps, _ := meta["profile"].([]any)
	var out []string
	for _, p := range ps {
		if s, ok := p.(string); ok {
			if k := Kind(s); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

func typeOf(res map[string]any) string {
	s, _ := res["resourceType"].(string)
	return s
}

func entries(b map[string]any) []map[string]any {
	es, _ := b["entry"].([]any)
	out := make([]map[string]any, 0, len(es))
	for _, e := range es {
		em, _ := e.(map[string]any)
		if r, ok := em["resource"].(map[string]any); ok {
			out = append(out, r)
		}
	}
	return out
}

// Classify reads one resource as an agent sends it (decoded JSON; numbers may be json.Number or
// float64).
func Classify(res map[string]any) Facts {
	var f Facts
	if res == nil {
		return f
	}
	root := typeOf(res)
	var all []map[string]any // every resource reached, the root first
	seenTypes := map[string]bool{}
	var walk func(r map[string]any, depth int)
	walk = func(r map[string]any, depth int) {
		if len(all) >= maxResources || depth > maxDepth {
			return
		}
		all = append(all, r)
		if len(all) > 1 {
			if t := typeOf(r); t != "" {
				seenTypes[t] = true
			}
		}
		cs, _ := r["contained"].([]any)
		for _, c := range cs {
			if cm, ok := c.(map[string]any); ok {
				walk(cm, depth+1)
			}
		}
		if typeOf(r) == "Bundle" {
			for _, e := range entries(r) {
				walk(e, depth+1)
			}
		}
	}
	walk(res, 0)
	if root == "Bundle" {
		f.BundleType, _ = res["type"].(string)
		f.EntryTypes = []string{}
		for t := range seenTypes {
			f.EntryTypes = append(f.EntryTypes, t)
		}
		slices.Sort(f.EntryTypes)
		if len(f.EntryTypes) > maxEntryTypes {
			f.EntryTypes = f.EntryTypes[:maxEntryTypes]
		}
	}

	// The ABDM record: the Composition itself, or a document bundle's Composition.
	var comp map[string]any
	if root == "Composition" {
		comp = res
	} else if root == "Bundle" {
		for _, e := range entries(res) {
			if typeOf(e) == "Composition" {
				comp = e
				break
			}
		}
	}
	if comp != nil {
		f.Record = recordOf(comp)
		if f.Record != "" {
			f.RecordStatus, _ = comp["status"].(string)
		}
	}

	// The NHCX kind: what the bundle holds first, checked against what it says it is.
	if root == "Bundle" {
		var declared string
		for _, k := range kinds(res) {
			if _, ok := NHCXBundles[k]; ok {
				declared = k
				break
			}
		}
		var held string
		if es := entries(res); len(es) > 0 && f.BundleType != "document" {
			first := typeOf(es[0])
			for k, t := range NHCXBundles {
				if t == first {
					held = k
				}
			}
		}
		switch {
		case declared != "" && held != "" && declared != held:
			f.NHCX, f.ProfileMismatch = held, true
		case declared != "" && held == "":
			f.NHCX, f.ProfileMismatch = declared, true
		case held != "":
			f.NHCX = held
		}
	}

	// Claims anywhere in the request.
	var claims []map[string]any
	for _, r := range all {
		if typeOf(r) == "Claim" {
			claims = append(claims, r)
		}
	}
	f.Claims = int64(len(claims))
	if len(claims) > 0 {
		f.ClaimUse = useOf(claims[0])
		uses := map[string]bool{}
		f.ClaimAmount, f.ClaimCurrency = 0, ""
		known := true
		for i, c := range claims {
			uses[useOf(c)] = true
			amt, cur, ok := totalOf(c)
			if !ok || (i > 0 && cur != f.ClaimCurrency) {
				known = false
				continue
			}
			f.ClaimCurrency = cur
			if amt > f.ClaimAmount {
				f.ClaimAmount = amt
			}
		}
		for u := range uses {
			f.ClaimUses = append(f.ClaimUses, u)
		}
		slices.Sort(f.ClaimUses)
		if !known {
			f.ClaimAmount, f.ClaimCurrency = -1, ""
		}
	}
	return f
}

func recordOf(comp map[string]any) string {
	for _, k := range kinds(comp) {
		if slices.Contains(Records, k) {
			return k
		}
	}
	t, _ := comp["type"].(map[string]any)
	cs, _ := t["coding"].([]any)
	for _, c := range cs {
		m, _ := c.(map[string]any)
		if sys, _ := m["system"].(string); sys != "http://snomed.info/sct" {
			continue
		}
		if code, _ := m["code"].(string); recordCodes[code] != "" {
			return recordCodes[code]
		}
	}
	return ""
}

func useOf(c map[string]any) string {
	u, _ := c["use"].(string)
	if slices.Contains(claimUses, u) {
		return u
	}
	return "other"
}

// totalOf is Claim.total in minor units: exact for the currencies in currencyExp, rounded up when
// it carries more decimals than the currency has. ok is false when there is no usable total.
func totalOf(c map[string]any) (amount int64, currency string, ok bool) {
	t, _ := c["total"].(map[string]any)
	cur, _ := t["currency"].(string)
	cur = strings.ToLower(cur)
	exp, known := currencyExp[cur]
	if !known {
		return 0, "", false
	}
	var s string
	switch v := t["value"].(type) {
	case json.Number:
		s = v.String()
	case float64:
		s = big.NewFloat(v).Text('f', -1)
	case string:
		return 0, "", false // FHIR decimals are JSON numbers
	default:
		return 0, "", false
	}
	r, okR := new(big.Rat).SetString(s)
	if !okR || r.Sign() < 0 {
		return 0, "", false
	}
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil))
	r.Mul(r, scale)
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Int64() > 1<<53-1 {
		return 0, "", false
	}
	return q.Int64(), cur, true
}

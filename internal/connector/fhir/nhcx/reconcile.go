package nhcx

import (
	"fmt"
	"strings"
)

// Reconciliation status constants.
const (
	StatusReconciled      = "RECONCILED"
	StatusUnbilledLeakage = "UNBILLED_CONSUMABLE_LEAKAGE"
	StatusInvalidClaim    = "INVALID_CLAIM_STRUCTURE"
)

// Standard procedural consumable codes (SNOMED CT / Indian National Health Cost codes)
// and conservative reference tariff benchmarks (in minor units: paise) for emergency/OT reconciliation.
var DefaultConsumableTariffs = map[string]int64{
	// Surgical Mesh / Hernia Repair
	"428983002": 1500000, // Surgical mesh: INR 15,000
	// Coronary / Vascular Stent
	"414002008": 3000000, // Drug-eluting stent: INR 30,000
	// Surgical Suture Pack / Stapler Cartridge
	"449171004": 450000, // Endoscopic linear stapler / suture kit: INR 4,500
	// Intravenous Infusion / Blood Administration Set
	"261271009": 35000, // IV Cannula & infusion kit: INR 350
	// Central Venous Catheter Kit
	"52124006": 250000, // Central venous line: INR 2,500
	// Endotracheal Tube & Circuit
	"26412008": 120000, // Ventilator circuit & tube: INR 1,200
	// Orthopedic Bone Fixation Plate / Screw
	"30361008": 1800000, // Bone plate & screw set: INR 18,000
	// Dental Titanium Implant
	"22813002": 2200000, // Dental endosseous implant: INR 22,000
}

// ConsumableItem represents a billable consumable or procedural order item.
type ConsumableItem struct {
	Code              string `json:"code"`
	System            string `json:"system"`
	Display           string `json:"display"`
	SourceResource    string `json:"source_resource"`
	SourceID          string `json:"source_id"`
	EstimatedValueINR int64  `json:"estimated_value_inr"`
}

// ReconciliationReport holds the output of the order-to-billing reconciliation witness.
type ReconciliationReport struct {
	Status                    string           `json:"status"`
	Reconciled                bool             `json:"reconciled"`
	ClaimedItemCount          int              `json:"claimed_item_count"`
	DocumentedConsumableCount int              `json:"documented_consumable_count"`
	MatchedItems              []ConsumableItem `json:"matched_items"`
	Omissions                 []ConsumableItem `json:"omissions"`
	TotalPotentialLeakageINR  int64            `json:"total_potential_leakage_inr"`
	FailClosedGating          bool             `json:"fail_closed_gating"`
	Summary                   string           `json:"summary"`
}

// ReconcileConsumables compares clinical documentation (Procedures, DeviceRequests, SupplyDeliveries)
// against the Claim.item line entries to catch omitted emergency/OT consumables before NHCX submission.
//
// In strict adherence to CLINICAL_SAFETY_BOUNDARY.md:
// 1. This check is purely administrative and billing-oriented.
// 2. It never alters clinical notes or diagnoses.
// 3. Any omission triggers fail-closed administrative gating rather than autonomous billing mutations.
func ReconcileConsumables(bundle map[string]any, clinicalRecords []map[string]any, tariffOverrides map[string]int64) (*ReconciliationReport, error) {
	vr, err := VerifyClaimBundle(bundle)
	if err != nil {
		return nil, err
	}
	if !vr.Valid && vr.ClaimUse == "claim" && len(vr.Errors) > 0 {
		return &ReconciliationReport{
			Status:           StatusInvalidClaim,
			Reconciled:       false,
			FailClosedGating: true,
			Summary:          fmt.Sprintf("Claim bundle has structural errors: %s", strings.Join(vr.Errors, "; ")),
		}, nil
	}

	// Extract claimed codes from Claim.item entries.
	claimedCodes := extractClaimedCodes(bundle)

	// Merge tariffs.
	tariffs := make(map[string]int64)
	for k, v := range DefaultConsumableTariffs {
		tariffs[k] = v
	}
	for k, v := range tariffOverrides {
		tariffs[k] = v
	}

	// Extract documented consumables from clinical records and bundle entries.
	allRecords := make([]map[string]any, 0, len(clinicalRecords)+vr.EntryCount)
	allRecords = append(allRecords, clinicalRecords...)
	for _, entry := range getEntries(bundle) {
		if r, ok := entry["resource"].(map[string]any); ok {
			allRecords = append(allRecords, r)
		}
	}

	documented := extractDocumentedConsumables(allRecords, tariffs)

	report := &ReconciliationReport{
		Status:                    StatusReconciled,
		Reconciled:                true,
		ClaimedItemCount:          len(claimedCodes),
		DocumentedConsumableCount: len(documented),
		MatchedItems:              make([]ConsumableItem, 0),
		Omissions:                 make([]ConsumableItem, 0),
		FailClosedGating:          false,
	}

	var leakagePaise int64
	for _, item := range documented {
		if claimedCodes[item.Code] {
			report.MatchedItems = append(report.MatchedItems, item)
		} else {
			report.Omissions = append(report.Omissions, item)
			leakagePaise += item.EstimatedValueINR
		}
	}

	report.TotalPotentialLeakageINR = leakagePaise
	if len(report.Omissions) > 0 {
		report.Status = StatusUnbilledLeakage
		report.Reconciled = false
		report.FailClosedGating = true
		report.Summary = fmt.Sprintf("Unbilled consumable leakage detected: %d documented item(s) absent from claim (estimated impact ₹%.2f)",
			len(report.Omissions), float64(leakagePaise)/100.0)
	} else {
		report.Summary = fmt.Sprintf("All %d documented procedural consumable(s) reconciled with claim line items", len(documented))
	}

	return report, nil
}

func extractClaimedCodes(bundle map[string]any) map[string]bool {
	codes := make(map[string]bool)
	entries := getEntries(bundle)
	for _, entry := range entries {
		res, ok := entry["resource"].(map[string]any)
		if !ok || res["resourceType"] != "Claim" {
			continue
		}
		items, _ := res["item"].([]any)
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			svc, _ := item["productOrService"].(map[string]any)
			if svc != nil {
				codings, _ := svc["coding"].([]any)
				for _, c := range codings {
					if cm, ok := c.(map[string]any); ok {
						if code, ok := cm["code"].(string); ok && code != "" {
							codes[code] = true
						}
					}
				}
			}
		}
	}
	return codes
}

func extractDocumentedConsumables(records []map[string]any, tariffs map[string]int64) []ConsumableItem {
	var items []ConsumableItem
	seen := make(map[string]bool)

	for _, r := range records {
		if r == nil {
			continue
		}
		typ, _ := r["resourceType"].(string)
		id, _ := r["id"].(string)

		switch typ {
		case "Procedure":
			// Procedure.usedCode or Procedure.focalDevice
			if usedCodes, ok := r["usedCode"].([]any); ok {
				for _, uc := range usedCodes {
					if ucm, ok := uc.(map[string]any); ok {
						for _, ci := range parseCodings(ucm, typ, id, tariffs) {
							key := ci.Code + "@" + id
							if !seen[key] {
								seen[key] = true
								items = append(items, ci)
							}
						}
					}
				}
			}
		case "DeviceRequest":
			if codeCC, ok := r["codeCodeableConcept"].(map[string]any); ok {
				for _, ci := range parseCodings(codeCC, typ, id, tariffs) {
					key := ci.Code + "@" + id
					if !seen[key] {
						seen[key] = true
						items = append(items, ci)
					}
				}
			}
		case "SupplyDelivery":
			if itemCC, ok := r["itemCodeableConcept"].(map[string]any); ok {
				for _, ci := range parseCodings(itemCC, typ, id, tariffs) {
					key := ci.Code + "@" + id
					if !seen[key] {
						seen[key] = true
						items = append(items, ci)
					}
				}
			}
		}
	}
	return items
}

func parseCodings(cc map[string]any, resType, resID string, tariffs map[string]int64) []ConsumableItem {
	var out []ConsumableItem
	codings, _ := cc["coding"].([]any)
	for _, c := range codings {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		code, _ := cm["code"].(string)
		sys, _ := cm["system"].(string)
		disp, _ := cm["display"].(string)
		if code == "" {
			continue
		}
		val, exists := tariffs[code]
		if !exists {
			val = 0
		}
		out = append(out, ConsumableItem{
			Code:              code,
			System:            sys,
			Display:           disp,
			SourceResource:    resType,
			SourceID:          resID,
			EstimatedValueINR: val,
		})
	}
	return out
}

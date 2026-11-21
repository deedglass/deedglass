package pas

import (
	"errors"
	"fmt"
	"strings"
)

// PAReconciliationReport holds the reconciliation verdict between requested prior auth items
// and documented active EHR clinical orders (Hazard CH-34).
type PAReconciliationReport struct {
	ClaimID         string   `json:"claim_id"`
	ClaimAmountUSD  float64  `json:"claim_amount_usd"`
	TotalClaimItems int      `json:"total_claim_items"`
	MatchedOrders   int      `json:"matched_orders"`
	UngroundedItems []string `json:"ungrounded_items,omitempty"`
	Reconciled      bool     `json:"reconciled"`
}

// ReconcilePriorAuth matches items requested in a Da Vinci PAS prior authorization bundle
// against documented active EHR orders (ServiceRequest, MedicationRequest, Procedure).
// Prevents ambient and agentic hallucination of unauthorized prior-authorization items (Hazard CH-34).
func ReconcilePriorAuth(bundle map[string]any, ehrOrders []map[string]any) (*PAReconciliationReport, error) {
	if bundle == nil {
		return nil, errors.New("pas: nil bundle")
	}

	entries := getEntries(bundle)
	if len(entries) == 0 {
		return nil, errors.New("pas: empty bundle entries")
	}

	firstRes, _ := entries[0]["resource"].(map[string]any)
	claimID, _ := firstRes["id"].(string)

	total, _ := firstRes["total"].(map[string]any)
	amount, _ := parseAmountFloat(total["value"])

	items, _ := firstRes["item"].([]any)

	report := &PAReconciliationReport{
		ClaimID:         claimID,
		ClaimAmountUSD:  amount,
		TotalClaimItems: len(items),
		UngroundedItems: make([]string, 0),
		Reconciled:      true,
	}

	// Index EHR order codes
	ehrCodes := make(map[string]bool)
	for _, order := range ehrOrders {
		for _, c := range extractOrderCodes(order) {
			ehrCodes[strings.ToLower(c)] = true
		}
	}

	for i, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		seq, _ := parseSequence(item["sequence"])
		if seq <= 0 {
			seq = i + 1
		}

		itemCodes := extractItemCodes(item)
		var matched bool
		for _, c := range itemCodes {
			if ehrCodes[strings.ToLower(c)] {
				matched = true
				break
			}
		}

		if matched {
			report.MatchedOrders++
		} else {
			codeSummary := strings.Join(itemCodes, ",")
			if codeSummary == "" {
				codeSummary = "unknown-service"
			}
			report.UngroundedItems = append(report.UngroundedItems,
				fmt.Sprintf("item %d (%s): no matching EHR order found (CH-34 phantom order)", seq, codeSummary))
			report.Reconciled = false
		}
	}

	return report, nil
}

func extractItemCodes(item map[string]any) []string {
	var codes []string
	svc, ok := item["productOrService"].(map[string]any)
	if !ok {
		return codes
	}
	codings, ok := svc["coding"].([]any)
	if !ok {
		return codes
	}
	for _, raw := range codings {
		coding, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		c, _ := coding["code"].(string)
		if c != "" {
			codes = append(codes, c)
		}
	}
	return codes
}

func extractOrderCodes(res map[string]any) []string {
	var codes []string
	// Codeable concept can be code (ServiceRequest, Procedure) or medicationCodeableConcept (MedicationRequest)
	for _, key := range []string{"code", "medicationCodeableConcept"} {
		if cc, ok := res[key].(map[string]any); ok {
			if codings, ok := cc["coding"].([]any); ok {
				for _, raw := range codings {
					if coding, ok := raw.(map[string]any); ok {
						if c, _ := coding["code"].(string); c != "" {
							codes = append(codes, c)
						}
					}
				}
			}
		}
	}
	return codes
}

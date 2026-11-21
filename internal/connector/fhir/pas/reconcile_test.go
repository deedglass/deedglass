package pas

import (
	"testing"
)

func TestReconcilePriorAuth_Matched(t *testing.T) {
	bundle := validTestPASBundle(1200.0)
	ehrOrders := []map[string]any{
		{
			"resourceType": "ServiceRequest",
			"id":           "sr-101",
			"code": map[string]any{
				"coding": []any{
					map[string]any{
						"system": "http://www.ama-assn.org/go/cpt",
						"code":   "72148",
					},
				},
			},
		},
	}

	report, err := ReconcilePriorAuth(bundle, ehrOrders)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !report.Reconciled {
		t.Errorf("expected reconciled = true")
	}
	if report.MatchedOrders != 1 {
		t.Errorf("expected 1 matched order, got %d", report.MatchedOrders)
	}
	if len(report.UngroundedItems) != 0 {
		t.Errorf("expected 0 ungrounded items, got %v", report.UngroundedItems)
	}
}

func TestReconcilePriorAuth_UngroundedPhantom(t *testing.T) {
	bundle := validTestPASBundle(1200.0)
	// EHR orders contain unrelated lab order, not the MRI CPT 72148
	ehrOrders := []map[string]any{
		{
			"resourceType": "ServiceRequest",
			"id":           "sr-999",
			"code": map[string]any{
				"coding": []any{
					map[string]any{
						"system": "http://loinc.org",
						"code":   "2345-7", // Glucose
					},
				},
			},
		},
	}

	report, err := ReconcilePriorAuth(bundle, ehrOrders)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Reconciled {
		t.Errorf("expected reconciled = false for ungrounded prior auth draft (CH-34)")
	}
	if len(report.UngroundedItems) != 1 {
		t.Errorf("expected 1 ungrounded item, got %d", len(report.UngroundedItems))
	}
}

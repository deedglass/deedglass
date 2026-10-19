package phimode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/store"
)

func TestProject(t *testing.T) {
	msg := `The service refused the request (HTTP 400): Patient "Jane Q Synthetic" not found.`
	p := store.PHIPending{ID: "act_x", Tool: "fhir.update_observation", Mode: "gateway",
		Request: json.RawMessage(`{"id":"1001","resource_id":"1001","status":"final","subject":{"reference":"Patient/MRN-0042-SYN"}}`),
		Result: json.RawMessage(`{"status":200,"headers":{"etag":"W/\"2\"","content-type":"application/fhir+json"},
		  "body":{"resourceType":"Observation","id":"1001","subject":{"reference":"Patient/MRN-0042-SYN"}}}`),
		ErrorMessage:  &msg,
		WitnessDetail: json.RawMessage(`{"object":"witness_detail","summary":"Read back: subject.reference is Patient/MRN-0042-SYN.","claim":{"verdict":"VERIFIED"}}`),
	}
	keep := connector.PHIKeep{Args: []string{"id"}, Result: []string{"status", "headers.etag"}}
	m := Project(p, keep)
	all := string(m.Request) + string(m.Result) + string(m.WitnessDetail) + *m.ErrorMessage
	for _, bad := range []string{"MRN-0042-SYN", "Jane Q Synthetic", "final", "application/fhir+json"} {
		if strings.Contains(all, bad) {
			t.Errorf("kept %q: %s", bad, all)
		}
	}
	for _, good := range []string{`"id":"1001"`, `"status":200`, `"etag":"W/\"2\""`, `"verdict":"VERIFIED"`} {
		if !strings.Contains(all, good) {
			t.Errorf("lost %s: %s", good, all)
		}
	}
	if !strings.HasPrefix(*m.ErrorMessage, "sha256:") || m.ID != "act_x" {
		t.Fatalf("%+v", m)
	}
	// Empty payloads stay empty (SQL NULL), and a proxy-mode action keeps nothing.
	m = Project(store.PHIPending{ID: "act_y"}, connector.PHIKeep{})
	if m.Request != nil || m.Result != nil || m.WitnessDetail != nil || m.ErrorMessage != nil {
		t.Fatalf("empty: %+v", m)
	}
}

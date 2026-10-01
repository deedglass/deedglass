package policy

import (
	"context"
	"testing"
	"time"
)

// TestS16Facts: tool.type, tool.op and request.http are visible to rules (S16 §3.6).
func TestS16Facts(t *testing.T) {
	src := []byte(`deedglass: policy/v1
name: s16-facts
defaults:
  decision: deny
  reason: Only order reads are allowed here.
rules:
  - id: rest-get-orders
    match: { tool: "orders.*" }
    when: 'tool.type == "rest" && tool.op == "get" && request.http.method == "GET" && request.http.path.startsWith("/v1/orders/")'
    decision: allow
`)
	p, diags := Compile(src, Options{File: "s16.yaml"})
	if p == nil {
		t.Fatalf("compile: %v", diags)
	}
	in := Input{V: InputVersion, Tool: ToolInput{ToolFacts: ToolFacts{Name: "orders.get", Connector: "orders", ConnectorType: "rest", SideEffect: "read", Type: "rest", Op: "get"}},
		Args: []byte(`{"id":"42"}`), Request: RequestFacts{Mode: "gateway", HTTP: HTTPFacts{Method: "GET", Path: "/v1/orders/42", Host: "orders.example.com"}},
		Agent: AgentInput{AgentFacts: AgentFacts{ID: LocalAgentID, Name: "a", Status: StatusActive, Labels: map[string]string{}}},
		Org:   OrgInput{OrgFacts: OrgFacts{ID: "org_01k6c9kb2zcjsyn7b8g2k1vqvp", Timezone: "UTC"}}, Time: time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)}
	d := Evaluate(context.Background(), p, in, EvalOptions{})
	if d.Outcome != Allow || d.RuleID != "rest-get-orders" {
		t.Fatalf("decision %+v", d)
	}
	in.Request.HTTP.Method = "PATCH"
	if d := Evaluate(context.Background(), p, in, EvalOptions{}); d.Outcome != Deny {
		t.Fatalf("PATCH: %+v", d)
	}
}

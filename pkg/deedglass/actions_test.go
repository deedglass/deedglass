package deedglass

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestActParity(t *testing.T) {
	var got []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/actions" && m["tool"] == "billing.delete_customer":
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"type":"policy_error","code":"policy_denied","message":"Blocked by policy \"no-deletes\": Agents never delete customers.","action":{"object":"action","id":"act_1","status":"denied"}}}`)
		case r.URL.Path == "/v1/actions":
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("no Idempotency-Key")
			}
			io.WriteString(w, `{"object":"action","id":"act_1","status":"authorized","mode":"proxy","next_action":{"type":"execute_at_edge","deadline":"2026-10-10T10:00:30.000Z"}}`)
		case strings.HasSuffix(r.URL.Path, "/result"):
			io.WriteString(w, `{"object":"action","id":"act_1","status":"executed","deed":"deed_1","execution":{"status":"succeeded","result":{"ok":true}}}`)
		case r.URL.Path == "/v1/tools" && r.URL.Query().Get("cursor") == "":
			io.WriteString(w, `{"object":"list","data":[{"object":"tool","name":"fake.echo","levels":["L0"]}],"has_more":true,"next_cursor":"c2"}`)
		case r.URL.Path == "/v1/tools":
			io.WriteString(w, `{"object":"list","data":[{"object":"tool","name":"stripe.create_refund","levels":["L0","L1"],"read_back":"Stripe shows the refund."}],"has_more":false,"next_cursor":null}`)
		default:
			io.WriteString(w, `{"object":"action","id":"act_1","status":"pending_approval","approval":{"id":"apr_1","status":"pending","expires_at":"2026-10-10T10:42:00.000Z"}}`)
		}
	}))
	defer srv.Close()
	c := NewClient("dg_test_Ab12Cd34_"+strings.Repeat("a", 32), srv.URL)
	c.HTTP = srv.Client()
	ctx := context.Background()
	w := 30 * time.Second
	a, _, err := c.Act(ctx, ActRequest{Tool: "billing.refund_customer", Args: json.RawMessage(`{"amount":2000}`), Mode: "proxy", ConnectorType: "sdk", Wait: &w})
	if err != nil || a.Status != "authorized" || a.NextAction.Type != "execute_at_edge" || len(a.Raw) == 0 {
		t.Fatalf("%+v %v", a, err)
	}
	if bodies[0]["connector_type"] != "sdk" || bodies[0]["wait"] != float64(25) || bodies[0]["mode"] != "proxy" {
		t.Fatalf("body %v", bodies[0])
	}
	r, err := c.ReportResult(ctx, a.ID, EdgeResult{Status: "succeeded", StartedAt: time.Now(), FinishedAt: time.Now(), Result: json.RawMessage(`{"ok":true}`)}, WithIdempotencyKey("rep_act_1"))
	if err != nil || r.Status != "executed" || *r.Deed != "deed_1" || string(r.Execution.Result) != `{"ok":true}` {
		t.Fatalf("%+v %v", r, err)
	}
	p, err := c.GetAction(ctx, "act_1", 40*time.Second)
	if err != nil || p.Approval.ID != "apr_1" {
		t.Fatalf("%+v %v", p, err)
	}
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 2 || tools[1].ReadBack == "" {
		t.Fatalf("%+v %v", tools, err)
	}
	_, _, err = c.Act(ctx, ActRequest{Tool: "billing.delete_customer"})
	var de *Error
	if !errors.As(err, &de) || de.Code != "policy_denied" || len(de.Action) == 0 {
		t.Fatalf("%v", err)
	}
	if _, _, err := c.Act(ctx, ActRequest{}); err == nil {
		t.Fatal("an empty tool was sent")
	}
	want := []string{"POST /v1/actions", "POST /v1/actions/act_1/result", "GET /v1/actions/act_1?wait=25", "GET /v1/tools", "GET /v1/tools?cursor=c2", "POST /v1/actions"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requests %v", got)
	}
}

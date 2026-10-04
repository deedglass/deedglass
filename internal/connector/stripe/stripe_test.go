package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
)

func TestLiveKeyRefusals(t *testing.T) {
	cases := []struct {
		key, code, msg string
	}{
		{"sk_live_51abcdefghijklmnop", connector.CodeStripeLiveKeyRefused, MsgLiveKey},
		{"rk_live_51abcdefghijklmnop", connector.CodeStripeLiveKeyRefused, MsgLiveKey},
		{"sk_org_51abcdefghijklmnop", connector.CodeValidationFailed, MsgOrgKey},
		{"pk_test_51abcdefghijklmnop", connector.CodeValidationFailed, MsgPublishable},
		{"pk_live_51abcdefghijklmnop", connector.CodeValidationFailed, MsgPublishable},
		{"whsec_abcdefghijklmnop", connector.CodeValidationFailed, MsgWebhookSecret},
		{"rk_test_short", connector.CodeValidationFailed, MsgNotSandbox},
		{"", connector.CodeValidationFailed, MsgNotSandbox},
		{"rk_test_51abc def", connector.CodeValidationFailed, MsgNotSandbox},
		{testKey, "", ""},
		{secretSK, "", ""},
	}
	c := New()
	for _, tc := range cases {
		code, msg := CheckKey(tc.key)
		if code != tc.code || msg != tc.msg {
			t.Errorf("CheckKey(%s…) = %q, %q; want %q, %q", Prefix(tc.key), code, msg, tc.code, tc.msg)
		}
		ref := c.CheckCredential("bearer", map[string]string{FieldToken: tc.key})
		if (ref == nil) != (tc.code == "") {
			t.Errorf("CheckCredential(%s…) = %v", Prefix(tc.key), ref)
		}
		if ref != nil {
			if strings.Contains(ref.Message, tc.key) && tc.key != "" {
				t.Errorf("the refusal echoes the key")
			}
			if tc.code == connector.CodeStripeLiveKeyRefused && ref.Code != tc.code {
				t.Errorf("live key code %q", ref.Code)
			}
			if ref.Param != "secret.token" {
				t.Errorf("param %q", ref.Param)
			}
		}
	}
	if Prefix(liveKey) != "sk_live_…" || Hint(testKey) != "rk_test_…9f2c" {
		t.Errorf("prefix %q hint %q", Prefix(liveKey), Hint(testKey))
	}
}

func TestLiveKeyStoredRow(t *testing.T) {
	r := newRig(t, nil)
	r.op.key = liveKey // the row was edited in the database
	_, err, _ := r.call(ToolGetCharge, `{"charge":"`+chargeID+`"}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Code != connector.CodeStripeLiveKeyRefused || ce.Kind != connector.KindConfig {
		t.Fatalf("err = %v", err)
	}
	if ce.AgentMessage != MsgDisabledAgent || !strings.Contains(ce.AdminDetail, "sk_live_…") {
		t.Errorf("messages %q / %q", ce.AgentMessage, ce.AdminDetail)
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Errorf("%d requests left; want 0", n)
	}
	if r.br.trips.Load() != 1 || r.br.reasons[0] != ReasonLiveKey {
		t.Errorf("breaker %d %v", r.br.trips.Load(), r.br.reasons)
	}
	// The instance stays disabled even with a good key back in the row.
	r.op.key = testKey
	_, err, _ = r.call(ToolGetCharge, `{"charge":"`+chargeID+`"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != connector.CodeConnectorUnavailable {
		t.Fatalf("after the trip: %v", err)
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Errorf("%d requests after the trip", n)
	}
	if !strings.Contains(r.logs.String(), "alert=stripe_live_key_stored") {
		t.Errorf("no alert line: %s", r.logs.String())
	}
	r.sweep(ce.AdminDetail, ce.AgentMessage)
}

func TestLivemodeBreaker(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"top", `{"id":"` + chargeID + `","object":"charge","amount":100,"currency":"usd","livemode":true}`},
		{"data", `{"object":"list","data":[{"id":"ch_1","livemode":false},{"id":"ch_2","livemode":true}],"has_more":false,"livemode":false}`},
		{"nested", `{"id":"` + chargeID + `","refunds":{"data":[{"livemode":true}]},"livemode":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/v1/charges/" + chargeID
			tool, args := ToolGetCharge, `{"charge":"`+chargeID+`"}`
			if tc.name == "data" {
				path, tool, args = "/v1/charges", ToolListCharges, `{}`
			}
			r := newRig(t, nil, ex("live", "GET", path, 200, tc.body))
			_, err, _ := r.call(tool, args)
			ce := connector.AsError(err)
			if ce == nil || ce.Code != connector.CodeStripeLivemodeDetected || ce.Kind != connector.KindUpstream {
				t.Fatalf("err = %v", err)
			}
			if ce.AgentMessage != MsgDisabledAgent || !strings.Contains(ce.AdminDetail, "req_demolive") {
				t.Errorf("messages %q / %q", ce.AgentMessage, ce.AdminDetail)
			}
			if r.br.trips.Load() != 1 || r.br.reasons[0] != ReasonLivemode {
				t.Errorf("breaker %v", r.br.reasons)
			}
			_, err, _ = r.call(ToolGetCharge, `{"charge":"`+chargeID+`"}`)
			if ce := connector.AsError(err); ce == nil || ce.Code != connector.CodeConnectorUnavailable {
				t.Fatalf("second call: %v", err)
			}
			if n := len(r.srv.Requests()); n != 1 {
				t.Errorf("%d requests; want 1 (no request after the trip)", n)
			}
			t.Logf("breaker transcript: requests=%d trips=%d reason=%s", len(r.srv.Requests()), r.br.trips.Load(), r.br.reasons[0])
		})
	}
}

func refundExchanges(amount int64, cur string) []saastest.Exchange {
	post := ex("refund", "POST", "/v1/refunds", 200, refundBody(amount, cur, "succeeded", actID))
	post.RequireHeaders = map[string]string{"Idempotency-Key": actID, "Stripe-Version": APIVersion}
	post.BodySubset = map[string]any{"charge": chargeID, "amount": "2000", "metadata[deedglass_action_id]": actID}
	if amount != 2000 {
		post.BodySubset["amount"] = jsonNum(amount)
	}
	return []saastest.Exchange{ex("charge", "GET", "/v1/charges/"+chargeID, 200, chargeBody(14900, 0, cur, false)), post}
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestCreateRefund(t *testing.T) {
	xs := refundExchanges(2000, "usd")
	xs = append(xs, ex("readback", "GET", "/v1/refunds/"+refundID, 200, refundBody(2000, "usd", "succeeded", actID)))
	r := newRig(t, nil, xs...)
	f := r.enrich(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":2000}`)
	if !f.Enriched || f.Currency != "usd" || f.Refundable != 14900 || f.AmountDisplay != "$20.00" || f.ChargeAgeDays != 2 {
		t.Fatalf("facts %+v", f)
	}
	res, err, c := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":2000,"metadata":{"ticket":"4121"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cost == nil || res.Cost.Amount != 2000 || res.Cost.Currency != "usd" {
		t.Errorf("cost %+v", res.Cost)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if out["id"] != refundID || out["status"] != "succeeded" || len(out) != 6 {
		t.Errorf("output %v", out)
	}
	body := string(r.srv.Requests()[1].Body)
	q, _ := url.ParseQuery(body)
	if q.Get("metadata[deedglass_action_id]") != actID || q.Get("metadata[ticket]") != "4121" || q.Get("amount") != "2000" {
		t.Errorf("form %s", body)
	}
	if r.srv.Count("charge") != 1 {
		t.Errorf("the charge was read %d times; enrich's read should be cached", r.srv.Count("charge"))
	}
	obs, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range fs {
		if !x.Match {
			t.Errorf("finding %s: %v vs %v", x.Field, x.Expected, x.Observed)
		}
	}
	if obs.Target != "refund "+refundID+" in Stripe" || len(fs) != 5 {
		t.Errorf("obs %+v fs %d", obs, len(fs))
	}
	r.sweep(string(res.Output))
}

func TestRefundRetrySameKey(t *testing.T) {
	xs := refundExchanges(2000, "usd")
	first := xs[1]
	first.Name, first.Status, first.Body = "refund503", 503, json.RawMessage(`{"error":{"type":"api_error","message":"try again"}}`)
	r := newRig(t, nil, xs[0], first, xs[1])
	_, err, _ := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":2000}`)
	if err != nil {
		t.Fatal(err)
	}
	reqs := r.srv.Requests()
	if len(reqs) != 3 || reqs[1].Header.Get("Idempotency-Key") != actID || reqs[2].Header.Get("Idempotency-Key") != actID {
		t.Fatalf("attempts %d", len(reqs))
	}
}

func TestShouldRetryFalse(t *testing.T) {
	xs := refundExchanges(2000, "usd")
	xs[1].Status, xs[1].Body = 500, json.RawMessage(`{"error":{"type":"api_error"}}`)
	xs[1].Headers["Stripe-Should-Retry"] = "false"
	r := newRig(t, nil, xs...)
	_, err, _ := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":2000}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Kind != connector.KindUpstream {
		t.Fatalf("err %v", err)
	}
	if n := r.srv.Count("refund"); n != 1 {
		t.Errorf("%d POSTs; want 1", n)
	}
}

func TestIdempotencyInUseRetried(t *testing.T) {
	xs := refundExchanges(2000, "usd")
	busy := xs[1]
	busy.Name, busy.Status, busy.Body = "busy", 409, json.RawMessage(`{"error":{"type":"invalid_request_error","code":"idempotency_key_in_use"}}`)
	r := newRig(t, nil, xs[0], busy, xs[1])
	if _, err, _ := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":2000}`); err != nil {
		t.Fatal(err)
	}
}

func TestArgumentRefusals(t *testing.T) {
	r := newRig(t, nil)
	for _, tc := range []struct{ tool, args, want string }{
		{ToolCreateRefund, `{"charge":"` + chargeID + `","amount":20.5}`, MsgFloatAmount},
		{ToolCreateRefund, `{"charge":"` + chargeID + `","amount":2e3}`, MsgFloatAmount},
		{ToolCreateRefund, `{"charge":"` + chargeID + `","amount":"2000"}`, MsgFloatAmount},
		{ToolCreateRefund, `{"charge":"` + chargeID + `","amount":0}`, "Send amount from 1"},
		{ToolCreateRefund, `{"charge":"` + chargeID + `","payment_intent":"pi_3QxZ8f2LdemoPI","amount":100}`, MsgChargeXorPI},
		{ToolCreateRefund, `{"amount":100}`, MsgChargeOrPI},
		{ToolUpdateCustomerMetadata, `{"customer":"` + custID + `","metadata":{"deedglass_x":"1"}}`, MsgReservedKey},
		{ToolUpdateCustomerMetadata, `{"customer":"` + custID + `","metadata":{"Deedglass_action_id":"act_x"}}`, MsgReservedKey},
		{ToolUpdateCustomerMetadata, `{"customer":"` + custID + `","metadata":{"a b":"1"}}`, "Metadata keys use"},
		{ToolUpdateCustomerMetadata, `{"customer":"` + custID + `","metadata":{"a":1}}`, "Metadata values are strings"},
		{ToolCreateRefund, `{"charge":"ch_bad","amount":100}`, "expected format"},
	} {
		_, err, _ := r.call(tc.tool, tc.args)
		ce := connector.AsError(err)
		if ce == nil || ce.Code != connector.CodeValidationFailed || !strings.Contains(ce.AgentMessage, tc.want) {
			t.Errorf("%s %s: %v", tc.tool, tc.args, err)
		}
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}

func TestRefundCaps(t *testing.T) {
	t.Run("jpy within the cap", func(t *testing.T) {
		xs := []saastest.Exchange{ex("charge", "GET", "/v1/charges/"+chargeID, 200, chargeBody(10000, 0, "jpy", false))}
		r := newRig(t, nil, xs...)
		f := r.enrich(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":5000}`)
		if f.Currency != "jpy" || f.AmountDisplay != "¥5,000" {
			t.Errorf("facts %+v", f)
		}
	})
	t.Run("above the cap", func(t *testing.T) {
		r := newRig(t, nil, ex("charge", "GET", "/v1/charges/"+chargeID, 200, chargeBody(500000, 0, "usd", false)))
		_, err, _ := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":100001}`)
		ce := connector.AsError(err)
		if ce == nil || ce.AgentMessage != "Refunds above $1,000.00 never run through this connector. A person makes them in Stripe." {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("unlisted currency", func(t *testing.T) {
		r := newRig(t, nil, ex("charge", "GET", "/v1/charges/"+chargeID, 200, chargeBody(5000, 0, "kwd", false)))
		_, err, _ := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":100}`)
		if ce := connector.AsError(err); ce == nil || !strings.Contains(ce.AgentMessage, "Refunds in KWD never run") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("enrich down", func(t *testing.T) {
		r := newRig(t, nil, ex("charge500", "GET", "/v1/charges/"+chargeID, 500, `{"error":{"type":"api_error"}}`),
			ex("charge500b", "GET", "/v1/charges/"+chargeID, 500, `{"error":{"type":"api_error"}}`))
		f := r.enrich(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":100}`)
		if f.Enriched {
			t.Fatalf("enriched with a 500: %+v", f)
		}
	})
}

func TestApprovalRequired(t *testing.T) {
	xs := refundExchanges(2000, "usd")
	xs[1].Status = 400
	xs[1].Body = json.RawMessage(`{"error":{"type":"invalid_request_error","code":"approval_required","message":"This action requires approval.","approval_request":{"id":"apreq_test_61Abc123","object":"v2.core.approval_request"}}}`)
	r := newRig(t, nil, xs...)
	_, err, _ := r.call(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":2000}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Kind != connector.KindUpstreamPending || ce.Code != connector.CodeUpstreamApprovalPending {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(ce.AdminDetail, "apreq_test_61Abc123") || !strings.Contains(ce.AdminDetail, "Settings › Approvals") {
		t.Errorf("detail %q", ce.AdminDetail)
	}
	// The id only in the message.
	_, apreq := errorBody([]byte(`{"error":{"code":"approval_required","message":"Submitted apreq_test_9zz9zz for review."}}`))
	if apreq != "apreq_test_9zz9zz" {
		t.Errorf("apreq %q", apreq)
	}
}

func TestRefundReadBack(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		amount       int64
		action       string
		wantErr      string
		wantMismatch string
	}{
		{"succeeded", "succeeded", 2000, actID, "", ""},
		{"pending", "pending", 2000, actID, "", ""},
		{"requires_action", "requires_action", 2000, actID, CodeRefundRequiresAction, ""},
		{"failed", "failed", 2000, actID, "", "status"},
		{"canceled", "canceled", 2000, actID, "", "status"},
		{"amount", "succeeded", 1999, actID, "", "amount"},
		{"metadata", "succeeded", 2000, "act_01j9zq7c4m8x2v6t0r3n5k1h7e", "", "metadata.deedglass_action_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil, ex("rb", "GET", "/v1/refunds/"+refundID, 200, refundBody(tc.amount, "usd", tc.status, tc.action)))
			_, spec := r.resolve(ToolCreateRefund)
			c := connector.Call{ActionID: mustAct(), OrgID: org, Tool: spec, Args: json.RawMessage(`{"charge":"` + chargeID + `","amount":2000}`)}
			res := connector.Result{Evidence: map[string]string{"refund": refundID, "charge_currency": "usd"}}
			_, fs, err := r.readBack(c, res)
			if tc.wantErr != "" {
				if ce := connector.AsError(err); ce == nil || ce.Code != tc.wantErr {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			bad := ""
			for _, f := range fs {
				if !f.Match && bad == "" {
					bad = f.Field
				}
			}
			if bad != tc.wantMismatch {
				t.Errorf("first mismatch %q; want %q (%+v)", bad, tc.wantMismatch, fs)
			}
		})
	}
}

func TestCancelSubscription(t *testing.T) {
	sub := func(status string, atEnd bool) string {
		b, _ := json.Marshal(map[string]any{"id": subID, "object": "subscription", "customer": custID, "status": status, "cancel_at_period_end": atEnd,
			"livemode": false, "items": map[string]any{"data": []any{map[string]any{"current_period_end": 1761868800, "quantity": 1,
				"price": map[string]any{"unit_amount": 2900, "currency": "usd", "recurring": map[string]any{"interval": "month"}}}}}})
		return string(b)
	}
	t.Run("at period end", func(t *testing.T) {
		post := ex("cancel", "POST", "/v1/subscriptions/"+subID, 200, sub("active", true))
		post.BodySubset = map[string]any{"cancel_at_period_end": "true"}
		post.RequireHeaders = map[string]string{"Idempotency-Key": actID}
		r := newRig(t, nil, post, ex("rb", "GET", "/v1/subscriptions/"+subID, 200, sub("active", true)))
		res, err, c := r.call(ToolCancelSubscription, `{"subscription":"`+subID+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		_, fs, err := r.readBack(c, res)
		if err != nil || len(fs) != 1 || !fs[0].Match {
			t.Fatalf("%v %+v", err, fs)
		}
	})
	t.Run("now", func(t *testing.T) {
		del := ex("delete", "DELETE", "/v1/subscriptions/"+subID, 200, sub("canceled", false))
		del.AbsentHeaders = []string{"Idempotency-Key"}
		r := newRig(t, nil, del, ex("rb", "GET", "/v1/subscriptions/"+subID, 200, sub("canceled", false)))
		res, err, c := r.call(ToolCancelSubscription, `{"subscription":"`+subID+`","at_period_end":false}`)
		if err != nil {
			t.Fatal(err)
		}
		_, fs, err := r.readBack(c, res)
		if err != nil || len(fs) != 1 || !fs[0].Match {
			t.Fatalf("%v %+v", err, fs)
		}
	})
	t.Run("enrich and summary", func(t *testing.T) {
		r := newRig(t, nil, ex("sub", "GET", "/v1/subscriptions/"+subID, 200, sub("active", false)))
		f := r.enrich(ToolCancelSubscription, `{"subscription":"`+subID+`"}`)
		if f.AmountPerPeriod != 2900 || f.Interval != "month" || f.CurrentPeriodEnd != 1761868800 {
			t.Fatalf("facts %+v", f)
		}
		got := New().Summarize(connector.SummaryInput{Agent: "support-bot", Qualified: "stripe.cancel_subscription",
			Args: json.RawMessage(`{"subscription":"` + subID + `"}`), Facts: map[string]any{"stripe": f.asMap()}}).Plain()
		if got != "support-bot wants to cancel "+subID+" ($29.00 / month) at the end of the period, Oct 31." {
			t.Errorf("summary %q", got)
		}
	})
}

func TestMetadataAndInvoiceItem(t *testing.T) {
	cust := `{"id":"` + custID + `","object":"customer","name":"Acme Demo Customer","email":"demo@example.test","metadata":{"tier":"gold"},"livemode":false}`
	post := ex("md", "POST", "/v1/customers/"+custID, 200, cust)
	post.BodySubset = map[string]any{"metadata[tier]": "gold", "metadata[old]": ""}
	item := `{"id":"ii_1QdemoItem01","object":"invoiceitem","amount":1500,"currency":"usd","customer":"` + custID + `","metadata":{"deedglass_action_id":"` + actID + `"},"livemode":false}`
	r := newRig(t, nil, post, ex("mdrb", "GET", "/v1/customers/"+custID, 200, cust),
		ex("ii", "POST", "/v1/invoiceitems", 200, item), ex("iirb", "GET", "/v1/invoiceitems/ii_1QdemoItem01", 200, item))
	res, err, c := r.call(ToolUpdateCustomerMetadata, `{"customer":"`+custID+`","metadata":{"tier":"gold","old":""}}`)
	if err != nil {
		t.Fatal(err)
	}
	_, fs, err := r.readBack(c, res)
	if err != nil || len(fs) != 2 || !fs[0].Match || !fs[1].Match {
		t.Fatalf("%v %+v", err, fs)
	}
	res, err, c = r.call(ToolCreateInvoiceItem, `{"customer":"`+custID+`","amount":1500,"currency":"usd","description":"Setup"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, fs, err = r.readBack(c, res)
	if err != nil || len(fs) != 4 {
		t.Fatalf("%v %+v", err, fs)
	}
	for _, f := range fs {
		if !f.Match {
			t.Errorf("%s mismatched", f.Field)
		}
	}
}

func TestReadsProjected(t *testing.T) {
	list := `{"object":"list","data":[` + chargeBody(14900, 0, "usd", false) + `],"has_more":false,"livemode":false}`
	q := ex("list", "GET", "/v1/charges", 200, list)
	q.Query = map[string]string{"limit": "5", "customer": custID}
	r := newRig(t, nil, q,
		ex("pi", "GET", "/v1/payment_intents/pi_3QxZ8f2LdemoPI", 200, `{"id":"pi_3QxZ8f2LdemoPI","amount":14900,"currency":"usd","status":"succeeded","latest_charge":"`+chargeID+`","client_secret":"pi_secret_x"}`),
		ex("refunds", "GET", "/v1/refunds", 200, `{"object":"list","data":[`+refundBody(2000, "usd", "succeeded", actID)+`],"has_more":false}`),
		ex("cust", "GET", "/v1/customers/"+custID, 200, `{"id":"`+custID+`","name":"Acme","email":"demo@example.test","metadata":{},"address":{"line1":"x"}}`))
	res, err, _ := r.call(ToolListCharges, `{"customer":"`+custID+`","limit":5}`)
	if err != nil || !strings.Contains(string(res.Output), `"has_more":false`) {
		t.Fatalf("%v %s", err, res.Output)
	}
	res, err, _ = r.call(ToolGetPaymentIntent, `{"payment_intent":"pi_3QxZ8f2LdemoPI"}`)
	if err != nil || strings.Contains(string(res.Output), "client_secret") {
		t.Fatalf("%v %s", err, res.Output)
	}
	if _, err, _ = r.call(ToolListRefunds, `{"charge":"`+chargeID+`"}`); err != nil {
		t.Fatal(err)
	}
	res, err, _ = r.call(ToolGetCustomer, `{"customer":"`+custID+`"}`)
	if err != nil || strings.Contains(string(res.Output), "line1") {
		t.Fatalf("%v %s", err, res.Output)
	}
}

func TestErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		kind   connector.Kind
	}{
		{401, `{"error":{"type":"invalid_request_error","message":"Invalid API Key"}}`, connector.KindAuth},
		{403, `{"error":{"type":"invalid_request_error","message":"The provided key does not have the required permissions"}}`, connector.KindAuth},
		{404, `{"error":{"type":"invalid_request_error","code":"resource_missing"}}`, connector.KindNotFound},
		{429, `{"error":{"type":"rate_limit_error"}}`, connector.KindRateLimited},
		{400, `{"error":{"type":"invalid_request_error","code":"charge_already_refunded"}}`, connector.KindToolError},
		{400, `{"error":{"type":"idempotency_error"}}`, connector.KindToolError},
		{502, `not json`, connector.KindUpstream},
	} {
		e := classify(&saas.Response{Status: tc.status, Body: []byte(tc.body), Header: http.Header{}})
		if e == nil || e.Kind != tc.kind {
			t.Errorf("%d %s → %v", tc.status, tc.body, e)
		}
	}
}

func TestNoLiveSwitch(t *testing.T) {
	// No config key, env var or flag can allow live mode (S30 §3.1).
	files, _ := filepath.Glob("*.go")
	bad := regexp.MustCompile(`(?i:allow_?live|enable_?live|live_?mode_?(on|enabled|allowed)|livemode_?allowed)|os\.Getenv|os\.LookupEnv|STRIPE_LIVE|\bflag\.`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if m := bad.Find(b); m != nil {
			t.Errorf("%s has %q: live mode must have no switch", f, m)
		}
	}
	var c Config
	b, _ := json.Marshal(c)
	if strings.Contains(strings.ToLower(string(b)), "live") {
		t.Errorf("config keys %s", b)
	}
	_, errs := New().Validate(context.Background(), connector.Env{}, json.RawMessage(`{"livemode":true}`))
	if len(errs) != 1 || !strings.Contains(errs[0].Message, "Live mode has no setting") {
		t.Errorf("errs %v", errs)
	}
}

func TestValidate(t *testing.T) {
	prod := connector.Env{Name: "prod"}
	norm, errs := New().Validate(context.Background(), prod, json.RawMessage(`{}`))
	if len(errs) > 0 || !strings.Contains(string(norm), `"api_version":"2026-08-26.dahlia"`) || !strings.Contains(string(norm), `"usd":100000`) {
		t.Fatalf("%s %v", norm, errs)
	}
	for _, bad := range []string{`{"api_base_url":"https://evil.test"}`, `{"refund_limits":{"USDX":1}}`, `{"refund_limits":{"usd":0}}`,
		`{"refund_reasons":["because"]}`, `{"api_version":"latest"}`, `[]`, `{"refund_limits":{}}`} {
		if _, errs := New().Validate(context.Background(), prod, json.RawMessage(bad)); len(errs) == 0 {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestSummarizeRefund(t *testing.T) {
	f := Facts{Charge: chargeID, Customer: custID, Currency: "usd", ChargeAmount: 14900, Enriched: true}
	got := New().Summarize(connector.SummaryInput{Agent: "support-bot", Qualified: "stripe.create_refund",
		Args: json.RawMessage(`{"charge":"` + chargeID + `","amount":12000}`), Facts: map[string]any{"stripe": f.asMap()}})
	if got.Plain() != "support-bot wants to refund $120.00 of $149.00 on "+chargeID+" for customer "+custID+"." {
		t.Errorf("%q", got.Plain())
	}
	if got[1].Text != " wants to refund " || got[2].Style != connector.SummaryStrong {
		t.Errorf("styles %+v", got)
	}
	f.Currency, f.ChargeAmount = "jpy", 10000
	got = New().Summarize(connector.SummaryInput{Agent: "support-bot", Args: json.RawMessage(`{"charge":"` + chargeID + `","amount":5000}`),
		Qualified: "stripe.create_refund", Facts: map[string]any{"stripe": f.asMap()}})
	if !strings.Contains(got.Plain(), "¥5,000 of ¥10,000") {
		t.Errorf("%q", got.Plain())
	}
	if New().Summarize(connector.SummaryInput{Args: json.RawMessage(`{"charge":"<@U1>","amount":5}`), Qualified: "stripe.create_refund"}) != nil {
		t.Error("a bad id summarized")
	}
}

func TestCatalog(t *testing.T) {
	info := New().Info()
	if len(info.Tools) != 10 || info.Modes[0] != "sandbox_only" {
		t.Fatalf("%d tools", len(info.Tools))
	}
	for _, tl := range info.Tools {
		if tl.SideEffect != connector.SideEffectRead && tl.ReadBack == "" {
			t.Errorf("%s has no read_back", tl.Name)
		}
		if tl.SideEffect == connector.SideEffectRead && tl.ReadBack != "" {
			t.Errorf("%s is a read with a read_back", tl.Name)
		}
	}
}

func TestForm(t *testing.T) {
	f, err := Form(map[string]any{"amount": int64(2000), "charge": chargeID, "metadata": map[string]any{"b": "2", "a": "1"},
		"items": []any{map[string]any{"price": "price_1"}}, "confirm": true})
	if err != nil {
		t.Fatal(err)
	}
	want := "amount=2000&charge=" + chargeID + "&confirm=true&items%5B0%5D%5Bprice%5D=price_1&metadata%5Ba%5D=1&metadata%5Bb%5D=2"
	if f.Encode() != want {
		t.Errorf("%s\nwant %s", f.Encode(), want)
	}
	if _, err := Form(map[string]any{"amount": 20.5}); err == nil {
		t.Error("a float was encoded")
	}
}

func TestConnectorTest(t *testing.T) {
	bal := ex("balance", "GET", "/v1/balance", 200, `{"object":"balance","livemode":false}`)
	probe := func(name, path string, status int) saastest.Exchange {
		x := ex(name, "GET", path, status, `{"object":"list","data":[],"livemode":false}`)
		if status == 403 {
			x.Body = json.RawMessage(`{"error":{"type":"invalid_request_error","message":"Your key needs rak_subscription_read."}}`)
		}
		return x
	}
	t.Run("restricted", func(t *testing.T) {
		r := newRig(t, nil, bal, probe("p1", "/v1/charges", 200), probe("p2", "/v1/customers", 200), probe("p3", "/v1/subscriptions", 403))
		rep, err := New().Test(context.Background(), r.inst())
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, c := range rep.Checks {
			got = append(got, c.ID+":"+string(c.Status))
		}
		want := "config:pass credentials:pass destination:pass auth:pass version:pass permissions:warn writes:skip"
		if strings.Join(got, " ") != want {
			t.Errorf("%s\nwant %s", strings.Join(got, " "), want)
		}
		if rep.Checks[1].Detail != "restricted key · rk_test_…9f2c" || !strings.Contains(rep.Checks[5].Detail, "rak_subscription_read") {
			t.Errorf("%+v", rep.Checks)
		}
		r.sweep(rep.Checks[1].Detail)
	})
	t.Run("secret key warns", func(t *testing.T) {
		r := newRig(t, nil, bal, probe("p1", "/v1/charges", 200), probe("p2", "/v1/customers", 200), probe("p3", "/v1/subscriptions", 200))
		r.op.key = secretSK
		rep, _ := New().Test(context.Background(), r.inst())
		if rep.Checks[1].Status != connector.CheckWarn || !strings.Contains(rep.Checks[1].Detail, MsgSecretKeyWarn) {
			t.Errorf("%+v", rep.Checks[1])
		}
	})
	t.Run("livemode", func(t *testing.T) {
		r := newRig(t, nil, ex("balance", "GET", "/v1/balance", 200, `{"object":"balance","livemode":true}`))
		rep, _ := New().Test(context.Background(), r.inst())
		if rep.Status != connector.CheckFail || rep.Checks[3].Status != connector.CheckFail || r.br.trips.Load() != 1 {
			t.Errorf("%+v trips %d", rep.Checks, r.br.trips.Load())
		}
	})
	t.Run("live key stored", func(t *testing.T) {
		r := newRig(t, nil)
		r.op.key = liveKey
		rep, _ := New().Test(context.Background(), r.inst())
		if rep.Checks[1].Status != connector.CheckFail || len(r.srv.Requests()) != 0 || r.br.trips.Load() != 1 {
			t.Errorf("%+v", rep.Checks[1])
		}
		r.sweep(rep.Checks[1].Detail)
	})
}

func mustAct() ids.ActionID { return ids.MustParse[ids.ActionKind](actID) }

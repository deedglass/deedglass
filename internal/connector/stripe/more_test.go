package stripe

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
)

func TestEnrichPaths(t *testing.T) {
	pi := ex("pi", "GET", "/v1/payment_intents/pi_3QxZ8f2LdemoPI", 200, `{"id":"pi_3QxZ8f2LdemoPI","latest_charge":{"id":"`+chargeID+`"},"livemode":false}`)
	ch := ex("charge", "GET", "/v1/charges/"+chargeID, 200, `{"id":"`+chargeID+`","amount":14900,"amount_refunded":20000,"currency":"USD","customer":"`+custID+`","payment_intent":{"id":"pi_3QxZ8f2LdemoPI"},"disputed":true,"livemode":false}`)
	r := newRig(t, nil, pi, ch)
	f := r.enrich(ToolCreateRefund, `{"payment_intent":"pi_3QxZ8f2LdemoPI","amount":100}`)
	if !f.Enriched || f.Charge != chargeID || f.PaymentIntent != "pi_3QxZ8f2LdemoPI" || f.Refundable != 0 || !f.Disputed || f.Currency != "usd" {
		t.Fatalf("%+v", f)
	}
	got := New().Summarize(connector.SummaryInput{Agent: "", Qualified: "stripe.create_refund",
		Args: json.RawMessage(`{"charge":"` + chargeID + `","amount":100}`), Facts: map[string]any{"stripe": f.asMap()}}).Plain()
	if !strings.HasPrefix(got, "An agent wants to refund $1.00 of $149.00") || !strings.HasSuffix(got, "The charge is disputed.") {
		t.Errorf("%q", got)
	}
	// Invoice item: no call; reads: no facts; bad args: zero facts.
	f = r.enrich(ToolCreateInvoiceItem, `{"customer":"`+custID+`","amount":1500,"currency":"eur"}`)
	if f.AmountDisplay != "€15.00" || !f.Enriched {
		t.Errorf("%+v", f)
	}
	ex2, spec := r.resolve(ToolGetCharge)
	if m, _ := ex2.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec, json.RawMessage(`{"charge":"`+chargeID+`"}`)); m != nil {
		t.Errorf("a read enriched: %v", m)
	}
	if f := r.enrich(ToolCreateRefund, `not json`); f.Enriched {
		t.Error("bad args enriched")
	}
	// A PaymentIntent without a charge yet, and one that can't be read.
	r2 := newRig(t, nil, ex("pi", "GET", "/v1/payment_intents/pi_3QxZ8f2LdemoPI", 200, `{"id":"pi_3QxZ8f2LdemoPI","latest_charge":null,"livemode":false}`))
	if f := r2.enrich(ToolCreateRefund, `{"payment_intent":"pi_3QxZ8f2LdemoPI","amount":100}`); f.Enriched || f.PaymentIntent == "" {
		t.Errorf("%+v", f)
	}
	r3 := newRig(t, nil, ex("pi", "GET", "/v1/payment_intents/pi_3QxZ8f2LdemoPI", 404, `{"error":{"type":"invalid_request_error"}}`))
	if f := r3.enrich(ToolCreateRefund, `{"payment_intent":"pi_3QxZ8f2LdemoPI","amount":100}`); f.Enriched {
		t.Errorf("%+v", f)
	}
	r4 := newRig(t, nil, ex("sub", "GET", "/v1/subscriptions/"+subID, 500, `{"error":{"type":"api_error"}}`), ex("sub2", "GET", "/v1/subscriptions/"+subID, 500, `{}`))
	if f := r4.enrich(ToolCancelSubscription, `{"subscription":"`+subID+`"}`); f.Enriched || f.Subscription != subID {
		t.Errorf("%+v", f)
	}
}

func TestEnrichCacheExpires(t *testing.T) {
	r := newRig(t, nil, ex("c1", "GET", "/v1/charges/"+chargeID, 200, chargeBody(1000, 0, "usd", false)),
		ex("c2", "GET", "/v1/charges/"+chargeID, 200, chargeBody(1000, 500, "usd", false)))
	old := now
	defer func() { now = old }()
	at := time.Now()
	now = func() time.Time { return at }
	r.enrich(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":100}`)
	if f := r.enrich(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":100}`); f.AmountRefunded != 0 {
		t.Error("the cache missed")
	}
	at = at.Add(EnrichTTL + time.Second)
	if f := r.enrich(ToolCreateRefund, `{"charge":"`+chargeID+`","amount":100}`); f.AmountRefunded != 500 {
		t.Error("the cache didn't expire")
	}
}

func TestSummarizeOthers(t *testing.T) {
	c := New()
	for _, tc := range []struct{ tool, args, want string }{
		{"stripe.create_invoice_item", `{"customer":"` + custID + `","amount":1500,"currency":"usd"}`, "bot wants to add $15.00 to the next invoice of " + custID + "."},
		{"stripe.update_customer_metadata", `{"customer":"` + custID + `","metadata":{"a":"1"}}`, "bot wants to change 1 metadata key on " + custID + "."},
		{"stripe.update_customer_metadata", `{"customer":"` + custID + `","metadata":{"a":"1","b":"2"}}`, "bot wants to change 2 metadata keys on " + custID + "."},
		{"stripe.cancel_subscription", `{"subscription":"` + subID + `","at_period_end":false}`, "bot wants to cancel " + subID + " now."},
		{"stripe.create_refund", `{"charge":"` + chargeID + `","amount":100}`, "bot wants to refund 100 minor units on " + chargeID + ". Deedglass couldn't read the charge, so the currency isn't known."},
	} {
		if got := c.Summarize(connector.SummaryInput{Agent: "bot", Qualified: tc.tool, Args: json.RawMessage(tc.args)}).Plain(); got != tc.want {
			t.Errorf("%s: %q", tc.tool, got)
		}
	}
	for _, bad := range []string{`{"customer":"x"}`, `nope`} {
		if c.Summarize(connector.SummaryInput{Qualified: "stripe.create_invoice_item", Args: json.RawMessage(bad)}) != nil {
			t.Errorf("%s summarized", bad)
		}
	}
	if c.Summarize(connector.SummaryInput{Qualified: "stripe.get_charge", Args: json.RawMessage(`{}`)}) != nil {
		t.Error("a read summarized")
	}
}

func TestClassifyMore(t *testing.T) {
	for _, tc := range []struct {
		st   int
		body string
		h    map[string]string
		kind connector.Kind
		rtr  bool
	}{
		{409, `{"error":{"type":"invalid_request_error","code":"idempotency_key_in_use"}}`, nil, connector.KindUpstream, true},
		{503, `{}`, map[string]string{"Stripe-Should-Retry": "true"}, connector.KindUpstream, true},
		{429, `{}`, map[string]string{"Retry-After": "1"}, connector.KindRateLimited, true},
		{400, `{"error":{"type":"card_error","code":"x","message":"` + strings.Repeat("m", 400) + `"}}`, map[string]string{"Stripe-Should-Retry": "false"}, connector.KindToolError, false},
		{402, `{"error":{"type":"card_error"}}`, nil, connector.KindToolError, false},
	} {
		h := http.Header{}
		for k, v := range tc.h {
			h.Set(k, v)
		}
		resp := &saas.Response{Status: tc.st, Body: []byte(tc.body), Header: h}
		e := classify(resp)
		retry, _ := retryIf(resp)
		if e == nil || e.Kind != tc.kind || retry != tc.rtr {
			t.Errorf("%d: %v retry %v", tc.st, e, retry)
		}
		if e != nil && e.Err != nil && e.Err.Error() == "" {
			t.Error("empty error text")
		}
	}
	if classify(&saas.Response{Status: 200}) != nil {
		t.Error("a 200 is an error")
	}
	if Prefix("abc") != "…" || Hint("rk_test_1") != "rk_test_…" {
		t.Errorf("%q %q", Prefix("abc"), Hint("rk_test_1"))
	}
	if Livemode([]byte(`not json "livemode": true`)) != true || Livemode([]byte(`{"livemode":"true"}`)) != true || Livemode([]byte(`{"a":1}`)) {
		t.Error("livemode raw scan")
	}
	deep := strings.Repeat(`{"a":`, 70) + `1` + strings.Repeat(`}`, 70)
	if !Livemode([]byte(strings.Replace(deep, `1`, `{"livemode":false}`, 1))) {
		t.Error("an absurdly deep answer should be treated as suspicious")
	}
}

func TestFormAndValidateMore(t *testing.T) {
	f, err := Form(map[string]any{"a": map[string]string{"x": "1"}, "n": 5, "list": []any{"p", int64(2), false}})
	if err != nil || f.Get("a[x]") != "1" || f.Get("n") != "5" || f.Get("list[2]") != "false" {
		t.Fatalf("%v %v", f, err)
	}
	deep := map[string]any{}
	cur := deep
	for i := 0; i < 6; i++ {
		n := map[string]any{}
		cur["k"] = n
		cur = n
	}
	cur["v"] = "x"
	if _, err := Form(map[string]any{"d": deep}); err == nil {
		t.Error("too deep accepted")
	}
	env := connector.Env{Name: "dev"}
	for _, bad := range []string{`{"api_base_url":"ftp://x"}`, `{"zz":` + strings.Repeat(`"`, 1) + `x"}`, strings.Repeat(" ", 17<<10), `{"refund_limits":5}`, `{"` + strings.Repeat("k", 80) + `":1}`} {
		if _, errs := New().Validate(context.Background(), env, json.RawMessage(bad)); len(errs) == 0 {
			t.Errorf("%.40s accepted", bad)
		}
	}
	if _, errs := New().Validate(context.Background(), env, nil); len(errs) != 0 {
		t.Errorf("empty config: %v", errs)
	}
}

func TestReadBackSkipsAndDecodeErrors(t *testing.T) {
	r := newRig(t, nil, ex("ii", "GET", "/v1/invoiceitems/ii_1QdemoItem01", 404, `{"error":{"type":"invalid_request_error"}}`))
	_, spec := r.resolve(ToolGetCharge)
	c := connector.Call{ActionID: mustAct(), OrgID: org, Tool: spec, Args: json.RawMessage(`{"charge":"` + chargeID + `"}`)}
	if _, _, err := New().NativeReadBack(context.Background(), r.inst(), c, connector.Result{}); err == nil {
		t.Error("a read was read back")
	}
	_, spec = r.resolve(ToolCreateRefund)
	c.Tool, c.Args = spec, json.RawMessage(`{"charge":"`+chargeID+`","amount":1}`)
	if _, _, err := New().NativeReadBack(context.Background(), r.inst(), c, connector.Result{}); err == nil {
		t.Error("no refund id, no skip")
	}
	_, spec = r.resolve(ToolCreateInvoiceItem)
	c.Tool, c.Args = spec, json.RawMessage(`{"customer":"`+custID+`","amount":1,"currency":"usd"}`)
	if _, _, err := New().NativeReadBack(context.Background(), r.inst(), c, connector.Result{}); err == nil {
		t.Error("no invoice item, no skip")
	}
	if _, _, err := New().NativeReadBack(context.Background(), r.inst(), c, connector.Result{Evidence: map[string]string{"invoice_item": "ii_1QdemoItem01"}}); connector.AsError(err) == nil {
		t.Errorf("404: %v", err)
	}
	r2 := newRig(t, nil, ex("bad", "GET", "/v1/charges/"+chargeID, 200, `{not json`))
	if _, err, _ := r2.call(ToolGetCharge, `{"charge":"`+chargeID+`"}`); connector.AsError(err) == nil {
		t.Errorf("a broken answer: %v", err)
	}
}

func TestTestFailures(t *testing.T) {
	r := newRig(t, nil, ex("balance", "GET", "/v1/balance", 401, `{"error":{"type":"invalid_request_error"}}`))
	rep, _ := New().Test(context.Background(), r.inst())
	if rep.Checks[3].Status != connector.CheckFail || !strings.Contains(rep.Checks[3].Detail, "refused the key") {
		t.Errorf("%+v", rep.Checks[3])
	}
	r = newRig(t, nil, ex("balance", "GET", "/v1/balance", 429, `{}`))
	rep, _ = New().Test(context.Background(), r.inst())
	if !strings.Contains(rep.Checks[3].Detail, "limiting") {
		t.Errorf("%+v", rep.Checks[3])
	}
	bal := ex("balance", "GET", "/v1/balance", 200, `{"livemode":false}`)
	bal.Headers["Stripe-Version"] = "2020-08-27"
	r = newRig(t, nil, bal, ex("p1", "GET", "/v1/charges", 500, `{}`), ex("p1b", "GET", "/v1/charges", 500, `{}`))
	rep, _ = New().Test(context.Background(), r.inst())
	if rep.Checks[4].Status != connector.CheckFail || rep.Checks[5].Status != connector.CheckFail {
		t.Errorf("%+v", rep.Checks)
	}
	r = newRig(t, nil)
	r.op.err = errUnusedVault
	rep, _ = New().Test(context.Background(), r.inst())
	if rep.Checks[1].Status != connector.CheckFail {
		t.Errorf("%+v", rep.Checks[1])
	}
	if _, err := New().Tools(context.Background(), r.inst()); err != nil {
		t.Error(err)
	}
	_ = saastest.Exchange{}
}

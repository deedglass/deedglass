package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/money"
)

// Messages for arguments Deedglass refuses (S30 §5).
const (
	MsgFloatAmount   = "Send amount as an integer in minor units, like 2000 for $20.00."
	MsgReservedKey   = "Metadata keys starting with deedglass_ are reserved."
	MsgChargeXorPI   = "Send charge or payment_intent, not both."
	MsgChargeOrPI    = "Send charge or payment_intent."
	MetadataActionID = "deedglass_action_id"
)

var reMetaKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// args is every tool's arguments (the schemas are closed; CheckArgs and ValidateArgs ran first).
type args struct {
	Customer      string            `json:"customer"`
	Charge        string            `json:"charge"`
	PaymentIntent string            `json:"payment_intent"`
	Subscription  string            `json:"subscription"`
	Amount        int64             `json:"amount"`
	Currency      string            `json:"currency"`
	Reason        string            `json:"reason"`
	Description   string            `json:"description"`
	Metadata      map[string]string `json:"metadata"`
	Limit         int               `json:"limit"`
	StartingAfter string            `json:"starting_after"`
	AtPeriodEnd   *bool             `json:"at_period_end"`
}

func decodeArgs(raw json.RawMessage) (args, error) {
	var a args
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&a); err != nil {
		return a, connector.ErrValidation("args", "can't be read: "+strings.TrimPrefix(err.Error(), "json: "))
	}
	return a, nil
}

func validation(param, msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed, AgentMessage: msg,
		AdminDetail: param + ": " + msg}
}

// CheckArgs implements connector.ArgsChecker: Stripe's own argument rules, before the schema, so
// the message says exactly what to send. A float amount never reaches Stripe.
func (c *Connector) CheckArgs(t connector.ToolSpec, raw json.RawMessage) error {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil // the schema check words it
	}
	if a, ok := m["amount"]; ok {
		var n json.Number
		if t := bytes.TrimSpace(a); len(t) == 0 || t[0] == '"' || json.Unmarshal(a, &n) != nil {
			return validation("args.amount", MsgFloatAmount)
		}
		if strings.ContainsAny(n.String(), ".eE") {
			return validation("args.amount", MsgFloatAmount)
		}
		if v, err := strconv.ParseInt(n.String(), 10, 64); err != nil || v < 1 || v > MaxAmount {
			return validation("args.amount", "Send amount from 1 to 99,999,999 minor units, like 2000 for $20.00.")
		}
	}
	if t.Name == ToolCreateRefund || t.Name == ToolListRefunds {
		_, hasCh := m["charge"]
		_, hasPI := m["payment_intent"]
		switch {
		case hasCh && hasPI:
			return validation("args.charge", MsgChargeXorPI)
		case !hasCh && !hasPI:
			return validation("args.charge", MsgChargeOrPI)
		}
	}
	if md, ok := m["metadata"]; ok {
		var meta map[string]any
		if json.Unmarshal(md, &meta) != nil {
			return validation("args.metadata", "Send metadata as an object of string values.")
		}
		if len(meta) > 20 {
			return validation("args.metadata", "Send at most 20 metadata keys.")
		}
		for k, v := range meta {
			if strings.HasPrefix(strings.ToLower(k), "deedglass_") {
				return validation("args.metadata", MsgReservedKey)
			}
			if !reMetaKey.MatchString(k) {
				return validation("args.metadata", "Metadata keys use letters, digits, _ and -, at most 40 characters.")
			}
			s, ok := v.(string)
			if !ok {
				return validation("args.metadata."+k, "Metadata values are strings.")
			}
			if len([]rune(s)) > 500 {
				return validation("args.metadata."+k, "Metadata values are at most 500 characters.")
			}
		}
		if t.Name == ToolUpdateCustomerMetadata && len(meta) == 0 {
			return validation("args.metadata", "Send at least one metadata key.")
		}
	}
	return nil
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	a, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Result{}, err
	}
	s, err := open(ctx, inst)
	if err != nil {
		return connector.Result{}, err
	}
	defer s.close()
	switch call.Tool.Name {
	case ToolGetCustomer:
		var o customer
		return s.read(ctx, saas.MustSegments("v1", "customers", a.Customer), nil, &o, func() any { return o.project() })
	case ToolGetCharge:
		var o charge
		return s.read(ctx, saas.MustSegments("v1", "charges", a.Charge), nil, &o, func() any { return o.project() })
	case ToolGetPaymentIntent:
		var o paymentIntent
		return s.read(ctx, saas.MustSegments("v1", "payment_intents", a.PaymentIntent), nil, &o, func() any { return o.project() })
	case ToolGetSubscription:
		var o subscription
		return s.read(ctx, saas.MustSegments("v1", "subscriptions", a.Subscription), nil, &o, func() any { return o.project() })
	case ToolListCharges:
		q := url.Values{"limit": {strconv.Itoa(limitOf(a.Limit))}}
		if a.Customer != "" {
			q.Set("customer", a.Customer)
		}
		if a.StartingAfter != "" {
			q.Set("starting_after", a.StartingAfter)
		}
		var o list[charge]
		return s.read(ctx, saas.MustSegments("v1", "charges"), q, &o, func() any { return o.project(func(x charge) any { return x.project() }) })
	case ToolListRefunds:
		q := url.Values{"limit": {strconv.Itoa(limitOf(a.Limit))}}
		if a.Charge != "" {
			q.Set("charge", a.Charge)
		} else {
			q.Set("payment_intent", a.PaymentIntent)
		}
		var o list[refund]
		return s.read(ctx, saas.MustSegments("v1", "refunds"), q, &o, func() any { return o.project(func(x refund) any { return x.project() }) })
	case ToolCreateRefund:
		return s.createRefund(ctx, call, a)
	case ToolCreateInvoiceItem:
		return s.createInvoiceItem(ctx, call, a)
	case ToolUpdateCustomerMetadata:
		return s.updateMetadata(ctx, call, a)
	case ToolCancelSubscription:
		return s.cancel(ctx, call, a)
	}
	return connector.Result{}, connector.ErrValidation("tool", "isn't a Stripe tool")
}

func limitOf(n int) int {
	if n < 1 || n > 25 {
		return 10
	}
	return n
}

func (s *session) read(ctx context.Context, path string, q url.Values, out any, project func() any) (connector.Result, error) {
	resp, err := s.get(ctx, path, q, out)
	if err != nil {
		return connector.Result{}, err
	}
	return result(resp, project(), nil, nil)
}

func result(resp *saas.Response, out any, ev map[string]string, cost *connector.Money) (connector.Result, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return connector.Result{}, err
	}
	meta := map[string]any{"http_status": resp.Status, "attempts": resp.Attempts}
	if id := requestID(resp); id != "req_…" {
		meta["stripe_request_id"] = id
	}
	return connector.Result{Output: b, Meta: meta, Evidence: ev, Cost: cost, Latency: resp.Latency}, nil
}

// withActionID is the agent's metadata plus metadata[deedglass_action_id].
func withActionID(md map[string]string, actionID string) map[string]any {
	out := map[string]any{}
	for k, v := range md {
		out[k] = v
	}
	out[MetadataActionID] = actionID
	return out
}

func (s *session) createRefund(ctx context.Context, call connector.Call, a args) (connector.Result, error) {
	// The cap needs the charge's real currency: read it (enrich cached it a moment ago).
	f := s.refundFacts(ctx, a)
	if !f.Enriched {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeUpstreamError,
			AgentMessage: "Deedglass couldn't read the charge from Stripe, so it didn't send the refund. Try again in a minute."}
	}
	limit, ok := s.st.cfg.RefundLimits[f.Currency]
	if !ok {
		return connector.Result{}, validation("args.amount", fmt.Sprintf("Refunds in %s never run through this connector. A person makes them in Stripe.", strings.ToUpper(f.Currency)))
	}
	if a.Amount > limit {
		return connector.Result{}, validation("args.amount", fmt.Sprintf("Refunds above %s never run through this connector. A person makes them in Stripe.", money.Format(limit, f.Currency)))
	}
	if a.Reason != "" && !contains(s.st.cfg.RefundReasons, a.Reason) {
		return connector.Result{}, validation("args.reason", "This connector doesn't allow that refund reason. The allowed reasons are "+strings.Join(s.st.cfg.RefundReasons, ", ")+".")
	}
	params := map[string]any{"amount": a.Amount, "metadata": withActionID(a.Metadata, call.ActionID.String())}
	if a.Charge != "" {
		params["charge"] = a.Charge
	} else {
		params["payment_intent"] = a.PaymentIntent
	}
	if a.Reason != "" {
		params["reason"] = a.Reason
	}
	var r refund
	resp, err := s.post(ctx, saas.MustSegments("v1", "refunds"), params, call.ActionID.String(), &r)
	if err != nil {
		return connector.Result{}, err
	}
	ev := map[string]string{"refund": r.ID, "charge_currency": f.Currency, "charge": f.Charge}
	cost := &connector.Money{Amount: r.Amount, Currency: strings.ToLower(r.Currency)}
	return result(resp, r.project(), ev, cost)
}

func (s *session) createInvoiceItem(ctx context.Context, call connector.Call, a args) (connector.Result, error) {
	params := map[string]any{"customer": a.Customer, "amount": a.Amount, "currency": a.Currency,
		"metadata": withActionID(a.Metadata, call.ActionID.String())}
	if a.Description != "" {
		params["description"] = a.Description
	}
	var it invoiceItem
	resp, err := s.post(ctx, saas.MustSegments("v1", "invoiceitems"), params, call.ActionID.String(), &it)
	if err != nil {
		return connector.Result{}, err
	}
	return result(resp, it.project(), map[string]string{"invoice_item": it.ID}, nil)
}

func (s *session) updateMetadata(ctx context.Context, call connector.Call, a args) (connector.Result, error) {
	md := map[string]any{}
	for k, v := range a.Metadata {
		md[k] = v
	}
	var o customer
	resp, err := s.post(ctx, saas.MustSegments("v1", "customers", a.Customer), map[string]any{"metadata": md}, call.ActionID.String(), &o)
	if err != nil {
		return connector.Result{}, err
	}
	return result(resp, o.project(), map[string]string{"customer": o.ID}, nil)
}

func (s *session) cancel(ctx context.Context, call connector.Call, a args) (connector.Result, error) {
	atEnd := a.AtPeriodEnd == nil || *a.AtPeriodEnd
	path := saas.MustSegments("v1", "subscriptions", a.Subscription)
	var o subscription
	var (
		resp *saas.Response
		err  error
	)
	if atEnd {
		resp, err = s.post(ctx, path, map[string]any{"cancel_at_period_end": true,
			"metadata": map[string]any{MetadataActionID: call.ActionID.String()}}, call.ActionID.String(), &o)
	} else {
		resp, err = s.del(ctx, path, &o)
	}
	if err != nil {
		return connector.Result{}, err
	}
	return result(resp, o.project(), map[string]string{"subscription": o.ID, "at_period_end": strconv.FormatBool(atEnd)}, nil)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

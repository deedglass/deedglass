package stripe

import (
	"context"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Read-back outcomes of a refund's status.
const (
	refundStatusOK = "succeeded or pending"
	// CodeRefundRequiresAction is the read-back error code when Stripe needs the customer to act:
	// a gap (UNVERIFIABLE), never a pass or a mismatch.
	CodeRefundRequiresAction = "refund_requires_action"
)

// NativeReadBack implements connector.NativeReadBacker (S30 §3.1): one GET of the object the
// write made or changed, compared field by field with what the call implies.
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	a, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	s, err := open(ctx, inst)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer s.close()
	ev := res.Evidence
	switch call.Tool.Name {
	case ToolCreateRefund:
		return s.readBackRefund(ctx, call, a, ev)
	case ToolCreateInvoiceItem:
		return s.readBackInvoiceItem(ctx, call, a, ev)
	case ToolUpdateCustomerMetadata:
		return s.readBackMetadata(ctx, a)
	case ToolCancelSubscription:
		return s.readBackCancel(ctx, a, ev)
	}
	return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the Stripe tool " + call.Tool.Name + " isn't read back"}
}

func (s *session) readOne(ctx context.Context, obs *connector.Observation, path string, out any) error {
	t0 := time.Now()
	resp, err := s.get(ctx, path, nil, out)
	obs.LatencyMS = time.Since(t0).Milliseconds()
	if resp != nil {
		obs.Status = resp.Status
		obs.Meta = map[string]any{"attempts": resp.Attempts}
		if id := requestID(resp); id != "req_…" {
			obs.Meta["stripe_request_id"] = id
		}
	}
	return err
}

func (s *session) readBackRefund(ctx context.Context, call connector.Call, a args, ev map[string]string) (connector.Observation, []connector.L1Finding, error) {
	id := ev["refund"]
	if !strings.HasPrefix(id, "re_") && !strings.HasPrefix(id, "pyr_") {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's answer names no refund to read back"}
	}
	obs := connector.Observation{Target: "refund " + id + " in Stripe"}
	var r refund
	if err := s.readOne(ctx, &obs, saas.MustSegments("v1", "refunds", id), &r); err != nil {
		return obs, nil, err
	}
	if r.Status == "requires_action" {
		return obs, nil, &connector.Error{Kind: connector.KindToolError, Code: CodeRefundRequiresAction,
			AgentMessage: "Stripe needs the customer to act before this refund completes."}
	}
	fs := []connector.L1Finding{
		connector.Compare("amount", a.Amount, r.Amount),
		connector.Compare("currency", ev["charge_currency"], strings.ToLower(r.Currency)),
	}
	if a.Charge != "" {
		fs = append(fs, connector.Compare("charge", a.Charge, r.Charge))
	} else {
		fs = append(fs, connector.Compare("payment_intent", a.PaymentIntent, r.PaymentIntent))
	}
	fs = append(fs, connector.Compare("metadata."+MetadataActionID, call.ActionID.String(), r.Metadata[MetadataActionID]))
	st := r.Status
	if st == "succeeded" || st == "pending" {
		st = refundStatusOK
	}
	fs = append(fs, connector.Compare("status", refundStatusOK, st))
	return obs, fs, nil
}

func (s *session) readBackInvoiceItem(ctx context.Context, call connector.Call, a args, ev map[string]string) (connector.Observation, []connector.L1Finding, error) {
	id := ev["invoice_item"]
	if !strings.HasPrefix(id, "ii_") {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's answer names no invoice item to read back"}
	}
	obs := connector.Observation{Target: "invoice item " + id + " in Stripe"}
	var it invoiceItem
	if err := s.readOne(ctx, &obs, saas.MustSegments("v1", "invoiceitems", id), &it); err != nil {
		return obs, nil, err
	}
	return obs, []connector.L1Finding{
		connector.Compare("amount", a.Amount, it.Amount),
		connector.Compare("currency", strings.ToLower(a.Currency), strings.ToLower(it.Currency)),
		connector.Compare("customer", a.Customer, it.Customer),
		connector.Compare("metadata."+MetadataActionID, call.ActionID.String(), it.Metadata[MetadataActionID]),
	}, nil
}

func (s *session) readBackMetadata(ctx context.Context, a args) (connector.Observation, []connector.L1Finding, error) {
	obs := connector.Observation{Target: "customer " + a.Customer + " in Stripe"}
	var o customer
	if err := s.readOne(ctx, &obs, saas.MustSegments("v1", "customers", a.Customer), &o); err != nil {
		return obs, nil, err
	}
	keys := make([]string, 0, len(a.Metadata))
	for k := range a.Metadata {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var fs []connector.L1Finding
	for _, k := range keys {
		want := a.Metadata[k]
		got, present := o.Metadata[k]
		if want == "" {
			obsv := "absent"
			if present {
				obsv = got
			}
			fs = append(fs, connector.Compare("metadata."+k, "absent", obsv))
			continue
		}
		fs = append(fs, connector.Compare("metadata."+k, want, got))
	}
	return obs, fs, nil
}

func (s *session) readBackCancel(ctx context.Context, a args, ev map[string]string) (connector.Observation, []connector.L1Finding, error) {
	obs := connector.Observation{Target: "subscription " + a.Subscription + " in Stripe"}
	var o subscription
	if err := s.readOne(ctx, &obs, saas.MustSegments("v1", "subscriptions", a.Subscription), &o); err != nil {
		return obs, nil, err
	}
	if ev["at_period_end"] == "false" {
		return obs, []connector.L1Finding{connector.Compare("status", "canceled", o.Status)}, nil
	}
	got := "active"
	if o.Status == "canceled" || o.CancelAtPeriodEnd {
		got = "canceled or canceling at period end"
	}
	return obs, []connector.L1Finding{connector.Compare("cancellation", "canceled or canceling at period end", got)}, nil
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

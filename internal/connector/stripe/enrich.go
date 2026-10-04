package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/money"
)

// EnrichTimeout bounds the enrich read: the decision never waits longer on Stripe.
const EnrichTimeout = 3 * time.Second

// Facts is request.stripe (policy.StripeFacts has the same JSON). Every amount is minor units of
// Currency, read from Stripe, never from the agent's words.
type Facts struct {
	Charge           string `json:"charge"`
	PaymentIntent    string `json:"payment_intent"`
	Customer         string `json:"customer"`
	Currency         string `json:"currency"`
	ChargeAmount     int64  `json:"charge_amount"`
	AmountRefunded   int64  `json:"amount_refunded"`
	Refundable       int64  `json:"refundable"`
	Disputed         bool   `json:"disputed"`
	ChargeAgeDays    int64  `json:"charge_age_days"`
	AmountDisplay    string `json:"amount_display"`
	Subscription     string `json:"subscription"`
	Status           string `json:"status"`
	CurrentPeriodEnd int64  `json:"current_period_end"`
	AmountPerPeriod  int64  `json:"amount_per_period"`
	Interval         string `json:"interval"`
	Enriched         bool   `json:"enriched"`
}

func (f Facts) asMap() map[string]any {
	b, _ := json.Marshal(f)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// Enrich implements connector.Enricher: {"stripe": {…}} for create_refund (the charge),
// cancel_subscription (the subscription) and create_invoice_item (the display amount). A read
// that fails leaves enriched false, which the starter policy sends to a person: it never loosens
// a decision.
func (c *Connector) Enrich(ctx context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	switch t.Name {
	case ToolCreateRefund, ToolCancelSubscription, ToolCreateInvoiceItem:
	default:
		return nil, nil
	}
	a, err := decodeArgs(raw)
	if err != nil {
		return map[string]any{"stripe": Facts{}.asMap()}, nil
	}
	if t.Name == ToolCreateInvoiceItem {
		cur := strings.ToLower(a.Currency)
		return map[string]any{"stripe": Facts{Customer: a.Customer, Currency: cur, AmountDisplay: money.Format(a.Amount, cur), Enriched: reCurrency.MatchString(cur)}.asMap()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, EnrichTimeout)
	defer cancel()
	s, err := open(ctx, inst)
	if err != nil {
		return map[string]any{"stripe": Facts{}.asMap()}, nil
	}
	defer s.close()
	if t.Name == ToolCancelSubscription {
		return map[string]any{"stripe": s.subscriptionFacts(ctx, a).asMap()}, nil
	}
	return map[string]any{"stripe": s.refundFacts(ctx, a).asMap()}, nil
}

// object reads one object through the 30-second cache.
func (s *session) object(ctx context.Context, path, id string) (map[string]any, error) {
	s.st.mu.Lock()
	if c, ok := s.st.cache[id]; ok && now().Sub(c.at) < EnrichTTL {
		s.st.mu.Unlock()
		return c.body, nil
	}
	s.st.mu.Unlock()
	var body map[string]any
	if _, err := s.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	s.st.mu.Lock()
	if len(s.st.cache) > 1000 {
		s.st.cache = map[string]cached{}
	}
	s.st.cache[id] = cached{at: now(), body: body}
	s.st.mu.Unlock()
	return body, nil
}

func asCharge(m map[string]any) (charge, bool) {
	// An expanded payment_intent or customer is an object: keep its id.
	flat := make(map[string]any, len(m))
	for k, v := range m {
		if o, ok := v.(map[string]any); ok && (k == "payment_intent" || k == "customer") {
			v, _ = o["id"].(string)
		}
		flat[k] = v
	}
	m = flat
	b, err := json.Marshal(m)
	if err != nil {
		return charge{}, false
	}
	var c charge
	if json.Unmarshal(b, &c) != nil || c.ID == "" {
		return charge{}, false
	}
	return c, true
}

// refundFacts reads the charge (or the PaymentIntent's latest charge).
func (s *session) refundFacts(ctx context.Context, a args) Facts {
	chID := a.Charge
	piID := a.PaymentIntent
	if chID == "" && piID != "" {
		m, err := s.object(ctx, saas.MustSegments("v1", "payment_intents", piID), piID)
		if err != nil {
			return Facts{PaymentIntent: piID}
		}
		chID, _ = m["latest_charge"].(string)
		if lc, ok := m["latest_charge"].(map[string]any); ok {
			chID, _ = lc["id"].(string)
		}
		if !strings.HasPrefix(chID, "ch_") {
			return Facts{PaymentIntent: piID}
		}
	}
	if _, err := saas.Segments(chID); err != nil || chID == "" {
		return Facts{}
	}
	m, err := s.object(ctx, saas.MustSegments("v1", "charges", chID), chID)
	if err != nil {
		return Facts{Charge: chID, PaymentIntent: piID}
	}
	c, ok := asCharge(m)
	if !ok {
		return Facts{Charge: chID, PaymentIntent: piID}
	}
	cur := strings.ToLower(c.Currency)
	f := Facts{Charge: c.ID, PaymentIntent: c.PaymentIntent, Customer: c.Customer, Currency: cur, ChargeAmount: c.Amount,
		AmountRefunded: c.AmountRefunded, Refundable: c.Amount - c.AmountRefunded, Disputed: c.Disputed,
		AmountDisplay: money.Format(a.Amount, cur), Enriched: reCurrency.MatchString(cur) && c.Amount > 0}
	if c.Created > 0 {
		f.ChargeAgeDays = int64(now().Sub(time.Unix(c.Created, 0)).Hours() / 24)
	}
	if f.Refundable < 0 {
		f.Refundable = 0
	}
	return f
}

func (s *session) subscriptionFacts(ctx context.Context, a args) Facts {
	if _, err := saas.Segments(a.Subscription); err != nil {
		return Facts{}
	}
	m, err := s.object(ctx, saas.MustSegments("v1", "subscriptions", a.Subscription), a.Subscription)
	if err != nil {
		return Facts{Subscription: a.Subscription}
	}
	b, _ := json.Marshal(m)
	var sub subscription
	if json.Unmarshal(b, &sub) != nil || sub.ID == "" {
		return Facts{Subscription: a.Subscription}
	}
	amt, cur, iv := sub.price()
	return Facts{Subscription: sub.ID, Customer: sub.Customer, Status: sub.Status, CurrentPeriodEnd: sub.periodEnd(),
		AmountPerPeriod: amt, Currency: cur, Interval: iv, AmountDisplay: money.Format(amt, cur), Enriched: true}
}

// Summarize implements connector.Summarizer: one sentence from Stripe's facts and ids, never from
// free agent text. Amounts are the loudest part.
func (c *Connector) Summarize(in connector.SummaryInput) connector.Summary {
	a, err := decodeArgs(in.Args)
	if err != nil {
		return nil
	}
	var f Facts
	if m, ok := in.Facts["stripe"].(map[string]any); ok {
		b, _ := json.Marshal(m)
		_ = json.Unmarshal(b, &f)
	}
	agent := in.Agent
	if agent == "" {
		agent = "An agent"
	}
	tool := in.Tool.Name
	if tool == "" {
		_, tool, _ = connector.Split(in.Qualified)
	}
	plain := func(t string) connector.SummaryPart { return connector.SummaryPart{Text: t} }
	strong := func(t string) connector.SummaryPart {
		return connector.SummaryPart{Text: t, Style: connector.SummaryStrong}
	}
	code := func(t string) connector.SummaryPart {
		return connector.SummaryPart{Text: t, Style: connector.SummaryCode}
	}
	s := connector.Summary{strong(agent)}
	switch tool {
	case ToolCreateRefund:
		target := a.Charge
		if target == "" {
			target = a.PaymentIntent
		}
		if !validID(target) || a.Amount <= 0 {
			return nil
		}
		if !f.Enriched {
			s = append(s, plain(" wants to refund "), strong(fmt.Sprintf("%d minor units", a.Amount)), plain(" on "), code(target),
				plain(". Deedglass couldn't read the charge, so the currency isn't known."))
			return s
		}
		s = append(s, plain(" wants to refund "), strong(money.Format(a.Amount, f.Currency)), plain(" of "),
			strong(money.Format(f.ChargeAmount, f.Currency)), plain(" on "), code(f.Charge))
		if validID(f.Customer) {
			s = append(s, plain(" for customer "), code(f.Customer))
		}
		s = append(s, plain("."))
		if f.Disputed {
			s = append(s, plain(" The charge is disputed."))
		}
		return s
	case ToolCancelSubscription:
		if !validID(a.Subscription) {
			return nil
		}
		s = append(s, plain(" wants to cancel "), code(a.Subscription))
		if f.Enriched && f.AmountPerPeriod > 0 && f.Interval != "" {
			s = append(s, plain(" ("), strong(money.Format(f.AmountPerPeriod, f.Currency)+" / "+f.Interval), plain(")"))
		}
		if a.AtPeriodEnd == nil || *a.AtPeriodEnd {
			s = append(s, plain(" at the end of the period"))
			if f.CurrentPeriodEnd > 0 {
				s = append(s, plain(", "+time.Unix(f.CurrentPeriodEnd, 0).UTC().Format("Jan 2")))
			}
			return append(s, plain("."))
		}
		return append(s, plain(" now."))
	case ToolCreateInvoiceItem:
		if !validID(a.Customer) || !reCurrency.MatchString(a.Currency) {
			return nil
		}
		return append(s, plain(" wants to add "), strong(money.Format(a.Amount, a.Currency)), plain(" to the next invoice of "), code(a.Customer), plain("."))
	case ToolUpdateCustomerMetadata:
		if !validID(a.Customer) {
			return nil
		}
		return append(s, plain(fmt.Sprintf(" wants to change %d metadata %s on ", len(a.Metadata), plural(len(a.Metadata), "key", "keys"))), code(a.Customer), plain("."))
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func validID(id string) bool {
	for _, p := range []string{patCharge, patIntent, patCustomer, patSubscription} {
		if matchID(p, id) {
			return true
		}
	}
	return false
}

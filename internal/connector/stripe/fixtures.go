package stripe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// FixtureIDs are the sandbox objects deedglass dev stripe-fixtures makes for demos (S30 §3.1).
type FixtureIDs struct {
	Customer     string            `json:"customer"`
	Charges      map[string]string `json:"charges"` // "usd_14900" → ch_…
	Intents      map[string]string `json:"payment_intents"`
	Product      string            `json:"product"`
	Price        string            `json:"price"`
	Subscription string            `json:"subscription"`
	Date         string            `json:"date"`
}

// FixturePayments are the demo payments: $149.00, $20.00 and ¥5,000.
var FixturePayments = []connector.Money{{Amount: 14900, Currency: "usd"}, {Amount: 2000, Currency: "usd"}, {Amount: 5000, Currency: "jpy"}}

// Fixtures creates the demo objects through the connector's own session, so every rail applies:
// the sandbox-key check, the livemode breaker, the pinned Stripe-Version. Each POST carries the
// fixed Idempotency-Key fixtures-<date>-<n>, so a second run the same day returns the same
// objects and creates nothing new.
func Fixtures(ctx context.Context, inst *connector.Instance, day time.Time) (FixtureIDs, error) {
	s, err := open(ctx, inst)
	if err != nil {
		return FixtureIDs{}, err
	}
	defer s.close()
	date := day.UTC().Format("20060102")
	key := func(n int) string { return fmt.Sprintf("fixtures-%s-%d", date, n) }
	out := FixtureIDs{Charges: map[string]string{}, Intents: map[string]string{}, Date: day.UTC().Format("2006-01-02")}
	var obj struct {
		ID           string `json:"id"`
		LatestCharge string `json:"latest_charge"`
	}
	if _, err := s.post(ctx, saas.MustSegments("v1", "customers"), map[string]any{"name": "Acme Demo Customer", "email": "demo@example.test",
		"payment_method": "pm_card_visa", "invoice_settings": map[string]any{"default_payment_method": "pm_card_visa"}}, key(1), &obj); err != nil {
		return out, err
	}
	out.Customer = obj.ID
	for i, p := range FixturePayments {
		obj.ID, obj.LatestCharge = "", ""
		if _, err := s.post(ctx, saas.MustSegments("v1", "payment_intents"), map[string]any{"amount": p.Amount, "currency": p.Currency,
			"customer": out.Customer, "payment_method": "pm_card_visa", "confirm": true,
			"automatic_payment_methods": map[string]any{"enabled": true, "allow_redirects": "never"}}, key(2+i), &obj); err != nil {
			return out, err
		}
		name := fmt.Sprintf("%s_%d", p.Currency, p.Amount)
		out.Intents[name], out.Charges[name] = obj.ID, obj.LatestCharge
	}
	if _, err := s.post(ctx, saas.MustSegments("v1", "products"), map[string]any{"name": "Acme Pro"}, key(5), &obj); err != nil {
		return out, err
	}
	out.Product = obj.ID
	if _, err := s.post(ctx, saas.MustSegments("v1", "prices"), map[string]any{"unit_amount": int64(2900), "currency": "usd",
		"recurring": map[string]any{"interval": "month"}, "product": out.Product}, key(6), &obj); err != nil {
		return out, err
	}
	out.Price = obj.ID
	if _, err := s.post(ctx, saas.MustSegments("v1", "subscriptions"), map[string]any{"customer": out.Customer,
		"items": []any{map[string]any{"price": out.Price}}}, key(7), &obj); err != nil {
		return out, err
	}
	out.Subscription = obj.ID
	for k, v := range out.Charges {
		if !strings.HasPrefix(v, "ch_") && !strings.HasPrefix(v, "py_") {
			return out, fmt.Errorf("stripe: the %s payment has no charge yet (%q)", k, v)
		}
	}
	return out, nil
}

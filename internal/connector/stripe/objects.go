package stripe

import "strings"

// Stripe objects as Deedglass reads them. Outputs are projections: the fields an agent needs,
// never the whole object (deeds only ever carry digests).

type list[T any] struct {
	Data    []T  `json:"data"`
	HasMore bool `json:"has_more"`
}

func (l list[T]) project(f func(T) any) any {
	out := make([]any, 0, len(l.Data))
	for _, x := range l.Data {
		out = append(out, f(x))
	}
	return map[string]any{"data": out, "has_more": l.HasMore}
}

type customer struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Email    string            `json:"email"`
	Metadata map[string]string `json:"metadata"`
	Deleted  bool              `json:"deleted"`
}

func (c customer) project() any {
	md := c.Metadata
	if md == nil {
		md = map[string]string{}
	}
	return map[string]any{"id": c.ID, "name": c.Name, "email": c.Email, "metadata": md}
}

type charge struct {
	ID             string `json:"id"`
	Amount         int64  `json:"amount"`
	AmountRefunded int64  `json:"amount_refunded"`
	Currency       string `json:"currency"`
	Customer       string `json:"customer"`
	PaymentIntent  string `json:"payment_intent"`
	Status         string `json:"status"`
	Refunded       bool   `json:"refunded"`
	Disputed       bool   `json:"disputed"`
	Created        int64  `json:"created"`
	Paid           bool   `json:"paid"`
}

func (c charge) project() any {
	return map[string]any{"id": c.ID, "amount": c.Amount, "amount_refunded": c.AmountRefunded, "currency": strings.ToLower(c.Currency),
		"customer": c.Customer, "payment_intent": c.PaymentIntent, "status": c.Status, "refunded": c.Refunded, "disputed": c.Disputed,
		"created": c.Created}
}

type paymentIntent struct {
	ID           string `json:"id"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	Customer     string `json:"customer"`
	Status       string `json:"status"`
	LatestCharge string `json:"latest_charge"`
}

func (p paymentIntent) project() any {
	return map[string]any{"id": p.ID, "amount": p.Amount, "currency": strings.ToLower(p.Currency), "customer": p.Customer,
		"status": p.Status, "latest_charge": p.LatestCharge}
}

type refund struct {
	ID            string            `json:"id"`
	Amount        int64             `json:"amount"`
	Currency      string            `json:"currency"`
	Status        string            `json:"status"`
	Charge        string            `json:"charge"`
	PaymentIntent string            `json:"payment_intent"`
	Metadata      map[string]string `json:"metadata"`
}

func (r refund) project() any {
	return map[string]any{"id": r.ID, "amount": r.Amount, "currency": strings.ToLower(r.Currency), "status": r.Status,
		"charge": r.Charge, "payment_intent": r.PaymentIntent}
}

type invoiceItem struct {
	ID          string            `json:"id"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Customer    string            `json:"customer"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

func (i invoiceItem) project() any {
	return map[string]any{"id": i.ID, "amount": i.Amount, "currency": strings.ToLower(i.Currency), "customer": i.Customer,
		"description": i.Description}
}

type subscription struct {
	ID                string `json:"id"`
	Customer          string `json:"customer"`
	Status            string `json:"status"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64  `json:"current_period_end"` // older versions; newer ones put it on the item
	Items             struct {
		Data []struct {
			CurrentPeriodEnd int64 `json:"current_period_end"`
			Quantity         int64 `json:"quantity"`
			Price            struct {
				UnitAmount int64  `json:"unit_amount"`
				Currency   string `json:"currency"`
				Recurring  struct {
					Interval string `json:"interval"`
				} `json:"recurring"`
			} `json:"price"`
		} `json:"data"`
	} `json:"items"`
}

// periodEnd is when the current period ends (Unix seconds): the item's, else the subscription's.
func (s subscription) periodEnd() int64 {
	if len(s.Items.Data) > 0 && s.Items.Data[0].CurrentPeriodEnd > 0 {
		return s.Items.Data[0].CurrentPeriodEnd
	}
	return s.CurrentPeriodEnd
}

// price is the amount per period of the first item, its currency and interval.
func (s subscription) price() (int64, string, string) {
	if len(s.Items.Data) == 0 {
		return 0, "", ""
	}
	it := s.Items.Data[0]
	q := it.Quantity
	if q < 1 {
		q = 1
	}
	return it.Price.UnitAmount * q, strings.ToLower(it.Price.Currency), it.Price.Recurring.Interval
}

func (s subscription) project() any {
	amt, cur, iv := s.price()
	return map[string]any{"id": s.ID, "customer": s.Customer, "status": s.Status, "cancel_at_period_end": s.CancelAtPeriodEnd,
		"current_period_end": s.periodEnd(), "amount_per_period": amt, "currency": cur, "interval": iv}
}

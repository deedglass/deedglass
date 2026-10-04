package stripe

import (
	"context"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
)

func fixtureExchanges() []saastest.Exchange {
	mk := func(name, path, key, body string) saastest.Exchange {
		x := ex(name, "POST", path, 200, body)
		x.RequireHeaders = map[string]string{"Idempotency-Key": key}
		return x
	}
	return []saastest.Exchange{
		mk("cust", "/v1/customers", "fixtures-20261010-1", `{"id":"`+custID+`","livemode":false}`),
		mk("pi1", "/v1/payment_intents", "fixtures-20261010-2", `{"id":"pi_1","latest_charge":"ch_1QdemoCharge1","livemode":false}`),
		mk("pi2", "/v1/payment_intents", "fixtures-20261010-3", `{"id":"pi_2","latest_charge":"ch_1QdemoCharge2","livemode":false}`),
		mk("pi3", "/v1/payment_intents", "fixtures-20261010-4", `{"id":"pi_3","latest_charge":"ch_1QdemoCharge3","livemode":false}`),
		mk("prod", "/v1/products", "fixtures-20261010-5", `{"id":"prod_1","livemode":false}`),
		mk("price", "/v1/prices", "fixtures-20261010-6", `{"id":"price_1","livemode":false}`),
		mk("sub", "/v1/subscriptions", "fixtures-20261010-7", `{"id":"`+subID+`","livemode":false}`),
	}
}

func TestFixturesIdempotent(t *testing.T) {
	// A second run sends the same keys, so Stripe answers with the same objects: nothing new.
	xs := append(fixtureExchanges(), fixtureExchanges()...)
	r := newRig(t, nil, xs...)
	day := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	a, err := Fixtures(context.Background(), r.inst(), day)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Fixtures(context.Background(), r.inst(), day)
	if err != nil {
		t.Fatal(err)
	}
	if a.Customer != b.Customer || a.Subscription != b.Subscription || a.Charges["usd_14900"] != "ch_1QdemoCharge1" || a.Charges["jpy_5000"] != "ch_1QdemoCharge3" {
		t.Fatalf("%+v vs %+v", a, b)
	}
}

package stripe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

func benchInstance(b *testing.B, body string) (*connector.Registry, store.Connector) {
	op := &opener{key: testKey}
	env := connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	srv := saastest.New(b, saastest.Exchange{Name: "x", Method: "GET", Path: "/v1/charges/" + chargeID, Status: 200, Body: json.RawMessage(body), Repeat: true},
		saastest.Exchange{Name: "y", Method: "POST", Path: "/v1/refunds", Status: 200, Body: json.RawMessage(refundBody(2000, "usd", "succeeded", actID)), Repeat: true})
	cfg, _ := New().Validate(context.Background(), env, json.RawMessage(`{"api_base_url":"`+srv.URL+`"}`))
	row := store.Connector{ID: conID, OrgID: org.String(), Type: Type, Name: "stripe", Config: cfg, ConfigHash: "sha256:bench", Status: "active"}
	return connector.New(connector.Options{Types: []connector.Connector{New()}, Loader: memLoader{row}, Opener: op, Env: env}), row
}

// BenchmarkEnrichCacheHit: a refund's enrich when the charge is in the 30-second cache.
func BenchmarkEnrichCacheHit(b *testing.B) {
	reg, _ := benchInstance(b, chargeBody(14900, 0, "usd", false))
	ex, spec, _, _ := reg.Resolve(context.Background(), org, "stripe.create_refund")
	en := ex.(connector.ArgsEnricher)
	args := json.RawMessage(`{"charge":"` + chargeID + `","amount":2000}`)
	_, _ = en.EnrichArgs(context.Background(), spec, args)
	b.ResetTimer()
	for b.Loop() {
		_, _ = en.EnrichArgs(context.Background(), spec, args)
	}
}

// BenchmarkRefundExecute: create_refund through the registry against a loopback fixture (the
// enrich read cached), so it is Deedglass's work plus one loopback round trip.
func BenchmarkRefundExecute(b *testing.B) {
	reg, _ := benchInstance(b, chargeBody(14900, 0, "usd", false))
	ex, spec, _, _ := reg.Resolve(context.Background(), org, "stripe.create_refund")
	c := connector.Call{ActionID: ids.MustParse[ids.ActionKind](actID), OrgID: org, Tool: spec, Args: json.RawMessage(`{"charge":"` + chargeID + `","amount":2000}`)}
	_, _ = ex.Execute(context.Background(), c)
	b.ResetTimer()
	for b.Loop() {
		if _, err := ex.Execute(context.Background(), c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkForm(b *testing.B) {
	p := map[string]any{"charge": chargeID, "amount": int64(2000), "metadata": map[string]any{"deedglass_action_id": actID, "ticket": "4121"}}
	for b.Loop() {
		_, _ = Form(p)
	}
}

func BenchmarkLivemodeScan(b *testing.B) {
	body := []byte(refundBody(2000, "usd", "succeeded", actID))
	for b.Loop() {
		_ = Livemode(body)
	}
}

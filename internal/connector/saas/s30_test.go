package saas_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
)

// TestRetryIf (S30): the hook decides retries for idempotent requests' answers, and a
// non-idempotent request is still sent once.
func TestRetryIf(t *testing.T) {
	x := func(name string, st int, h map[string]string) saastest.Exchange {
		return saastest.Exchange{Name: name, Method: "POST", Path: "/api/v1/refunds", Status: st, Headers: h, Body: json.RawMessage(`{}`)}
	}
	srv := saastest.New(t, x("a", 400, map[string]string{"Should": "retry"}), x("b", 200, nil), x("c", 500, map[string]string{"Should": "no"}), x("d", 400, map[string]string{"Should": "retry"}))
	c := devClient(t, srv)
	var waited time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) bool { waited = d; return true }
	c.RetryIf = func(r *saas.Response) (bool, time.Duration) { return r.Header.Get("Should") == "retry", time.Hour }
	resp, err := c.Do(context.Background(), saas.Request{Method: "POST", Path: "/v1/refunds", Idempotent: true})
	if err != nil || resp.Attempts != 2 || waited != saas.MaxRetryWait {
		t.Fatalf("%v %v %v", resp, err, waited)
	}
	if _, err := c.Do(context.Background(), saas.Request{Method: "POST", Path: "/v1/refunds", Idempotent: true}); err == nil || srv.Count("c") != 1 {
		t.Fatalf("a 500 the hook refuses was retried: %v", err)
	}
	if _, err := c.Do(context.Background(), saas.Request{Method: "POST", Path: "/v1/refunds"}); err == nil || srv.Count("d") != 1 {
		t.Fatal("a non-idempotent request was retried")
	}
}

func TestDestinationCheck(t *testing.T) {
	inst := connector.NewInstance(mustCon(), mustOrg(), "x", "stripe", nil, "", connector.Env{Egress: &egress.Policy{Env: egress.EnvProd}}, nil)
	ch, ok := saas.DestinationCheck(context.Background(), inst, "https://127.0.0.1")
	if ok || ch.Status != connector.CheckFail || ch.DocURL != saas.DestinationDoc {
		t.Errorf("loopback in prod: %+v", ch)
	}
	inst = connector.NewInstance(mustCon(), mustOrg(), "x", "stripe", nil, "", connector.Env{Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}, nil)
	if ch, ok := saas.DestinationCheck(context.Background(), inst, "http://127.0.0.1:9"); !ok || ch.Status != connector.CheckPass {
		t.Errorf("dev loopback: %+v", ch)
	}
	if ch, ok := saas.DestinationCheck(context.Background(), inst, "::not a url"); ok {
		t.Errorf("garbage: %+v", ch)
	}
	_ = http.MethodGet
}

func mustCon() ids.ConnectorID {
	return ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7d")
}
func mustOrg() ids.OrgID { return ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp") }

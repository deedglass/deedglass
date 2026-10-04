package connectorapi

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/catalogdocs"
	"github.com/deedglass/deedglass/internal/httpx"
)

// BenchmarkConnectorTypes: GET /v1/connector-types through the router (target p99 < 5 ms).
func BenchmarkConnectorTypes(b *testing.B) {
	p := principals()["admin key"]
	reg := connector.New(connector.Options{Types: catalogdocs.Types()[1:]})
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: who{p}, Keys: who{p}})
	srv.Mount(Routes(&API{Registry: reg, Log: slog.New(slog.DiscardHandler)})...)
	req := httptest.NewRequest("GET", "/v1/connector-types", nil)
	req.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	for b.Loop() {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			b.Fatal(w.Code)
		}
	}
}

package connectorapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/catalogdocs"
	"github.com/deedglass/deedglass/internal/httpx"
)

func catalogServer(t *testing.T, p httpx.Principal) *httptest.Server {
	reg := connector.New(connector.Options{Types: catalogdocs.Types()[1:]}) // fake is a built-in in dev, not a type
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: who{p}, Keys: who{p}})
	srv.Mount(Routes(&API{Registry: reg, Log: slog.New(slog.DiscardHandler)})...)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func get(t *testing.T, url string, h map[string]string) (*http.Response, []byte) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_"+strings.Repeat("B", 32))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return r, b
}

// TestCatalogRoutes (S30 §6): the catalog golden, a stable ETag and 304, one type, 404, and an
// agent key refused.
func TestCatalogRoutes(t *testing.T) {
	ps := principals()
	ts := catalogServer(t, ps["admin key"])
	r, body := get(t, ts.URL+"/v1/connector-types", nil)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "private, max-age=300" || !strings.HasPrefix(r.Header.Get("ETag"), `"sha256:`) {
		t.Fatalf("%d %v", r.StatusCode, r.Header)
	}
	golden := "testdata/connector_types.golden.json"
	if os.Getenv("DEEDGLASS_UPDATE_CATALOG") != "" {
		_ = os.MkdirAll("testdata", 0o755)
		_ = os.WriteFile(golden, body, 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil || string(want) != string(body) {
		t.Errorf("the catalog differs from %s (DEEDGLASS_UPDATE_CATALOG=1 go test ./internal/connectorapi -run TestCatalogRoutes)", golden)
	}
	var l struct {
		Data []connector.TypeInfo `json:"data"`
	}
	_ = json.Unmarshal(body, &l)
	order := []string{}
	for _, x := range l.Data {
		order = append(order, x.Title)
	}
	if strings.Join(order, ",") != "GitHub,Gmail,PostgreSQL,REST API,Remote MCP server,Slack,Stripe" {
		t.Errorf("order %v", order)
	}
	// A second server (a restart) gives the same ETag; If-None-Match answers 304.
	ts2 := catalogServer(t, ps["viewer"])
	tag := r.Header.Get("ETag")
	r2, _ := get(t, ts2.URL+"/v1/connector-types", map[string]string{"Cookie": "x"})
	_ = r2
	r3, b3 := get(t, ts.URL+"/v1/connector-types", map[string]string{"If-None-Match": tag})
	if r3.StatusCode != 304 || len(b3) != 0 {
		t.Errorf("If-None-Match: %d", r3.StatusCode)
	}
	r4, b4 := get(t, ts.URL+"/v1/connector-types/stripe", nil)
	var one connector.TypeInfo
	if r4.StatusCode != 200 || json.Unmarshal(b4, &one) != nil || one.Type != "stripe" || len(one.Tools) != 10 {
		t.Errorf("one: %d %s", r4.StatusCode, b4)
	}
	if tg := r4.Header.Get("ETag"); tg == "" || tg == tag {
		t.Errorf("type ETag %q", tg)
	}
	if r5, _ := get(t, ts.URL+"/v1/connector-types/stripe", map[string]string{"If-None-Match": r4.Header.Get("ETag")}); r5.StatusCode != 304 {
		t.Errorf("type 304: %d", r5.StatusCode)
	}
	if r6, b6 := get(t, ts.URL+"/v1/connector-types/notion", nil); r6.StatusCode != 404 || !strings.Contains(string(b6), `"not_found"`) {
		t.Errorf("unknown: %d %s", r6.StatusCode, b6)
	}
	tsAgent := catalogServer(t, ps["agent key"])
	if r7, b7 := get(t, tsAgent.URL+"/v1/connector-types/stripe", nil); r7.StatusCode != 403 || !strings.Contains(string(b7), "insufficient_scope") {
		t.Errorf("agent key: %d %s", r7.StatusCode, b7)
	}
	// Restart stability: the same registry content computes the same tag.
	tsAgain := catalogServer(t, ps["admin key"])
	if r8, _ := get(t, tsAgain.URL+"/v1/connector-types", nil); r8.Header.Get("ETag") != tag {
		t.Errorf("ETag changed across restarts")
	}
}

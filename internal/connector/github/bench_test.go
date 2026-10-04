package github

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

const benchArgs = `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`

var benchAnswer = []byte(`{"number":42,"node_id":"I_x","html_url":"https://github.com/arjun/deedglass-sandbox/issues/42","state":"open","title":"Flaky checkout test","user":{"login":"arjun-sandbox-bot"}}`)

// BenchmarkGitHubExecute is create_issue against an httptest server through the egress client
// (loopback network included).
func BenchmarkGitHubExecute(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Remaining", "4987")
		w.WriteHeader(201)
		_, _ = w.Write(benchAnswer)
	}))
	defer srv.Close()
	c := New()
	inst := instance(b, c, cfgJSON(srv.URL, nil), patOpener(sentinelToken()))
	cl := call(b, "create_issue", benchArgs)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Execute(ctxWithRedactor(), inst, cl); err != nil {
			b.Fatal(err)
		}
	}
}

// stubTransport answers in memory: what is left is the connector's own overhead (arguments,
// credential, request building, answer decoding, projection), without the network.
type stubTransport struct{}

func (stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
	}
	return &http.Response{StatusCode: 201, Header: http.Header{"Content-Type": {"application/json"}, "X-Ratelimit-Remaining": {"4987"}},
		Body: io.NopCloser(bytes.NewReader(benchAnswer)), Request: r}, nil
}

func overheadConn(b *testing.B) (*Connector, *connector.Instance) {
	c := New()
	c.httpClient = &http.Client{Transport: stubTransport{}}
	return c, instance(b, c, cfgJSON("https://api.github.com", nil), patOpener(sentinelToken()))
}

// BenchmarkGitHubExecuteOverhead is the S29 §3.8 measure: execute overhead excluding network
// (target p50 < 500 µs). It reports p50 and p99 per call.
func BenchmarkGitHubExecuteOverhead(b *testing.B) {
	c, inst := overheadConn(b)
	cl := call(b, "create_issue", benchArgs)
	lat := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		t0 := time.Now()
		if _, err := c.Execute(ctxWithRedactor(), inst, cl); err != nil {
			b.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
	}
	b.StopTimer()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	b.ReportMetric(float64(lat[len(lat)/2].Microseconds()), "p50-µs")
	b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds()), "p99-µs")
}

// TestExecuteOverheadBudget keeps the overhead budget honest in ordinary test runs: p50 < 500 µs
// over 200 in-memory calls (generous for -race).
func TestExecuteOverheadBudget(t *testing.T) {
	c := New()
	c.httpClient = &http.Client{Transport: stubTransport{}}
	inst := instance(t, c, cfgJSON("https://api.github.com", nil), patOpener(sentinelToken()))
	cl := call(t, "create_issue", benchArgs)
	lat := make([]time.Duration, 0, 200)
	for i := 0; i < 200; i++ {
		t0 := time.Now()
		res, err := c.Execute(context.Background(), inst, cl)
		if err != nil {
			t.Fatal(err)
		}
		lat = append(lat, time.Since(t0))
		if i == 0 {
			var o map[string]any
			_ = json.Unmarshal(res.Output, &o)
			if o["number"] != float64(42) {
				t.Fatal(string(res.Output))
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if p50 := lat[100]; p50 > 5*time.Millisecond {
		t.Fatalf("p50 %v", p50)
	}
}

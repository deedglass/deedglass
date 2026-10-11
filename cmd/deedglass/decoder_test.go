package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connectorapi"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/httpx"
)

// The decoder CLI against a fake server: S20 §5's outputs (TTY, NO_COLOR, --json).

func decoderViews(now time.Time) map[string]decoder.View {
	p := func(s string) *string { return &s }
	n := func(v int64) *int64 { return &v }
	f := func(v float64) *float64 { return &v }
	tl := 1
	at := now.Add(-4 * time.Second)
	since := time.Date(2026, 10, 7, 7, 40, 0, 0, time.UTC)
	base := func(id, name, status string) decoder.View {
		return decoder.View{Object: "decoder", Connector: id, Name: name, Slot: "deedglass_" + id[4:], Publication: "deedglass_pub",
			Status: status, Health: decoder.HealthWord(status), Detail: map[string]any{}, Since: since, Transport: "peek",
			Server:   decoder.ViewServer{Version: p("18.6"), SystemID: p("7690386503164422802"), Timeline: &tl},
			Watching: []string{"public.orders", "public.order_items", "public.refunds"}, Frames: map[string]int64{"bound": 1204, "unattributed": 3},
			Recent: []decoder.ViewFrame{}, UpdatedAt: since}
	}
	pg := base("con_01j9zq7c4m8x2v6t0r3n5k1h7d", "postgres", decoder.StatusRunning)
	pg.Positions = decoder.ViewPositions{CheckpointLSN: p("0/3E65050"), ConfirmedFlushLSN: p("0/3E65050"), RestartLSN: p("0/3E64F00"), CurrentWALLSN: p("0/3E66000")}
	pg.Lag = decoder.ViewLag{Bytes: n(4016), Seconds: f(0.1), RetainedBytes: n(8192)}
	pg.LastCommit = &decoder.ViewCommit{LSN: "0/3E65020", At: &at}
	pg.Frames["forged_marker"] = 1
	pg.Recent = []decoder.ViewFrame{
		{CommitLSN: p("0/3E65020"), Class: "bound", Action: p("act_01j9zt3a5c8x2v6t0r3n5k1h7d"),
			Counts: map[string]map[string]int64{"public.orders": {"insert": 0, "update": 1, "delete": 0, "truncate": 0}}, Flags: []string{}, At: since},
		{CommitLSN: p("0/3E61A88"), Class: "bound", Action: p("act_01j9zt1q8w8x2v6t0r3n5k1h7d"),
			Counts: map[string]map[string]int64{"public.orders": {"update": 301}}, Flags: []string{}, At: since},
		{CommitLSN: p("0/3E5F010"), Class: "unattributed", Counts: map[string]map[string]int64{"public.refunds": {"insert": 2}}, Flags: []string{}, At: since},
		{CommitLSN: p("0/3E5C7F8"), Class: "forged_marker", Counts: map[string]map[string]int64{}, Flags: []string{}, At: since},
	}
	billing := base("con_01j9zq7c4m8x2v6t0r3n5k1h7e", "billing", decoder.StatusConnecting)
	billing.Reason = p("unreachable")
	billing.Detail = map[string]any{"attempt": 3, "next_try_ms": 8000, "message": "dial tcp 10.0.0.5:5432: connect: connection refused"}
	analytics := base("con_01j9zq7c4m8x2v6t0r3n5k1h7f", "analytics", decoder.StatusHalted)
	analytics.Reason = p("slot_invalidated")
	analytics.Detail = map[string]any{"wal_status": "lost"}
	lagging := pg
	lagging.Status, lagging.Health, lagging.Reason = decoder.StatusDegraded, "degraded", p("lag_high")
	lagging.Lag = decoder.ViewLag{Bytes: n(611 << 20), Seconds: f(42), RetainedBytes: n(611 << 20)}
	lagging.Detail = map[string]any{"alert_bytes": 512 << 20}
	lagging.Recent = []decoder.ViewFrame{}
	return map[string]decoder.View{"postgres": pg, "billing": billing, "analytics": analytics, "lagging": lagging}
}

func fakeDecoderAPI(t *testing.T, degraded bool) *httptest.Server {
	t.Helper()
	views := decoderViews(time.Now())
	if degraded {
		views["postgres"] = views["lagging"]
	}
	conns := []connectorapi.Connector{
		{Object: "connector", ID: views["postgres"].Connector, Type: "postgres", Name: "postgres"},
		{Object: "connector", ID: "con_01j9zq7c4m8x2v6t0r3n5k1h70", Type: "rest", Name: "orders"},
		{Object: "connector", ID: views["billing"].Connector, Type: "postgres", Name: "billing"},
		{Object: "connector", ID: views["analytics"].Connector, Type: "postgres", Name: "analytics"},
		{Object: "connector", ID: "con_01j9zq7c4m8x2v6t0r3n5k1h71", Type: "postgres", Name: "scratch"},
	}
	byRef := map[string]decoder.View{}
	for _, v := range []decoder.View{views["postgres"], views["billing"], views["analytics"]} {
		byRef[v.Connector], byRef[v.Name] = v, v
	}
	h := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/connectors":
			_ = enc.Encode(httpx.NewList(conns, false, nil))
		case strings.HasSuffix(r.URL.Path, "/decoder"):
			ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/connectors/"), "/decoder")
			v, ok := byRef[ref]
			if !ok {
				w.WriteHeader(409)
				io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"witness_not_configured","message":"Checks aren't on for this connector, so it has no decoder. Turn on witness.enabled in its config first.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/witness_not_configured"}}`)
				return
			}
			if r.Method == "DELETE" {
				if !strings.Contains(string(body), `"confirm":"postgres"`) {
					t.Errorf("drop body %s", body)
				}
				_ = enc.Encode(connectorapi.DropResult{Object: "decoder", Connector: v.Connector, Name: v.Name, Slot: v.Slot, Dropped: true,
					Deed: "deed_01j9zv8k6m8x2v6t0r3n5k1h7d", Status: "dropped"})
				return
			}
			if r.URL.Query().Get("recent") == "0" {
				v.Recent = []decoder.ViewFrame{}
			}
			_ = enc.Encode(v)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"Not found.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/not_found"}}`)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(s.Close)
	return s
}

func TestDecoderCommandGoldens(t *testing.T) {
	api := fakeDecoderAPI(t, false)
	slow := fakeDecoderAPI(t, true)
	env := func(srv *httptest.Server, color bool) map[string]string {
		m := map[string]string{"DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": "adm"}
		if !color {
			m["NO_COLOR"] = "1"
		}
		return m
	}
	cases := []struct {
		name  string
		env   map[string]string
		tty   bool
		stdin string
		args  []string
		code  int
	}{
		{"decoder/status", env(api, false), false, "", []string{"decoder", "status"}, 1},
		{"decoder/status_color", env(api, true), true, "", []string{"decoder", "status"}, 1},
		{"decoder/status_detail", env(api, false), false, "", []string{"decoder", "status", "--connector", "postgres", "--recent", "4"}, 0},
		{"decoder/status_detail_color", env(api, true), true, "", []string{"decoder", "status", "--connector", "postgres", "--recent", "4"}, 0},
		{"decoder/status_degraded", env(slow, false), false, "", []string{"decoder", "status", "--connector", "postgres"}, 0},
		{"decoder/status_halted", env(api, false), false, "", []string{"decoder", "status", "--connector", "analytics"}, 1},
		{"decoder/status_not_witnessed", env(api, false), false, "", []string{"decoder", "status", "--connector", "scratch"}, 1},
		{"decoder/drop", env(api, false), false, "", []string{"decoder", "drop", "--connector", "postgres", "--confirm", "postgres"}, 0},
		{"decoder/drop_typed", env(api, false), true, "postgres\n", []string{"decoder", "drop", "--connector", "postgres"}, 0},
		{"decoder/drop_mistyped", env(api, false), true, "postgre\n", []string{"decoder", "drop", "--connector", "postgres"}, 1},
		{"decoder/drop_not_a_tty", env(api, false), false, "", []string{"decoder", "drop", "--connector", "postgres"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := cliIn(t, []byte(c.stdin), c.env, c.tty, c.args...)
			if r.code != c.code {
				t.Fatalf("exit %d, want %d\n%s%s", r.code, c.code, r.stdout, r.stderr)
			}
			golden(t, c.name, r)
		})
	}
	// --json is the decoder object (and a list without --connector).
	r := cli(t, env(api, false), false, "decoder", "status", "--connector", "postgres", "--recent", "4", "--json")
	var v decoder.View
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil || v.Object != "decoder" || len(v.Recent) != 4 || v.Health != "healthy" {
		t.Fatalf("%v %s", err, r.stdout)
	}
	r = cli(t, env(api, false), false, "decoder", "status", "--json")
	var l httpx.List[decoder.View]
	if err := json.Unmarshal([]byte(r.stdout), &l); err != nil || len(l.Data) != 3 || r.code != 1 {
		t.Fatalf("%v %d %s", err, r.code, r.stdout)
	}
}

func TestDecoderWordsCopy(t *testing.T) {
	now := time.Now()
	views := decoderViews(now)
	reasons := []string{decoder.ReasonSlotInvalidated, decoder.ReasonSlotMissing, decoder.ReasonPublicationMissing, decoder.ReasonPublicationInvalid,
		decoder.ReasonMalformedEvidence, decoder.ReasonSessionSettingsChanged, decoder.ReasonLedgerFunctionChanged, decoder.ReasonSystemChanged,
		decoder.ReasonWALLevel, decoder.ReasonNotReplication, decoder.ReasonSlotConflict}
	for _, reason := range reasons {
		v := views["analytics"]
		rr := reason
		v.Reason = &rr
		w := decoderWords(v, now)
		if w.kind != "fail" || (w.explain == "" && w.next == "") {
			t.Errorf("%s: %+v", reason, w)
		}
		for _, s := range []string{w.headline, w.explain, w.next} {
			if strings.Contains(s, "!") || strings.Contains(s, "—") {
				t.Errorf("%s: %q breaks the copy rules", reason, s)
			}
		}
	}
	for _, reason := range []string{decoder.ReasonLagHigh, decoder.ReasonRestartRetained, decoder.ReasonStalled, decoder.ReasonPublicationChanged,
		decoder.ReasonTimelineChanged, decoder.ReasonPeekSlow} {
		v := views["lagging"]
		rr := reason
		v.Reason = &rr
		if w := decoderWords(v, now); w.kind != "unknown" || w.explain == "" {
			t.Errorf("%s: %+v", reason, w)
		}
	}
}

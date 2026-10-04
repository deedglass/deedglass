package learn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// S28 §6 "Fallbacks": the Messages API client against httptest servers, then PhraseRows end to
// end over memStore.

// script answers each request with the next step; the last step repeats.
type script struct {
	mu    sync.Mutex
	steps []func(w http.ResponseWriter, r *http.Request)
	n     int
	reqs  []*http.Request
	body  [][]byte
}

func (s *script) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	i := min(s.n, len(s.steps)-1)
	s.n++
	s.reqs = append(s.reqs, r.Clone(context.Background()))
	s.body = append(s.body, b)
	step := s.steps[i]
	s.mu.Unlock()
	step(w, r)
}

func (s *script) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

func status(code int, header ...string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"x","message":"x"}}`))
	}
}

func reply(stop, title, rationale string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { writeClaude(w, stop, title, rationale) }
}

func raw(body string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func phraserFor(t *testing.T, s *script) (*ClaudePhraser, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	var mu sync.Mutex
	slept := &[]time.Duration{}
	return &ClaudePhraser{APIKey: "dg-test-key", HTTP: srv.Client(), BaseURL: srv.URL, Timeout: 2 * time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			*slept = append(*slept, d)
			mu.Unlock()
			return nil
		}}, slept
}

func TestClaudeRequestShape(t *testing.T) {
	t.Parallel()
	s := &script{steps: []func(http.ResponseWriter, *http.Request){reply("end_turn", "T", "R.")}}
	c, _ := phraserFor(t, s)
	res, err := c.Phrase(bg, valInput())
	if err != nil || res.Title != "T" || res.Rationale != "R." || res.InputTokens != 900 || res.OutputTokens != 120 || res.Model != DefaultPhrasingModel {
		t.Fatalf("%v %+v", err, res)
	}
	r := s.reqs[0]
	if r.Method != "POST" || r.URL.Path != "/v1/messages" {
		t.Fatalf("%s %s", r.Method, r.URL.Path)
	}
	if r.Header.Get("x-api-key") != "dg-test-key" || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("content-type") != "application/json" {
		t.Fatalf("headers %v", r.Header)
	}
	var body map[string]any
	if err := json.Unmarshal(s.body[0], &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != DefaultPhrasingModel || body["max_tokens"] != 1024.0 || body["system"] != PhrasingSystemPrompt {
		t.Fatalf("body %v", body)
	}
	for _, k := range []string{"temperature", "top_p", "top_k", "stream", "tools"} {
		if _, ok := body[k]; ok {
			t.Errorf("body sets %s", k)
		}
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages %v (no assistant prefill)", msgs)
	}
	oc := body["output_config"].(map[string]any)
	f := oc["format"].(map[string]any)
	sch := f["schema"].(map[string]any)
	if oc["effort"] != "low" || f["type"] != "json_schema" || sch["additionalProperties"] != false {
		t.Fatalf("output_config %v", oc)
	}
	props := sch["properties"].(map[string]any)
	if props["title"].(map[string]any)["maxLength"] != 90.0 || props["rationale"].(map[string]any)["maxLength"] != 400.0 {
		t.Fatalf("schema %v", sch)
	}
	checkBody(t, s.body[0])
	// The model override, and String never prints the key.
	c.ModelID = "claude-haiku-4-5"
	if c.Model() != "claude-haiku-4-5" || strings.Contains(c.String(), "dg-test-key") || !strings.Contains(c.String(), "claude-haiku-4-5") {
		t.Fatal(c.String())
	}
}

func TestClaudeFallbacks(t *testing.T) {
	t.Parallel()
	ok := reply("end_turn", "T", "R.")
	type step = func(http.ResponseWriter, *http.Request)
	cases := []struct {
		name     string
		steps    []step
		reason   string // "" = success
		requests int
		slept    []time.Duration
		tokens   bool
	}{
		{"429 then 200", []step{status(429, "retry-after", "2"), ok}, "", 2, []time.Duration{2 * time.Second}, false},
		{"429 without retry-after", []step{status(429), ok}, "", 2, []time.Duration{time.Second}, false},
		{"429 retry-after over 10 s", []step{status(429, "retry-after", "30")}, "http_429", 1, nil, false},
		{"429 twice", []step{status(429, "retry-after", "0")}, "http_429", 2, []time.Duration{0}, false},
		{"529 twice", []step{status(529)}, "http_529", 2, []time.Duration{time.Second}, false},
		{"500 then 200", []step{status(500), ok}, "", 2, []time.Duration{time.Second}, false},
		{"503 twice", []step{status(503)}, "http_503", 2, []time.Duration{time.Second}, false},
		{"400", []step{status(400)}, "http_400", 1, nil, false},
		{"401", []step{status(401)}, "http_401", 1, nil, false},
		{"refusal", []step{reply("refusal", "", "")}, "refusal", 1, nil, true},
		{"max_tokens", []step{reply("max_tokens", "T", "R.")}, "max_tokens", 1, nil, true},
		{"not the schema", []step{raw(`{"stop_reason":"end_turn","content":[{"type":"text","text":"{\"title\":\"T\",\"extra\":1}"}],"usage":{"input_tokens":900,"output_tokens":120}}`)}, "validation:schema", 1, nil, true},
		{"not JSON text", []step{raw(`{"stop_reason":"end_turn","content":[{"type":"text","text":"Sure, here it is"}],"usage":{"input_tokens":900,"output_tokens":120}}`)}, "validation:schema", 1, nil, true},
		{"unreadable body", []step{raw(`not json`)}, "unreadable", 1, nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := &script{steps: c.steps}
			p, slept := phraserFor(t, s)
			res, err := p.Phrase(bg, valInput())
			if s.count() != c.requests {
				t.Errorf("%d requests, want %d", s.count(), c.requests)
			}
			if len(*slept) != len(c.slept) || (len(c.slept) > 0 && (*slept)[0] != c.slept[0]) {
				t.Errorf("slept %v, want %v", *slept, c.slept)
			}
			if c.reason == "" {
				if err != nil || res.Title != "T" {
					t.Fatalf("%v %+v", err, res)
				}
				return
			}
			var pe *PhraseError
			if !errors.As(err, &pe) || pe.Reason != c.reason {
				t.Fatalf("err %v, want reason %s", err, c.reason)
			}
			if c.tokens != (pe.Result != nil && pe.Result.InputTokens == 900 && pe.Result.OutputTokens == 120) {
				t.Errorf("tokens counted: %+v", pe.Result)
			}
		})
	}
}

func TestClaudeTimeoutAndNetwork(t *testing.T) {
	t.Parallel()
	slow := &script{steps: []func(http.ResponseWriter, *http.Request){func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
		writeClaude(w, "end_turn", "T", "R.")
	}}}
	p, _ := phraserFor(t, slow)
	p.Timeout = 50 * time.Millisecond
	_, err := p.Phrase(bg, valInput())
	var pe *PhraseError
	if !errors.As(err, &pe) || pe.Reason != "timeout" || slow.count() != 1 {
		t.Fatalf("timeout: %v (%d requests)", err, slow.count())
	}
	// A closed server: network.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err = (&ClaudePhraser{APIKey: "k", HTTP: &http.Client{}, BaseURL: url}).Phrase(bg, valInput())
	if !errors.As(err, &pe) || pe.Reason != "network" {
		t.Fatalf("network: %v", err)
	}
	// The wait before the retry is cut short: timeout.
	s := &script{steps: []func(http.ResponseWriter, *http.Request){status(529)}}
	p2, _ := phraserFor(t, s)
	p2.Sleep = func(context.Context, time.Duration) error { return context.DeadlineExceeded }
	if _, err := p2.Phrase(bg, valInput()); !errors.As(err, &pe) || pe.Reason != "timeout" || s.count() != 1 {
		t.Fatalf("sleep cut: %v", err)
	}
	// The default sleep honours the context.
	s3 := &script{steps: []func(http.ResponseWriter, *http.Request){status(429, "retry-after", "5")}}
	p3, _ := phraserFor(t, s3)
	p3.Sleep = nil
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	t0 := time.Now()
	if _, err := p3.Phrase(ctx, valInput()); !errors.As(err, &pe) || pe.Reason != "timeout" || time.Since(t0) > time.Second {
		t.Fatalf("default sleep: %v after %v", err, time.Since(t0))
	}
	// A bad base URL never sends.
	if _, err := (&ClaudePhraser{APIKey: "k", HTTP: &http.Client{}, BaseURL: "://bad"}).Phrase(bg, valInput()); err == nil {
		t.Fatal("bad base URL")
	}
}

func TestCostMicros(t *testing.T) {
	t.Parallel()
	cases := []struct {
		model   string
		in, out int
		want    int64
	}{
		{"claude-sonnet-5", 900, 120, 3000}, // $0.003
		{"claude-sonnet-5", 0, 0, 0},
		{"claude-sonnet-5", 1, 0, 2},
		{"claude-haiku-4-5", 1000, 100, 1500},
		{"claude-opus-5", 1000, 100, 7500},
		{"claude-opus-5-5", 1000, 100, 6000},
		{"unknown-model", 900, 120, 7500}, // priced at the most expensive row
	}
	for _, c := range cases {
		if got := CostMicros(c.model, c.in, c.out); got != c.want {
			t.Errorf("CostMicros(%s, %d, %d) = %d, want %d", c.model, c.in, c.out, got, c.want)
		}
	}
	if _, ok := Prices[DefaultPhrasingModel]; !ok || PricesCheckedOn == "" {
		t.Fatal("the default model has no price")
	}
	// Five dollars covers well over a thousand typical calls.
	if n := 5_000_000 / CostMicros(DefaultPhrasingModel, 900, 120); n < 1000 {
		t.Fatalf("$5 covers %d calls", n)
	}
}

func TestTemplatePhraser(t *testing.T) {
	t.Parallel()
	in := valInput()
	r, err := TemplatePhraser{}.Phrase(bg, in)
	if err != nil || r.Title != in.TemplateTitle || r.Rationale != in.TemplateRationale || (TemplatePhraser{}).Model() != "template" {
		t.Fatal(r, err)
	}
	if (&PhraseError{Reason: "refusal"}).Error() != "learn: phrasing: refusal" {
		t.Fatal("PhraseError")
	}
}

// --- PhraseRows end to end -------------------------------------------------------------------------

// rewording answers with the template text reworded (same placeholders and numbers).
func rewording(hits *atomic.Int32, title func(string) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var in PhrasingInput
		_ = json.Unmarshal([]byte(req.Messages[0].Content), &in)
		writeClaude(w, "end_turn", title(in.TemplateTitle), in.TemplateRationale)
	}
}

func phrasingEnv(t *testing.T, handler http.Handler, llm bool) (*sugEnv, *ClaudePhraser) {
	t.Helper()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	e.m.mu.Lock()
	e.m.orgs[e.org].info.Settings.LLMPhrasing = llm
	e.m.mu.Unlock()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := &ClaudePhraser{APIKey: "dg-test-key", HTTP: srv.Client(), BaseURL: srv.URL, Sleep: func(context.Context, time.Duration) error { return nil }}
	e.sg.Phraser = c
	return e, c
}

func TestPhraseRowsClaude(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	e, c := phrasingEnv(t, rewording(&hits, func(s string) string { return strings.Replace(s, "Allow", "Let", 1) }), true)
	run := e.generate()
	e.sg.WaitPhrasing()
	if hits.Load() != 1 || len(run.Created) != 1 {
		t.Fatalf("%d requests, %d created", hits.Load(), len(run.Created))
	}
	got, _ := e.m.GetSuggestion(bg, e.org, run.Created[0].ID)
	p := got.Phrasing
	if p.Source != "claude" || p.Model != c.Model() || p.InputTokens != 900 || p.OutputTokens != 120 || p.CostUSDMicros != CostMicros(c.Model(), 900, 120) || p.At == nil {
		t.Fatalf("phrasing %+v", p)
	}
	if got.Title != "Let updates to public.orders without approval" || p.TemplateTitle != "Allow updates to public.orders without approval" {
		t.Fatalf("title %q (template %q)", got.Title, p.TemplateTitle)
	}
	if !strings.Contains(got.Rationale, "by ops-agent in the last 14 days") || strings.Contains(got.Rationale, "{") {
		t.Fatalf("rationale not resolved: %q", got.Rationale)
	}
}

func TestPhraseRowsLimits(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	h := rewording(&hits, func(s string) string { return s })
	rowsOf := func(e *sugEnv) []NewSuggestion {
		f := newEvFixture(t, simSource(t, 1))
		f.rv = e.rv
		f.addOrderApprovals(401, 3, 0)
		ps, _ := GenLoosenFn(f.evidence())
		r := NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindLoosen, Title: ps[0].Title(), PhrasingInput: BuildPhrasingInput(&ps[0], 14),
			Fingerprint: "sha256:x", PolicyID: e.rv.PolicyID, BaseVersionID: e.rv.VersionID}
		e.m.putSuggestion(e.org, r, SugOpen)
		return []NewSuggestion{r}
	}
	set := DefaultSettings()
	set.LLMPhrasing = true

	// The monthly cap reached: no request.
	e, _ := phrasingEnv(t, h, true)
	e.m.orgs[e.org].costMicros = int64(set.LLMMonthlyCapCents) * 10_000
	e.sg.PhraseRows(bg, e.org, set, rowsOf(e))
	// 20 calls today: no request.
	e2, _ := phrasingEnv(t, h, true)
	e2.m.orgs[e2.org].callsToday = PhrasingPerDay
	e2.sg.PhraseRows(bg, e2.org, set, rowsOf(e2))
	// The usage can't be read: no request.
	e3, _ := phrasingEnv(t, h, true)
	e3.m.errUsage = errors.New("db down")
	e3.sg.PhraseRows(bg, e3.org, set, rowsOf(e3))
	if hits.Load() != 0 || len(e.m.phrasings)+len(e2.m.phrasings)+len(e3.m.phrasings) != 0 {
		t.Fatalf("%d requests past the limits", hits.Load())
	}
	// One cent under the cap: one request.
	e4, _ := phrasingEnv(t, h, true)
	e4.m.orgs[e4.org].costMicros = int64(set.LLMMonthlyCapCents)*10_000 - 1
	e4.sg.PhraseRows(bg, e4.org, set, rowsOf(e4))
	if hits.Load() != 1 || len(e4.m.phrasings) != 1 || e4.m.phrasings[0].P.Source != "claude" {
		t.Fatalf("under the cap: %d requests %+v", hits.Load(), e4.m.phrasings)
	}
}

func TestPhraseLaterOff(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	h := rewording(&hits, func(s string) string { return s })
	// llm_phrasing off.
	e, _ := phrasingEnv(t, h, false)
	e.generate()
	e.sg.WaitPhrasing()
	// Phraser nil.
	e2, _ := phrasingEnv(t, h, true)
	e2.sg.Phraser = nil
	e2.generate()
	e2.sg.WaitPhrasing()
	// The template phraser.
	e3, _ := phrasingEnv(t, h, true)
	e3.sg.Phraser = TemplatePhraser{}
	e3.generate()
	e3.sg.WaitPhrasing()
	if hits.Load() != 0 || len(e.m.phrasings)+len(e2.m.phrasings)+len(e3.m.phrasings) != 0 {
		t.Fatalf("%d requests with phrasing off", hits.Load())
	}
	// Nothing new inserted: nothing to phrase.
	e.sg.phraseLater(e.org, OrgInfo{Settings: Settings{LLMPhrasing: true}}, []NewSuggestion{{ID: "sug_x"}}, map[string]bool{})
	e.sg.WaitPhrasing()
}

// failingPhraser returns a plain error.
type failingPhraser struct{}

func (failingPhraser) Phrase(context.Context, *PhrasingInput) (*PhraseResult, error) {
	return nil, errors.New("boom")
}
func (failingPhraser) Model() string { return "claude-sonnet-5" }

func TestPhraseRowsTemplateStands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		handler http.Handler
		phraser Phraser
		reason  string
		tokens  bool
	}{
		{"a number not in the input", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeClaude(w, "end_turn", "Allow 402 updates to {table_1}", "People approved 401 of 404 updates to {table_1} by {agent_1}.")
		}), nil, ValNumberNotInInput, true},
		{"refusal", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeClaude(w, "refusal", "", "") }), nil, "refusal", true},
		{"529 twice", status529(), nil, "http_529", false},
		{"a plain error", nil, failingPhraser{}, "error", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := c.handler
			if h == nil {
				h = http.NotFoundHandler()
			}
			e, _ := phrasingEnv(t, h, true)
			if c.phraser != nil {
				e.sg.Phraser = c.phraser
			}
			run := e.generate()
			e.sg.WaitPhrasing()
			if len(run.Created) != 1 {
				t.Fatalf("setup: %+v", run)
			}
			got, _ := e.m.GetSuggestion(bg, e.org, run.Created[0].ID)
			p := got.Phrasing
			if p.Source != "template" || p.Reason != c.reason || got.Title != "Allow updates to public.orders without approval" || p.TemplateTitle != "" {
				t.Fatalf("%+v title %q", p, got.Title)
			}
			if c.tokens != (p.InputTokens == 900 && p.CostUSDMicros > 0) {
				t.Errorf("tokens %+v", p)
			}
			if len(e.m.phrasings) != 1 || e.m.phrasings[0].Title != nil {
				t.Errorf("stored %+v", e.m.phrasings)
			}
		})
	}
}

func status529() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(529) })
}

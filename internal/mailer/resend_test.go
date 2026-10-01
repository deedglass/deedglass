package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "re_test_9f8e7d6c5b4a"

// reply is one scripted response from the fake Resend.
type reply struct {
	status int
	body   string
	header map[string]string
	hang   bool // wait for the client to give up
}

// recorded is one request the fake Resend saw.
type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

// fakeResend is an httptest server that answers with a script (the last reply repeats).
type fakeResend struct {
	*httptest.Server
	mu     sync.Mutex
	script []reply
	reqs   []recorded
}

func newFake(t *testing.T, script ...reply) *fakeResend {
	t.Helper()
	f := &fakeResend{script: script}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		n := len(f.reqs)
		f.reqs = append(f.reqs, recorded{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		rp := f.script[min(n, len(f.script)-1)]
		f.mu.Unlock()
		if rp.hang {
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		}
		for k, v := range rp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rp.status)
		_, _ = io.WriteString(w, rp.body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeResend) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

// sleeps records the stubbed waits.
type sleeps struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = append(s.d, d)
	return nil
}

func (s *sleeps) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.d...)
}

func newTestResend(t *testing.T, url string, s *sleeps, log *slog.Logger, client *http.Client) *Resend {
	t.Helper()
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r, err := NewResend(ResendOptions{
		APIKey: testKey, From: "Deedglass <signin@deedglass.com>", BaseURL: url + "/",
		Client: client, Logger: log, Sleep: s.sleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const okBody = `{"id":"49a3999c-0ce1-4ea6-ab68-afcd6dc2e794"}`

func TestResendSuccess(t *testing.T) {
	f := newFake(t, reply{status: 200, body: okBody})
	var s sleeps
	r := newTestResend(t, f.URL, &s, nil, nil)
	m := testMessage()
	m.Tags = map[string]string{"org": "org_01j9", "kind": "spoof", "bad name": "a.b@c", "": "skip", "empty": ""}
	if err := r.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m2 := testMessage()
	m2.IdempotencyKey = ""
	if err := r.Send(context.Background(), m2); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 2 || len(s.got()) != 0 {
		t.Fatalf("%d requests, sleeps %v", len(reqs), s.got())
	}
	q := reqs[0]
	for _, c := range []struct{ name, got, want string }{
		{"method", q.method, "POST"},
		{"path", q.path, "/emails"},
		{"authorization", q.header.Get("Authorization"), "Bearer " + testKey},
		{"content type", q.header.Get("Content-Type"), "application/json"},
		{"idempotency key", q.header.Get("Idempotency-Key"), "login/ltk_01j9zr3m5k8q2w7x4t6v0b1c9d"},
		{"user agent", q.header.Get("User-Agent"), "deedglass-mailer"},
		{"no idempotency key", reqs[1].header.Get("Idempotency-Key"), ""},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	var shape map[string]json.RawMessage
	if err := json.Unmarshal(q.body, &shape); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range shape {
		keys = append(keys, k)
	}
	if strings.Join(sorted(keys), ",") != "from,headers,html,subject,tags,text,to" {
		t.Errorf("body keys %v", keys)
	}
	var body resendEmail
	if err := json.Unmarshal(q.body, &body); err != nil {
		t.Fatal(err)
	}
	if body.From != "Deedglass <signin@deedglass.com>" || len(body.To) != 1 || body.To[0] != m.To ||
		body.Subject != m.Subject || body.HTML != m.HTML || body.Text != m.Text {
		t.Errorf("body %+v", body)
	}
	wantTags := []resendTag{{"kind", "login"}, {"bad_name", "a_b_c"}, {"org", "org_01j9"}}
	if len(body.Tags) != len(wantTags) {
		t.Fatalf("tags %v, want %v", body.Tags, wantTags)
	}
	for i := range wantTags {
		if body.Tags[i] != wantTags[i] {
			t.Errorf("tag %d = %v, want %v", i, body.Tags[i], wantTags[i])
		}
	}
	var body2 resendEmail
	_ = json.Unmarshal(reqs[1].body, &body2)
	ref1, ref2 := body.Headers["X-Entity-Ref-ID"], body2.Headers["X-Entity-Ref-ID"]
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	if !hex32.MatchString(ref1) || !hex32.MatchString(ref2) || ref1 == ref2 || len(body.Headers) != 1 {
		t.Errorf("X-Entity-Ref-ID %q %q (headers %v)", ref1, ref2, body.Headers)
	}
	if strings.Contains(string(q.body), "usr_") || strings.Contains(string(q.body), "req_") {
		t.Error("UserID or RequestID was sent")
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestResendRetries(t *testing.T) {
	ms := time.Millisecond
	for _, c := range []struct {
		name         string
		script       []reply
		wantErr      error // nil for success
		anyErr       bool
		wantAttempts int
		wantSleeps   []time.Duration
		errHas       []string
	}{
		{name: "429 then 200", script: []reply{{status: 429, header: map[string]string{"retry-after": "2"}}, {status: 200, body: okBody}},
			wantAttempts: 2, wantSleeps: []time.Duration{2 * time.Second}},
		{name: "429 garbage retry-after", script: []reply{{status: 429, header: map[string]string{"Retry-After": "soon"}}, {status: 200, body: okBody}},
			wantAttempts: 2, wantSleeps: []time.Duration{time.Second}},
		{name: "429 missing retry-after", script: []reply{{status: 429}, {status: 200, body: okBody}},
			wantAttempts: 2, wantSleeps: []time.Duration{time.Second}},
		{name: "429 capped", script: []reply{{status: 429, header: map[string]string{"Retry-After": "3600"}}, {status: 200, body: okBody}},
			wantAttempts: 2, wantSleeps: []time.Duration{10 * time.Second}},
		{name: "500 twice then 200", script: []reply{{status: 500}, {status: 502}, {status: 200, body: okBody}},
			wantAttempts: 3, wantSleeps: []time.Duration{500 * ms, time.Second}},
		{name: "500 three times", script: []reply{{status: 500, body: `{"name":"internal_server_error"}`}},
			anyErr: true, wantAttempts: 3, wantSleeps: []time.Duration{500 * ms, time.Second}, errHas: []string{"3 attempts", "status 500"}},
		{name: "429 three times", script: []reply{{status: 429, header: map[string]string{"Retry-After": "1"}}},
			anyErr: true, wantAttempts: 3, wantSleeps: []time.Duration{time.Second, time.Second}, errHas: []string{"status 429"}},
		{name: "422 rejected", script: []reply{{status: 422, body: `{"statusCode":422,"name":"validation_error","message":"Invalid to: priya@acme.com"}`}},
			wantErr: ErrRejected, wantAttempts: 1, errHas: []string{"status 422", "validation_error"}},
		{name: "401 rejected without a name", script: []reply{{status: 401, body: `not json`}},
			wantErr: ErrRejected, wantAttempts: 1, errHas: []string{"status 401"}},
		{name: "500 then 403", script: []reply{{status: 500}, {status: 403, body: `{"name":"invalid <api> key"}`}},
			wantErr: ErrRejected, wantAttempts: 2, wantSleeps: []time.Duration{500 * ms}, errHas: []string{"status 403", "invalid__api__key"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, c.script...)
			var s sleeps
			r := newTestResend(t, f.URL, &s, nil, nil)
			err := r.Send(context.Background(), testMessage())
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case c.anyErr:
				if err == nil || errors.Is(err, ErrRejected) {
					t.Fatalf("err = %v, want a non-rejected error", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if err != nil {
				for _, h := range c.errHas {
					if !strings.Contains(err.Error(), h) {
						t.Errorf("error %q is missing %q", err, h)
					}
				}
				for _, bad := range []string{testKey, "priya@acme.com", "Invalid to"} {
					if strings.Contains(err.Error(), bad) {
						t.Errorf("error %q carries %q", err, bad)
					}
				}
			}
			if n := len(f.requests()); n != c.wantAttempts {
				t.Errorf("%d attempts, want %d", n, c.wantAttempts)
			}
			if got := s.got(); !equalDurations(got, c.wantSleeps) {
				t.Errorf("sleeps %v, want %v", got, c.wantSleeps)
			}
			for _, q := range f.requests() {
				if q.header.Get("Idempotency-Key") != testMessage().IdempotencyKey {
					t.Error("a retry lost the Idempotency-Key")
				}
			}
		})
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestResendTimeout(t *testing.T) {
	f := newFake(t, reply{hang: true})
	var s sleeps
	r := newTestResend(t, f.URL, &s, nil, &http.Client{Timeout: 50 * time.Millisecond})
	err := r.Send(context.Background(), testMessage())
	if err == nil || errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "3 attempts") || strings.Contains(err.Error(), testKey) {
		t.Errorf("error %q", err)
	}
	if n := len(f.requests()); n != 3 {
		t.Errorf("%d attempts, want 3", n)
	}
	if got := s.got(); !equalDurations(got, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Errorf("sleeps %v", got)
	}
}

func TestResendCancel(t *testing.T) {
	// A cancelled sleep stops the retries.
	f := newFake(t, reply{status: 503})
	r := newTestResend(t, f.URL, &sleeps{}, nil, nil)
	r.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := r.Send(context.Background(), testMessage()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.requests()); n != 1 {
		t.Errorf("%d attempts, want 1", n)
	}

	// A cancelled context stops a request in flight without retrying.
	h := newFake(t, reply{hang: true})
	r = newTestResend(t, h.URL, &sleeps{}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Send(ctx, testMessage()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if n := len(h.requests()); n != 1 {
		t.Errorf("%d attempts, want 1", n)
	}

	// An invalid message never reaches Resend.
	m := testMessage()
	m.Kind = ""
	if err := r.Send(context.Background(), m); err == nil {
		t.Fatal("sent a message with no kind")
	}
	if n := len(h.requests()); n != 1 {
		t.Errorf("an invalid message made a request")
	}
}

func TestResendLogs(t *testing.T) {
	login, err := LoginMessage(fixtureLogin())
	if err != nil {
		t.Fatal(err)
	}
	login.IdempotencyKey = "login/ltk_01j9zr3m5k8q2w7x4t6v0b1c9d"
	login.UserID = "usr_01j9zr3m5k8q2w7x4t6v0b1c9d"
	login.RequestID = "req_01j9zr3m5k8q2w7x4t6v0b1c9d"

	var buf lockedBuffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ok := newFake(t, reply{status: 200, body: okBody})
	if err := newTestResend(t, ok.URL, &sleeps{}, log, nil).Send(context.Background(), login); err != nil {
		t.Fatal(err)
	}
	bad := newFake(t, reply{status: 422, body: `{"name":"validation_error","message":"Invalid to: priya@acme.com"}`})
	if err := newTestResend(t, bad.URL, &sleeps{}, log, nil).Send(context.Background(), login); !errors.Is(err, ErrRejected) {
		t.Fatal(err)
	}
	down := newFake(t, reply{status: 500})
	if err := newTestResend(t, down.URL, &sleeps{}, log, nil).Send(context.Background(), login); err == nil {
		t.Fatal("500 succeeded")
	}
	out := buf.String()

	for _, canary := range []string{testKey, "priya@acme.com", "Sign in to Deedglass", "Sign in to Acme", fixtureLink,
		"OEVkbr-q18qimgaDkbcE5mnZqQ2a1zGG7_TgYq8PKFc", "482913", "482 913", "ltk_01j9", "Invalid to"} {
		if strings.Contains(out, canary) {
			t.Errorf("logs carry %q:\n%s", canary, out)
		}
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d log lines, want 3:\n%s", len(lines), out)
	}
	for i, want := range []map[string]any{
		{"level": "INFO", "msg": "mail.sent", "status": 200.0, "resend_id": "49a3999c-0ce1-4ea6-ab68-afcd6dc2e794", "attempts": 1.0},
		{"level": "WARN", "msg": "mail.failed", "status": 422.0, "attempts": 1.0, "reason": "rejected", "resend_error": "validation_error"},
		{"level": "WARN", "msg": "mail.failed", "status": 500.0, "attempts": 3.0, "reason": "unavailable"},
	} {
		var got map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatal(err)
		}
		want["kind"], want["user_id"], want["request_id"] = "login", login.UserID, login.RequestID
		for k, v := range want {
			if got[k] != v {
				t.Errorf("line %d: %s = %v, want %v", i, k, got[k], v)
			}
		}
	}
}

func TestNewResend(t *testing.T) {
	for _, c := range []struct {
		name string
		o    ResendOptions
		ok   bool
	}{
		{"ok", ResendOptions{APIKey: testKey, From: "Deedglass <signin@deedglass.com>"}, true},
		{"no key", ResendOptions{From: "Deedglass <signin@deedglass.com>"}, false},
		{"blank key", ResendOptions{APIKey: "  ", From: "x"}, false},
		{"no from", ResendOptions{APIKey: testKey}, false},
		{"bad base", ResendOptions{APIKey: testKey, From: "x", BaseURL: "ftp://example.com"}, false},
		{"no host", ResendOptions{APIKey: testKey, From: "x", BaseURL: "https://"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, err := NewResend(c.o)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), testKey) {
				t.Errorf("error carries the key: %v", err)
			}
			if c.ok && (r.endpoint != "https://api.resend.com/emails" || r.client.Timeout != 10*time.Second || r.log == nil || r.sleep == nil) {
				t.Errorf("defaults: %+v", r)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
	}{
		{"2", 2 * time.Second}, {" 3 ", 3 * time.Second}, {"0", 0}, {"10", 10 * time.Second}, {"11", 10 * time.Second},
		{"99999999999999999999", time.Second}, {"9999999999", 10 * time.Second}, {"", time.Second}, {"-1", time.Second},
		{"1.5", time.Second}, {"Wed, 21 Oct 2026 07:28:00 GMT", time.Second},
	} {
		if got := retryAfter(c.in); got != c.want {
			t.Errorf("retryAfter(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSanitizeTag(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"login", 256, "login"}, {"org_01-AZ", 256, "org_01-AZ"}, {"a b.c@d", 256, "a_b_c_d"},
		{"é", 256, "__"}, {"abcdef", 3, "abc"}, {"", 256, ""}, {strings.Repeat("x", 300), 256, strings.Repeat("x", 256)},
	} {
		if got := sanitizeTag(c.in, c.max); got != c.want {
			t.Errorf("sanitizeTag(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("sleepCtx on a cancelled context: %v", err)
	}
}

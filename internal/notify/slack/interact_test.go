package slack

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	slackc "github.com/deedglass/deedglass/internal/connector/slack"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/store"
)

type rig struct {
	t    *testing.T
	st   *fakeStore
	api  *fakeAPI
	dec  *fakeDecider
	h    *Handler
	srv  *httptest.Server
	now  time.Time
	logs *bytes.Buffer
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, st: newFakeStore(), api: newFakeAPI(), dec: &fakeDecider{}, now: t0, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(r.logs, nil))
	r.h = &Handler{Store: r.st, Conns: fakeConns{}, API: r.api, Approvals: r.dec, Log: log, Now: func() time.Time { return r.now },
		Async: func(f func()) { f() }}
	api := httpx.New(httpx.Options{Logger: log})
	api.Mount(r.h.Routes()...)
	r.srv = httptest.NewServer(api)
	t.Cleanup(r.srv.Close)
	return r
}

// payloadJSON builds a block_actions payload.
func clickPayload(team, app, user, action, value string) string {
	b, _ := json.Marshal(map[string]any{"type": "block_actions", "team": map[string]any{"id": team}, "api_app_id": app,
		"user": map[string]any{"id": user, "team_id": team}, "trigger_id": "123.456.abc", "response_url": "https://hooks.slack.com/actions/T/1/x",
		"actions": []map[string]any{{"action_id": action, "value": value}}})
	return string(b)
}

func denyPayload(team, app, user, value, reason string) string {
	vals := map[string]any{"reason": map[string]any{"reason": map[string]any{"type": "plain_text_input", "value": reason}}}
	b, _ := json.Marshal(map[string]any{"type": "view_submission", "team": map[string]any{"id": team}, "api_app_id": app,
		"user": map[string]any{"id": user, "team_id": team},
		"view": map[string]any{"callback_id": "deedglass_deny", "private_metadata": value, "state": map[string]any{"values": vals}}})
	return string(b)
}

// send posts a signed form body; secret "" signs nothing; tsOff shifts the timestamp.
func (r *rig) send(payload, secret string, tsOff time.Duration, mutate func(h http.Header)) (int, string) {
	r.t.Helper()
	body := "payload=" + url.QueryEscape(payload)
	ts := strconv.FormatInt(r.now.Add(tsOff).Unix(), 10)
	req, _ := http.NewRequest("POST", r.srv.URL+"/v1/slack/interact", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	if secret != "" {
		req.Header.Set("X-Slack-Signature", slackc.Sign([]byte(secret), ts, []byte(body)))
	}
	if mutate != nil {
		mutate(req.Header)
	}
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

var value = aprID + "|" + ctxDg

func TestInteractApprove(t *testing.T) {
	r := newRig(t)
	start := time.Now()
	code, body := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil)
	if code != 200 || body != "" || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("code %d body %q", code, body)
	}
	if r.dec.count() != 1 {
		t.Fatalf("decide called %d times", r.dec.count())
	}
	in := r.dec.calls[0]
	if in.Org != orgA || in.ApprovalID != aprID || in.ContextDigest != ctxDg || in.Vote != approve.VoteApprove ||
		in.Approver.UserID != usrA || in.Approver.Via != approve.ViaSlack || in.Approver.External != "T0AAAAAAA/U0PRIYA" {
		t.Fatalf("decide input %+v", in)
	}
	// The same request again is a replay: 200, no second decision.
	code, _ = r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil)
	if code != 200 || r.dec.count() != 1 {
		t.Fatalf("replay: code %d decide %d", code, r.dec.count())
	}
	if len(r.api.said()) != 0 {
		t.Fatalf("ephemerals %v", r.api.said())
	}
}

func TestInteractRefusals(t *testing.T) {
	ok := clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value)
	cases := []struct {
		name    string
		payload string
		secret  string
		off     time.Duration
		mutate  func(h http.Header)
		want    int
	}{
		{"bad signature", ok, secretB, 0, nil, 401},
		{"unsigned", ok, "", 0, nil, 401},
		{"timestamp 301 s old", ok, secretA, -301 * time.Second, nil, 401},
		{"timestamp 301 s ahead", ok, secretA, 301 * time.Second, nil, 401},
		{"non-hex signature", ok, secretA, 0, func(h http.Header) { h.Set("X-Slack-Signature", "v0="+strings.Repeat("z", 64)) }, 401},
		{"non-integer timestamp", ok, secretA, 0, func(h http.Header) { h.Set("X-Slack-Request-Timestamp", "1.5") }, 401},
		{"org B's team signed with org A's secret", clickPayload("T0BBBBBBB", "A0BBBBBBB", "U0PRIYA", ActionApprove, value), secretA, 0, nil, 401},
		{"unknown team", clickPayload("T0CCCCCCC", "A0CCCCCCC", "U0PRIYA", ActionApprove, value), secretA, 0, nil, 401},
		{"not JSON", "{", secretA, 0, nil, 401},
		{"no payload", "", secretA, 0, nil, 401},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			code, body := r.send(c.payload, c.secret, c.off, c.mutate)
			if code != c.want || body != "" {
				t.Fatalf("code %d body %q", code, body)
			}
			if r.dec.count() != 0 {
				t.Fatal("Decide ran for a refused request")
			}
		})
	}
	// The wrong content type and an oversized body are bare 401s too (the route's pipeline).
	r := newRig(t)
	req, _ := http.NewRequest("POST", r.srv.URL+"/v1/slack/interact", strings.NewReader(`{"payload":"{}"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 401 || len(b) != 0 {
		t.Fatalf("json body: %d %q", resp.StatusCode, b)
	}
	big := "payload=" + strings.Repeat("a", MaxBody)
	req, _ = http.NewRequest("POST", r.srv.URL+"/v1/slack/interact", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = r.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("oversized: %d", resp.StatusCode)
	}
	// The refusal reason is logged; the payload and the signature never are.
	if strings.Contains(r.logs.String(), "U0PRIYA") {
		t.Fatal("the log holds payload values")
	}
}

func TestInteractRouteError(t *testing.T) {
	r := newRig(t)
	r.st.routeErr = io.ErrUnexpectedEOF
	if code, _ := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil); code != 401 {
		t.Fatalf("code %d", code)
	}
}

func TestInteractPeople(t *testing.T) {
	cases := []struct {
		user    string
		domains []string
		want    string
	}{
		{"U0UNKNOWN", nil, MsgUnmapped},
		{"U0UNCONF", nil, MsgUnmapped},
		{"U0GUEST", nil, MsgUnmapped},
		{"U0ULTRA", nil, MsgUnmapped},
		{"U0BOT", nil, MsgUnmapped},
		{"U0GONE", nil, MsgUnmapped},
		{"U0OTHER", nil, MsgUnmapped},
		{"U0NOMAIL", nil, MsgUnmapped},
		{"U0WRONGTEAM", nil, MsgUnmapped},
		{"U0PRIYA", []string{"example.org"}, MsgUnmapped},
	}
	for _, c := range cases {
		t.Run(c.user, func(t *testing.T) {
			r := newRig(t)
			r.api.domains = c.domains
			code, _ := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", c.user, ActionApprove, value), secretA, 0, nil)
			if code != 200 || r.dec.count() != 0 {
				t.Fatalf("code %d decide %d", code, r.dec.count())
			}
			if s := r.api.said(); len(s) != 1 || s[0] != c.want {
				t.Fatalf("said %q", s)
			}
		})
	}
	// An allowed domain passes.
	r := newRig(t)
	r.api.domains = []string{"ACME.com"}
	r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil)
	if r.dec.count() != 1 {
		t.Fatal("an allowed domain was refused")
	}
}

func TestInteractSlowLookup(t *testing.T) {
	r := newRig(t)
	r.api.slow = 2 * time.Second
	start := time.Now()
	code, _ := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil)
	if took := time.Since(start); code != 200 || took > 2500*time.Millisecond {
		t.Fatalf("code %d in %v", code, took)
	}
	if r.dec.count() != 0 {
		t.Fatal("decided without knowing who clicked")
	}
	if s := r.api.said(); len(s) != 1 || s[0] != MsgSlow {
		t.Fatalf("said %q", s)
	}
}

func TestInteractDecideRefusals(t *testing.T) {
	approved := cardRow("approved", []store.CardVote{{UserID: "usr_x", Name: "Arjun", Vote: "approve", At: t0}})
	denied := cardRow("denied", []store.CardVote{{UserID: "usr_x", Name: "Arjun", Vote: "deny", At: t0}})
	expired := cardRow("expired", nil)
	cancelled := cardRow("cancelled", nil)
	cases := []struct {
		name string
		err  error
		row  *store.ApprovalCardRow
		want string
	}{
		{"self", httpx.E(httpx.CodeSelfApproval), nil, MsgSelf},
		{"repeat", httpx.E(httpx.CodeAlreadyCountersigned), nil, MsgRepeat},
		{"not eligible", httpx.E(httpx.CodeApproverNotEligible), nil, `You can see this request, but policy "pull-requests-to-main-need-approval" needs an admin to approve it.`},
		{"decided approve", httpx.E(httpx.CodeApprovalDecided), approved, "Arjun already approved this at <!date^"},
		{"decided deny", httpx.E(httpx.CodeApprovalDecided), denied, "Arjun already denied this at <!date^"},
		{"decided expired", httpx.E(httpx.CodeApprovalDecided), expired, "This request expired at <!date^"},
		{"decided cancelled", httpx.E(httpx.CodeApprovalDecided), cancelled, "The agent cancelled this request. Nothing ran."},
		{"expired", httpx.E(httpx.CodeApprovalExpired), nil, "This request expired at <!date^"},
		{"changed", httpx.E(httpx.CodeContextChanged), nil, MsgChanged},
		{"gone", httpx.E(httpx.CodeNotFound), nil, MsgGone},
		{"other code", httpx.E(httpx.CodeConflict), nil, MsgFailed},
		{"untyped", io.ErrUnexpectedEOF, nil, MsgFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.dec.err = c.err
			if c.row != nil {
				r.st.card = c.row
			}
			code, _ := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil)
			if code != 200 || r.dec.count() != 1 {
				t.Fatalf("code %d decide %d", code, r.dec.count())
			}
			if s := r.api.said(); len(s) != 1 || !strings.HasPrefix(s[0], c.want) {
				t.Fatalf("said %q, want prefix %q", s, c.want)
			}
		})
	}
	// Without the row the sentences still read.
	r := newRig(t)
	r.st.cardErr = store.ErrNotFound
	r.dec.err = httpx.E(httpx.CodeApprovalDecided)
	r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil)
	if s := r.api.said(); len(s) != 1 || !strings.HasPrefix(s[0], "Someone already decided this at an earlier time.") {
		t.Fatalf("said %q", s)
	}
}

func TestInteractDeny(t *testing.T) {
	r := newRig(t)
	// The click opens the reason form; nothing is decided yet.
	code, _ := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionDeny, value), secretA, 0, nil)
	if code != 200 || r.dec.count() != 0 || len(r.api.views) != 1 {
		t.Fatalf("code %d decide %d views %d", code, r.dec.count(), len(r.api.views))
	}
	var view map[string]any
	_ = json.Unmarshal(r.api.views[0], &view)
	if view["callback_id"] != CallbackDeny || view["private_metadata"] != value {
		t.Fatalf("view %v", view)
	}
	// An empty reason keeps the form open with the field error.
	code, body := r.send(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", value, "   "), secretA, 0, nil)
	if code != 200 || !strings.Contains(body, `"response_action":"errors"`) || !strings.Contains(body, MsgReasonEmpty) || r.dec.count() != 0 {
		t.Fatalf("code %d body %s", code, body)
	}
	code, body = r.send(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", value, strings.Repeat("x", 281)), secretA, 0, nil)
	if !strings.Contains(body, MsgReasonLong) || r.dec.count() != 0 {
		t.Fatalf("code %d body %s", code, body)
	}
	// A reason denies.
	code, body = r.send(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", value, "Not during the release freeze."), secretA, 0, nil)
	if code != 200 || body != "" || r.dec.count() != 1 {
		t.Fatalf("code %d body %q decide %d", code, body, r.dec.count())
	}
	in := r.dec.calls[0]
	if in.Vote != approve.VoteDeny || in.Reason != "Not during the release freeze." || in.ContextDigest != ctxDg {
		t.Fatalf("%+v", in)
	}
	// A refused deny shows its sentence on the form; an unmapped person too.
	r.dec.err = httpx.E(httpx.CodeApproverNotEligible)
	_, body = r.send(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", value, "No."), secretA, 0, nil)
	if !strings.Contains(body, "needs an admin") {
		t.Fatalf("body %s", body)
	}
	_, body = r.send(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0GUEST", value, "No."), secretA, 0, nil)
	if !strings.Contains(body, "match your Slack account") {
		t.Fatalf("body %s", body)
	}
	// Other modals and malformed metadata are ignored.
	code, body = r.send(strings.Replace(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", value, "x"), "deedglass_deny", "other", 1), secretA, 0, nil)
	if code != 200 || body != "" {
		t.Fatalf("%d %q", code, body)
	}
	code, _ = r.send(denyPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", "apr_x|nope", "x"), secretA, 0, nil)
	if code != 200 {
		t.Fatalf("%d", code)
	}
	// A failed views.open is answered in Slack.
	r2 := newRig(t)
	r2.api.viewErr = io.ErrUnexpectedEOF
	r2.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionDeny, value), secretA, 0, nil)
	if s := r2.api.said(); len(s) != 1 || s[0] != MsgFailed {
		t.Fatalf("said %q", s)
	}
}

func TestInteractIgnored(t *testing.T) {
	r := newRig(t)
	for _, p := range []string{
		clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionOpen, ""),
		clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", "something_else", value),
		clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, "apr_x|sha256:short"),
		strings.Replace(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), "block_actions", "shortcut", 1),
	} {
		r.now = r.now.Add(time.Second) // a new signature each time
		if code, body := r.send(p, secretA, 0, nil); code != 200 || body != "" {
			t.Fatalf("%d %q", code, body)
		}
	}
	if r.dec.count() != 0 || len(r.api.said()) != 0 {
		t.Fatalf("decide %d said %v", r.dec.count(), r.api.said())
	}
}

func TestInteractRateLimit(t *testing.T) {
	r := newRig(t)
	got429 := false
	for i := 0; i < PerIPPerMinute+5; i++ {
		code, _ := r.send("{", secretA, 0, nil)
		if code == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("never limited")
	}
}

func TestBareGuard(t *testing.T) {
	g := bareGuard{}
	if !g.Admit(nil, nil) || g.Challenge(nil, "x") != "x" {
		t.Fatal("guard")
	}
	for _, c := range []struct{ in, want int }{{400, 401}, {413, 401}, {415, 401}, {429, 429}, {503, 503}} {
		w := httptest.NewRecorder()
		e := httpx.E(httpx.CodeConflict)
		e.Status = c.in
		if c.in == 429 {
			e.Header = http.Header{"Retry-After": {"7"}}
		}
		g.RenderError(w, nil, e, "req_1")
		if w.Code != c.want || w.Body.Len() != 0 {
			t.Fatalf("%d → %d %q", c.in, w.Code, w.Body.String())
		}
		if c.in == 429 && w.Header().Get("Retry-After") != "7" {
			t.Fatal("Retry-After dropped")
		}
	}
}

func TestSplitValue(t *testing.T) {
	if a, d, ok := splitValue(value); !ok || a != aprID || d != ctxDg {
		t.Fatal(a, d, ok)
	}
	for _, bad := range []string{"", aprID, "act_x|" + ctxDg, aprID + "|sha256:abc", aprID + "|" + ctxDg + "0"} {
		if _, _, ok := splitValue(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

// TestInteractLatency is S29 §3.8's handler budget on fixtures: p99 under 800 ms with users.info
// warm, and still under the 2.5 s hard budget with a 300 ms users.info (cold). It logs the numbers
// make bench-slack records.
func TestInteractLatency(t *testing.T) {
	for _, c := range []struct {
		name string
		slow time.Duration
		p99  time.Duration
	}{{"warm", 0, 800 * time.Millisecond}, {"cold", 300 * time.Millisecond, 2500 * time.Millisecond}} {
		r := newRig(t)
		r.h.limiter = nil
		r.api.slow = c.slow
		n := 200
		if c.slow > 0 {
			n = 20
		}
		var lat []time.Duration
		for i := 0; i < n; i++ {
			r.now = r.now.Add(time.Millisecond * 1100) // a fresh signature each time
			start := time.Now()
			if code, _ := r.send(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value), secretA, 0, nil); code != 200 {
				t.Fatalf("%s: %d", c.name, code)
			}
			lat = append(lat, time.Since(start))
		}
		slicesSort(lat)
		p50, p99 := lat[len(lat)/2], lat[(len(lat)*99)/100-1]
		t.Logf("handler %s: n=%d p50=%v p99=%v", c.name, n, p50, p99)
		if p99 > c.p99 {
			t.Fatalf("%s p99 %v over %v", c.name, p99, c.p99)
		}
	}
}

func slicesSort(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}

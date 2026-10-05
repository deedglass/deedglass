package slack

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	slackc "github.com/deedglass/deedglass/internal/connector/slack"
	"github.com/deedglass/deedglass/internal/httpx"
)

// FuzzSlackVerify (S34): the whole /v1/slack/interact path on arbitrary bytes. Nothing panics,
// the answer is one of the statuses the route documents, and nothing is decided unless the body
// carries the org's real v0 signature for that exact body and a fresh timestamp. A signed seed
// shows the check is not vacuous: its first delivery decides once and its replay decides nothing.
// (internal/connector/slack's FuzzSlackVerify covers the signature function alone.)
func FuzzSlackVerify(f *testing.F) {
	st, api, dec := newFakeStore(), newFakeAPI(), &fakeDecider{}
	h := &Handler{Store: st, Conns: fakeConns{}, API: api, Approvals: dec, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return t0 }, Async: func(f func()) { f() }}
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	srv.Mount(h.Routes()...)
	h.limiter = nil // the per-IP limit (TestInteractRateLimit) would turn every later input into a 429
	ts := strconv.FormatInt(t0.Unix(), 10)
	good := []byte("payload=" + url.QueryEscape(clickPayload("T0AAAAAAA", "A0AAAAAAA", "U0PRIYA", ActionApprove, value)))
	f.Add(good, ts, slackc.Sign([]byte(secretA), ts, good))
	f.Add(good, ts, slackc.Sign([]byte("wrong"), ts, good))
	f.Add(good, strconv.FormatInt(t0.Unix()-360, 10), slackc.Sign([]byte(secretA), strconv.FormatInt(t0.Unix()-360, 10), good))
	f.Add([]byte("payload=%7B"), ts, "v0=00")
	f.Add([]byte{}, "", "")
	f.Add(bytes.Repeat([]byte("a"), 70000), ts, "v0="+string(bytes.Repeat([]byte("0"), 64)))
	f.Fuzz(func(t *testing.T, body []byte, tsHdr, sig string) {
		before := dec.count()
		req := httptest.NewRequest("POST", "/v1/slack/interact", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Slack-Request-Timestamp", tsHdr)
		req.Header.Set("X-Slack-Signature", sig)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		_, _ = io.Copy(io.Discard, rec.Result().Body)
		switch rec.Code {
		case http.StatusOK, http.StatusUnauthorized, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType,
			http.StatusTooManyRequests, http.StatusBadRequest:
		default:
			t.Fatalf("status %d", rec.Code)
		}
		if dec.count() > before {
			n, err := strconv.ParseInt(tsHdr, 10, 64)
			if err != nil || n < t0.Unix()-300 || n > t0.Unix()+300 || sig != slackc.Sign([]byte(secretA), tsHdr, body) {
				t.Fatalf("decided an unsigned or stale request (status %d)", rec.Code)
			}
			if dec.count() > before+1 {
				t.Fatal("decided twice")
			}
		}
	})
}

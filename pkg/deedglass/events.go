package deedglass

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
)

// The live event stream (GET /v1/events, S24): an EventSource-like client. It resumes with
// Last-Event-ID after a dropped connection, reconnects with backoff (1, 2, 4, 8, 16, 30 s, ±20%),
// and hands every message to the caller, the control messages included: hello says whether a
// resume replayed what was missed, resync says it couldn't (refetch your lists), session_ended
// ends the stream.

// StreamEvent is one message of the stream.
type StreamEvent struct {
	ID   string          // the id: line, the stream cursor (<instance>.<seq>); "" when absent
	Type string          // hello, resync, session_ended, or an event type (action.created, …)
	Data json.RawMessage // the data: line
}

// Event is the event object an event message carries: the webhook envelope plus hydrated.
type Event struct {
	Object     string `json:"object"` // "event"
	ID         string `json:"id"`     // evt_…
	Type       string `json:"type"`
	APIVersion string `json:"api_version"`
	CreatedAt  string `json:"created_at"`
	Livemode   bool   `json:"livemode"`
	Org        string `json:"org"`
	Hydrated   bool   `json:"hydrated"`
	Data       struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

// StreamHello is the hello message.
type StreamHello struct {
	Instance   string `json:"instance"`
	Resumed    bool   `json:"resumed"`
	HeartbeatS int    `json:"heartbeat_s"`
	ServerTime string `json:"server_time"`
}

// StreamResync is the resync message.
type StreamResync struct {
	Reason string `json:"reason"` // cursor_expired, other_instance, lagged, bus_reconnected
}

// SessionEndedError ends a stream whose credential stopped being valid.
type SessionEndedError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *SessionEndedError) Error() string {
	return "deedglass: stream ended: " + e.Code + ": " + e.Message
}

// DefaultBackoff is the reconnect schedule; each wait is jittered by ±20%.
var DefaultBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// EventStream follows GET /v1/events. Set Client, optionally the filters and a cursor, then Run.
type EventStream struct {
	Client      *Client
	Types       []string // ?types=: event types or prefixes (action.*)
	Agent       string   // ?agent=
	LastEventID string   // resume from here; Run keeps it current

	// OnReconnect is called before each reconnect wait, with the wait and the error that ended the
	// connection.
	OnReconnect func(wait time.Duration, cause error)
	// IdleTimeout drops a connection that sends nothing (not even a ping) for this long (45 s).
	IdleTimeout time.Duration
	// Seams for tests.
	Backoff []time.Duration
	Jitter  func() float64 // in [0, 1); default from crypto/rand
	Sleep   func(ctx context.Context, d time.Duration) error
}

// Run streams until ctx ends (nil), the stream refuses for good (an *Error: 4xx other than 429, a
// *SessionEndedError), or fn returns an error (that error). fn receives every message in order.
func (s *EventStream) Run(ctx context.Context, fn func(StreamEvent) error) error {
	backoff := s.Backoff
	if len(backoff) == 0 {
		backoff = DefaultBackoff
	}
	jitter := s.Jitter
	if jitter == nil {
		jitter = cryptoFloat
	}
	sleep := s.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	attempt := 0
	for {
		live, err := s.once(ctx, fn)
		if ctx.Err() != nil {
			return nil
		}
		var se *SessionEndedError
		var ae *Error
		var fe *fnError
		switch {
		case errors.As(err, &fe):
			return fe.err
		case errors.As(err, &se):
			return se
		case errors.As(err, &ae) && ae.Status >= 400 && ae.Status < 500 && ae.Status != http.StatusTooManyRequests:
			return ae
		}
		if live {
			attempt = 0 // the connection was up: start the schedule over
		}
		wait := Backoff(backoff, attempt, jitter())
		attempt++
		if s.OnReconnect != nil {
			s.OnReconnect(wait, err)
		}
		if err := sleep(ctx, wait); err != nil {
			return nil
		}
	}
}

// Backoff is the attempt'th wait of schedule (the last one repeats), jittered by ±20% (j in [0, 1)).
func Backoff(schedule []time.Duration, attempt int, j float64) time.Duration {
	d := schedule[min(attempt, len(schedule)-1)]
	return time.Duration(float64(d) * (0.8 + 0.4*j))
}

// cryptoFloat is a float in [0, 1) (G5: no math/rand, even for jitter).
func cryptoFloat() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type fnError struct{ err error }

func (e *fnError) Error() string { return e.err.Error() }

// once runs one connection. live reports whether it got as far as the hello.
func (s *EventStream) once(ctx context.Context, fn func(StreamEvent) error) (live bool, err error) {
	c := s.Client
	if c == nil || c.BaseURL == "" {
		return false, &Error{Status: 0, Code: "client_invalid", Message: "The client has no server URL."}
	}
	base, err := url.Parse(strings.TrimSuffix(c.BaseURL, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return false, &Error{Code: "client_invalid", Message: "The server URL must be http(s)://host[:port]."}
	}
	q := url.Values{}
	if len(s.Types) > 0 {
		q.Set("types", strings.Join(s.Types, ","))
	}
	if s.Agent != "" {
		q.Set("agent", s.Agent)
	}
	target := base.String() + "/v1/events"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, target, nil)
	if err != nil {
		return false, err
	}
	h := req.Header
	h.Set("Accept", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Deedglass-Version", APIVersion)
	ua := c.UserAgent
	if ua == "" {
		ua = "deedglass-go"
	}
	h.Set("User-Agent", ua)
	if c.APIKey != "" {
		h.Set("Authorization", "Bearer "+c.APIKey)
	}
	if s.LastEventID != "" {
		h.Set("Last-Event-ID", s.LastEventID)
	}
	hc := c.HTTP
	if hc == nil {
		hc = egress.FirstParty(egress.FirstPartyOptions{Proxy: true}) // no overall timeout: the stream is long
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, redactKey(err, c.APIKey)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var env struct {
			Error *Error `json:"error"`
		}
		e := &Error{}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			e = env.Error
		}
		e.Status, e.RetryAfter = resp.StatusCode, resp.Header.Get("Retry-After")
		if e.RequestID == "" {
			e.RequestID = resp.Header.Get("Deedglass-Request-Id")
		}
		return false, e
	}
	idle := s.IdleTimeout
	if idle <= 0 {
		idle = 45 * time.Second
	}
	watchdog := time.AfterFunc(idle, cancel)
	defer watchdog.Stop()
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	var ev StreamEvent
	var data strings.Builder
	hasData := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || cctx.Err() != nil {
				return live, errors.New("the stream closed")
			}
			return live, err
		}
		watchdog.Reset(idle)
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // dispatch
			if hasData || ev.Type != "" {
				ev.Data = json.RawMessage(data.String())
				if ev.Type == "" {
					ev.Type = "message"
				}
				if ev.ID != "" {
					s.LastEventID = ev.ID
				}
				switch ev.Type {
				case "hello":
					live = true
				case "session_ended":
					se := &SessionEndedError{}
					_ = json.Unmarshal(ev.Data, se)
					if err := fn(ev); err != nil {
						return live, &fnError{err}
					}
					return live, se
				}
				if err := fn(ev); err != nil {
					return live, &fnError{err}
				}
			}
			ev, hasData = StreamEvent{}, false
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // a comment (: ping)
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			ev.ID = value
		case "event":
			ev.Type = value
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		}
	}
}

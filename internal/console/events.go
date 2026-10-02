package console

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// GET /v1/events (S24 §3.11): Server-Sent Events of what Postgres committed, for one org.
//
//	retry: 3000
//
//	id: 7f3k2m.1841
//	event: hello
//	data: {"object":"stream","instance":"7f3k2m","resumed":false,"heartbeat_s":15,"server_time":"…"}
//
//	id: 7f3k2m.1842
//	event: action.witnessed
//	data: {"object":"event","id":"evt_…","type":"action.witnessed",…,"hydrated":true,"data":{"object":{…}}}
//
//	: ping
//
// The id: line is the stream cursor (<instance>.<seq>), not the evt_ id. hello and resync carry the
// stream's position too, so a client that reconnects after a quiet spell resumes from there.

// EventsOptions wire the route.
type EventsOptions struct {
	Hub *Hub
	// Recheck authenticates the request again with its route's rules (httpx.Server.Recheck); a
	// refusal ends the stream with session_ended. Nil: never re-checked (tests only).
	Recheck func(*http.Request) error
	Now     func() time.Time
	// observe, when set, sees each event's enqueue time once it is written (the bench).
	observe func(queued time.Time)
}

// EventRoutes is GET /v1/events. A nil Hub mounts the route (for the route table) answering 503.
func EventRoutes(o EventsOptions) []httpx.Route {
	if o.Now == nil {
		o.Now = time.Now
	}
	return []httpx.Route{{Method: "GET", Pattern: "/v1/events", OperationID: "streamEvents", Auth: httpx.SessionOrKey,
		MinRole: httpx.RoleViewer, Scopes: []string{httpx.ScopeActionsRead}, Streaming: true,
		Purpose: "reading live events", Handler: o.serve}}
}

// The stream's copy (rule 17: errors from catalogs; these are the stream's own).
const (
	MsgTooManyForUser = "You have %d live connections open. Close a Console tab and try again."
	MsgTooManyForOrg  = "This org has too many live connections open on this server. Close a Console tab and try again."
	MsgUnknownParam   = "Unknown parameter %s. Use types, agent or last_event_id."
	MsgBadTypes       = "types is a comma list of event types or prefixes like action.*, such as types=action.*,deed.sealed."
	MsgBadAgent       = "agent must be an agent id (agt_…)."
	MsgAgentKeyAgent  = "An agent key sees only its own agent's events. Leave agent out, or use that agent's id."
	MsgBadCursor      = "last_event_id must be a cursor from this stream's id: lines, like 7f3k2m.1842."
	MsgStreamDown     = "Live events aren't available on this server right now. Try again in a few seconds."
)

// eventPrefixes are the families a types= wildcard may name (contract §8).
var eventPrefixes = map[string]bool{"action": true, "deed": true, "approval": true, "agent": true, "org": true,
	"policy": true, "decoder": true, "anomaly": true, "suggestion": true}

var typeRE = regexp.MustCompile(`^[a-z]{2,16}\.(\*|[a-z_]{2,32})$`)

// knownType reports whether t is an event type the stream can carry (webhook.test never is).
func knownType(t string) bool {
	if t == ledger.EventWebhookTest {
		return false
	}
	for _, k := range ledger.WebhookEvents {
		if k == t {
			return true
		}
	}
	return false
}

// parseTypes validates ?types=.
func parseTypes(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 32 {
		return nil, httpx.ErrValidation("types", MsgBadTypes)
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if !typeRE.MatchString(p) {
			return nil, httpx.ErrValidation("types", MsgBadTypes)
		}
		fam, rest, _ := strings.Cut(p, ".")
		if (rest == "*" && !eventPrefixes[fam]) || (rest != "*" && !knownType(p)) {
			return nil, httpx.ErrValidation("types", MsgBadTypes)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// request is a validated GET /v1/events.
type request struct {
	f      Filter
	cursor string
}

func parseRequest(r *http.Request, p httpx.Principal) (request, error) {
	q := r.URL.Query()
	for k, vs := range q {
		switch k {
		case "types", "agent", "last_event_id":
			if len(vs) > 1 {
				return request{}, httpx.ErrValidation(k, "Send "+k+" once.")
			}
		default:
			return request{}, httpx.ErrValidation(clipParam(k), fmt.Sprintf(MsgUnknownParam, strconv.Quote(clipParam(k))))
		}
	}
	types, err := parseTypes(q.Get("types"))
	if err != nil {
		return request{}, err
	}
	f := Filter{Org: p.OrgID.String(), Types: types}
	if a := q.Get("agent"); a != "" {
		if ids.Check(ids.Agent, a) != nil {
			return request{}, httpx.ErrValidation("agent", MsgBadAgent)
		}
		f.Agent = a
	}
	switch p.Kind {
	case httpx.KindAgentKey:
		own := p.AgentID.String()
		if f.Agent != "" && f.Agent != own {
			return request{}, httpx.ErrValidation("agent", MsgAgentKeyAgent)
		}
		f.Agent, f.AgentKey, f.Owner = own, true, p.KeyID.String()
	case httpx.KindAdminKey:
		f.Owner = p.KeyID.String()
	default:
		f.Owner = p.UserID.String()
	}
	cursor := r.Header.Get("Last-Event-ID") // the header wins over the query
	param := "Last-Event-ID"
	if cursor == "" {
		cursor, param = q.Get("last_event_id"), "last_event_id"
	}
	if cursor != "" {
		if _, _, ok := ParseCursor(cursor); !ok {
			return request{}, httpx.ErrValidation(param, MsgBadCursor)
		}
	}
	return request{f: f, cursor: cursor}, nil
}

func clipParam(s string) string {
	if len(s) > 40 {
		return s[:40]
	}
	return s
}

// hello is the first event of every stream.
type hello struct {
	Object     string `json:"object"` // "stream"
	Instance   string `json:"instance"`
	Resumed    bool   `json:"resumed"`
	HeartbeatS int    `json:"heartbeat_s"`
	ServerTime string `json:"server_time"`
}

// SessionEnded is the last event of a stream whose credential stopped being valid.
type SessionEnded struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// recheckStrikes is how many re-checks in a row may fail for a reason that isn't a refusal (the
// database didn't answer) before the stream closes and the client re-authenticates on reconnect.
const recheckStrikes = 3

// WriteTimeout bounds each write to a client (S24: SetWriteDeadline(now+20s) before every write).
const WriteTimeout = 20 * time.Second

func (o EventsOptions) serve(w http.ResponseWriter, r *http.Request) error {
	p, ok := httpx.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	req, err := parseRequest(r, p) // every refusal before the first byte
	if err != nil {
		return err
	}
	if o.Hub == nil {
		return httpx.E(httpx.CodeServiceUnavailable, httpx.Msg(MsgStreamDown))
	}
	sub, replay, resync, position, err := o.Hub.Subscribe(req.f, req.cursor)
	switch {
	case errors.Is(err, ErrTooManyForUser):
		return httpx.E(httpx.CodeRateLimited, httpx.Msgf(MsgTooManyForUser, o.Hub.Options().MaxPerUser), httpx.WithHeader("Retry-After", "5"))
	case errors.Is(err, ErrTooManyForOrg):
		return httpx.E(httpx.CodeRateLimited, httpx.Msg(MsgTooManyForOrg), httpx.WithHeader("Retry-After", "5"))
	case err != nil:
		return httpx.E(httpx.CodeServiceUnavailable, httpx.Msg(MsgStreamDown))
	}
	defer sub.Close()
	httpx.Annotate(r.Context(), req.f.Org, "", "")
	ho := o.Hub.Options()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // reverse proxies: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	bw := bufio.NewWriterSize(w, 4096)
	flush := func() error {
		_ = rc.SetWriteDeadline(o.Now().Add(WriteTimeout))
		if err := bw.Flush(); err != nil {
			return err
		}
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
	hb, _ := json.Marshal(hello{Object: "stream", Instance: ho.Instance, Resumed: req.cursor != "" && resync == "",
		HeartbeatS: int(ho.Heartbeat / time.Second), ServerTime: o.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	bw.WriteString("retry: 3000\n\n")
	writeEvent(bw, position, "hello", hb)
	if resync != "" {
		writeEvent(bw, position, "resync", resyncData(resync))
	}
	for _, e := range replay {
		writeEvent(bw, e.Cursor, e.Type, e.Data)
		if bw.Buffered() > 32<<10 {
			if err := flush(); err != nil {
				return nil
			}
		}
	}
	if err := flush(); err != nil {
		return nil
	}

	ping := time.NewTicker(ho.Heartbeat)
	defer ping.Stop()
	life := time.NewTimer(ho.MaxLifetime)
	defer life.Stop()
	recheck := time.NewTicker(ho.Revalidate)
	defer recheck.Stop()
	strikes := 0
	var written []*Envelope
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-sub.Done():
			return nil // the server is draining: the client reconnects elsewhere
		case <-life.C:
			return nil // 30 minutes: the client reconnects with its cursor
		case <-sub.Lagged():
			writeEvent(bw, "", "resync", resyncData(ResyncLagged))
			_ = flush()
			return nil
		case e := <-sub.C():
			batch := append(written[:0], e)
			writeEvent(bw, e.Cursor, e.Type, e.Data)
			// Drain what is already queued into the same write.
			for more := true; more && bw.Buffered() < 32<<10; {
				select {
				case e := <-sub.C():
					writeEvent(bw, e.Cursor, e.Type, e.Data)
					batch = append(batch, e)
				default:
					more = false
				}
			}
			if err := flush(); err != nil {
				return nil
			}
			if o.observe != nil {
				for _, e := range batch {
					o.observe(e.queued)
				}
			}
			written = batch
			ping.Reset(ho.Heartbeat)
		case <-ping.C:
			bw.WriteString(": ping\n\n")
			if err := flush(); err != nil {
				return nil
			}
		case <-recheck.C:
			if o.Recheck == nil {
				continue
			}
			err := o.Recheck(r)
			if err == nil {
				strikes = 0
				continue
			}
			if e := httpx.AsError(err); e != nil && (e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden) {
				b, _ := json.Marshal(SessionEnded{Code: string(e.Code), Message: e.Message})
				writeEvent(bw, "", "session_ended", b)
				_ = flush()
				return nil
			}
			if strikes++; strikes >= recheckStrikes {
				return nil
			}
		}
	}
}

// writeEvent writes one SSE message. data is one line of JSON (encoding/json never emits a raw
// newline), so it needs one data: line.
func writeEvent(bw *bufio.Writer, id, typ string, data []byte) {
	if id != "" {
		bw.WriteString("id: ")
		bw.WriteString(id)
		bw.WriteByte('\n')
	}
	bw.WriteString("event: ")
	bw.WriteString(typ)
	bw.WriteString("\ndata: ")
	bw.Write(data)
	bw.WriteString("\n\n")
}

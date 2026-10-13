//go:build outboundmail

// Package fakegmail is a fake Gmail API and token endpoint for the outbound-mail connector's tests
// and the growth engine's dev end to end (S40). It keeps sent messages in memory, logs every
// request (method and path, never a body), and can be told to misbehave: answer 500 after storing
// (an unknown outcome), 429, change the stored body (MISMATCH), or store the Sent copy with
// another From. It sends nothing anywhere.
//
// S45 (the growth rehearsal): POST /_fake/inbound adds a message to the inbox (a reply, a bounce
// or an unrelated mail, as raw RFC 5322 text), so history, threads and in:inbox listings answer
// the way Gmail's do; GET /_fake/sent lists the Sent copies' headers so a test can reply to one.
package fakegmail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"sync"
)

// Mode is how the next sends behave.
type Mode string

// Modes.
const (
	ModeOK          Mode = ""
	ModeFailAfter   Mode = "fail_after_store" // store, then answer 500: the sender can't know
	ModeRateLimited Mode = "rate_limited"     // 429, nothing stored
	ModeTamperBody  Mode = "tamper_body"      // store with one changed body byte
	ModeFromDenied  Mode = "from_deedglass"   // store with From rewritten to a deedglass.com address
	ModeDropSent    Mode = "drop"             // answer 500 and store nothing
)

// Request is one logged request.
type Request struct {
	Method, Path string
}

// Message is one stored message.
type Message struct {
	ID, ThreadID string
	Raw          []byte
	Labels       []string
	HistoryID    uint64
}

// Server is the fake.
type Server struct {
	mu       sync.Mutex
	Email    string // the profile's address
	mode     Mode
	log      []Request
	messages []Message
	n        int
	history  uint64
}

// New returns a fake whose profile is email.
func New(email string) *Server { return &Server{Email: email} }

// SetMode changes how the next sends behave.
func (s *Server) SetMode(m Mode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = m
}

// Log returns the request log.
func (s *Server) Log() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// Messages returns the stored messages.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// Sends counts POST …/messages/send requests.
func (s *Server) Sends() int {
	n := 0
	for _, r := range s.Log() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/messages/send") {
			n++
		}
	}
	return n
}

// Writes counts every request that isn't a GET or the token POST.
func (s *Server) Writes() int {
	n := 0
	for _, r := range s.Log() {
		if r.Method != http.MethodGet && r.Path != "/token" {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/_fake/") {
		s.mu.Lock()
		s.log = append(s.log, Request{Method: r.Method, Path: r.URL.Path})
		s.mu.Unlock()
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && p == "/token":
		_ = r.ParseForm()
		ans := map[string]any{"access_token": "fake-access-token", "expires_in": 3600, "scope": "https://www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.readonly"}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			ans["refresh_token"] = "fake-refresh-token"
		}
		writeJSON(w, 200, ans)
	case r.Method == http.MethodPost && p == "/_fake/mode":
		s.SetMode(Mode(r.URL.Query().Get("mode")))
		writeJSON(w, 200, map[string]any{"mode": r.URL.Query().Get("mode")})
	case r.Method == http.MethodGet && p == "/_fake/log":
		writeJSON(w, 200, map[string]any{"requests": s.Log(), "sends": s.Sends(), "writes": s.Writes()})
	case r.Method == http.MethodPost && p == "/_fake/inbound":
		s.inbound(w, r)
	case r.Method == http.MethodGet && p == "/_fake/sent":
		s.sentList(w)
	case r.Header.Get("Authorization") != "Bearer fake-access-token":
		writeJSON(w, 401, map[string]any{"error": map[string]any{"status": "UNAUTHENTICATED"}})
	case r.Method == http.MethodGet && p == "/gmail/v1/users/me/profile":
		writeJSON(w, 200, map[string]any{"emailAddress": s.Email})
	case r.Method == http.MethodPost && p == "/gmail/v1/users/me/messages/send":
		s.send(w, r)
	case r.Method == http.MethodGet && p == "/gmail/v1/users/me/messages":
		s.list(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/gmail/v1/users/me/messages/"):
		s.get(w, strings.TrimPrefix(p, "/gmail/v1/users/me/messages/"))
	case r.Method == http.MethodGet && p == "/gmail/v1/users/me/history":
		s.historyList(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/gmail/v1/users/me/threads/"):
		s.thread(w, strings.TrimPrefix(p, "/gmail/v1/users/me/threads/"))
	default:
		writeJSON(w, 404, map[string]any{"error": map[string]any{"status": "NOT_FOUND"}})
	}
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"threadId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"status": "INVALID_ARGUMENT"}})
		return
	}
	raw, err := base64.URLEncoding.DecodeString(b.Raw)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"status": "INVALID_ARGUMENT"}})
		return
	}
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	switch mode {
	case ModeRateLimited:
		writeJSON(w, 429, map[string]any{"error": map[string]any{"status": "RESOURCE_EXHAUSTED"}})
		return
	case ModeDropSent:
		writeJSON(w, 500, map[string]any{"error": map[string]any{"status": "INTERNAL"}})
		return
	case ModeTamperBody:
		if i := bytes.Index(raw, []byte("\r\n\r\n")); i > 0 && i+4 < len(raw) {
			raw = append([]byte(nil), raw...)
			raw[i+4] ^= 0x01
		}
	case ModeFromDenied:
		if m, err := mail.ReadMessage(bytes.NewReader(raw)); err == nil {
			from := m.Header.Get("From")
			raw = bytes.Replace(raw, []byte("From: "+from), []byte("From: someone@deedglass.com"), 1)
		}
	}
	s.mu.Lock()
	s.n++
	id := fmt.Sprintf("msg%06d", s.n)
	tid := b.ThreadID
	if tid == "" {
		tid = fmt.Sprintf("thr%06d", s.n)
	}
	s.history++
	s.messages = append(s.messages, Message{ID: id, ThreadID: tid, Raw: raw, Labels: []string{"SENT"}, HistoryID: s.history})
	s.mu.Unlock()
	if mode == ModeFailAfter {
		writeJSON(w, 500, map[string]any{"error": map[string]any{"status": "INTERNAL"}})
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "threadId": tid, "labelIds": []string{"SENT"}})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	out := []map[string]string{}
	if want, ok := strings.CutPrefix(q, "rfc822msgid:"); ok {
		want = "<" + strings.Trim(want, "<>") + ">"
		for _, m := range s.Messages() {
			mm, err := mail.ReadMessage(bytes.NewReader(m.Raw))
			if err == nil && strings.TrimSpace(mm.Header.Get("Message-Id")) == want {
				out = append(out, map[string]string{"id": m.ID, "threadId": m.ThreadID})
			}
		}
	} else {
		inbox := strings.Contains(q, "in:inbox")
		for _, m := range s.Messages() {
			if inbox && !hasLabel(m, "INBOX") {
				continue
			}
			out = append(out, map[string]string{"id": m.ID, "threadId": m.ThreadID})
		}
	}
	writeJSON(w, 200, map[string]any{"messages": out})
}

func (s *Server) get(w http.ResponseWriter, id string) {
	for _, m := range s.Messages() {
		if m.ID == id {
			writeJSON(w, 200, map[string]any{"id": m.ID, "threadId": m.ThreadID, "labelIds": m.Labels,
				"raw": base64.URLEncoding.EncodeToString(m.Raw), "historyId": fmt.Sprint(m.HistoryID)})
			return
		}
	}
	writeJSON(w, 404, map[string]any{"error": map[string]any{"status": "NOT_FOUND"}})
}

func hasLabel(m Message, l string) bool {
	for _, x := range m.Labels {
		if x == l {
			return true
		}
	}
	return false
}

// Inbound adds one message to the inbox and returns its id. threadID "" starts a new thread.
func (s *Server) Inbound(raw []byte, threadID string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	s.history++
	m := Message{ID: fmt.Sprintf("msg%06d", s.n), ThreadID: threadID, Raw: append([]byte(nil), raw...), Labels: []string{"INBOX", "UNREAD"}, HistoryID: s.history}
	if m.ThreadID == "" {
		m.ThreadID = fmt.Sprintf("thr%06d", s.n)
	}
	s.messages = append(s.messages, m)
	return m
}

func (s *Server) inbound(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Raw      string `json:"raw"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&b); err != nil || b.Raw == "" {
		writeJSON(w, 400, map[string]any{"error": "send {\"raw\": \"<RFC 5322 text>\", \"thread_id\": \"thr…\"}"})
		return
	}
	if _, err := mail.ReadMessage(strings.NewReader(b.Raw)); err != nil {
		writeJSON(w, 400, map[string]any{"error": "raw isn't an RFC 5322 message"})
		return
	}
	m := s.Inbound([]byte(b.Raw), b.ThreadID)
	writeJSON(w, 200, map[string]any{"id": m.ID, "thread_id": m.ThreadID, "history_id": fmt.Sprint(m.HistoryID)})
}

// sentList answers the Sent copies' routing headers (never a body), oldest first.
func (s *Server) sentList(w http.ResponseWriter) {
	out := []map[string]string{}
	for _, m := range s.Messages() {
		if !hasLabel(m, "SENT") {
			continue
		}
		e := map[string]string{"id": m.ID, "thread_id": m.ThreadID}
		if mm, err := mail.ReadMessage(bytes.NewReader(m.Raw)); err == nil {
			e["to"], e["subject"], e["message_id"] = mm.Header.Get("To"), mm.Header.Get("Subject"), strings.TrimSpace(mm.Header.Get("Message-Id"))
			e["references"] = mm.Header.Get("References")
			e["list_unsubscribe"] = mm.Header.Get("List-Unsubscribe")
		}
		out = append(out, e)
	}
	writeJSON(w, 200, map[string]any{"sent": out})
}

func (s *Server) historyList(w http.ResponseWriter, r *http.Request) {
	var start uint64
	_, _ = fmt.Sscan(r.URL.Query().Get("startHistoryId"), &start)
	label := r.URL.Query().Get("labelId")
	s.mu.Lock()
	cur := s.history
	s.mu.Unlock()
	added := []map[string]any{}
	for _, m := range s.Messages() {
		if m.HistoryID <= start || (label != "" && !hasLabel(m, label)) {
			continue
		}
		added = append(added, map[string]any{"id": fmt.Sprint(m.HistoryID), "messagesAdded": []any{map[string]any{"message": map[string]string{"id": m.ID, "threadId": m.ThreadID}}}})
	}
	writeJSON(w, 200, map[string]any{"history": added, "historyId": fmt.Sprint(max(cur, 1))})
}

func (s *Server) thread(w http.ResponseWriter, id string) {
	msgs := []map[string]any{}
	for _, m := range s.Messages() {
		if m.ThreadID != id {
			continue
		}
		var hs []map[string]string
		if mm, err := mail.ReadMessage(bytes.NewReader(m.Raw)); err == nil {
			for k, vs := range mm.Header {
				for _, v := range vs {
					hs = append(hs, map[string]string{"name": k, "value": v})
				}
			}
		}
		msgs = append(msgs, map[string]any{"id": m.ID, "labelIds": m.Labels, "payload": map[string]any{"headers": hs}})
	}
	writeJSON(w, 200, map[string]any{"id": id, "messages": msgs})
}

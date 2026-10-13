//go:build outboundmail

package fakegmail

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func call(t *testing.T, h http.Handler, method, path, body string, out any) int {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer fake-access-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v (%s)", method, path, err, w.Body.String())
		}
	}
	return w.Code
}

func TestInboundHistoryThreadsAndSent(t *testing.T) {
	s := New("arjun@sending.example")
	raw := "From: Arjun <arjun@sending.example>\r\nTo: lin@acme.example\r\nSubject: hi\r\nMessage-Id: <a1@sending.example>\r\n\r\nbody"
	var sent struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	call(t, s, "POST", "/gmail/v1/users/me/messages/send", `{"raw":"`+base64.URLEncoding.EncodeToString([]byte(raw))+`"}`, &sent)
	var h0 struct {
		HistoryID string `json:"historyId"`
	}
	call(t, s, "GET", "/gmail/v1/users/me/history?startHistoryId=0&labelId=INBOX", "", &h0)

	reply := "From: lin@acme.example\r\nTo: arjun@sending.example\r\nSubject: Re: hi\r\nIn-Reply-To: <a1@sending.example>\r\n\r\nInterested"
	b, _ := json.Marshal(map[string]string{"raw": reply, "thread_id": sent.ThreadID})
	var in struct{ ID, ThreadID, HistoryID string }
	if code := call(t, s, "POST", "/_fake/inbound", string(b), &in); code != 200 || in.ID == "" {
		t.Fatalf("inbound: %d %+v", code, in)
	}
	var h struct {
		History []struct {
			MessagesAdded []struct {
				Message struct{ ID, ThreadID string } `json:"message"`
			} `json:"messagesAdded"`
		} `json:"history"`
	}
	call(t, s, "GET", "/gmail/v1/users/me/history?startHistoryId="+h0.HistoryID+"&labelId=INBOX", "", &h)
	if len(h.History) != 1 || h.History[0].MessagesAdded[0].Message.ID != in.ID {
		t.Fatalf("history after the send should list only the inbox reply: %+v", h)
	}
	var l struct{ Messages []struct{ ID string } }
	call(t, s, "GET", "/gmail/v1/users/me/messages?q=in:inbox+newer_than:2d", "", &l)
	if len(l.Messages) != 1 {
		t.Fatalf("in:inbox lists the reply only: %+v", l)
	}
	var th struct {
		Messages []struct {
			Payload struct{ Headers []map[string]string } `json:"payload"`
		} `json:"messages"`
	}
	call(t, s, "GET", "/gmail/v1/users/me/threads/"+sent.ThreadID, "", &th)
	if len(th.Messages) != 2 || len(th.Messages[1].Payload.Headers) == 0 {
		t.Fatalf("thread holds the send and the reply with headers: %+v", th)
	}
	var sl struct {
		Sent []map[string]string `json:"sent"`
	}
	call(t, s, "GET", "/_fake/sent", "", &sl)
	if len(sl.Sent) != 1 || sl.Sent[0]["message_id"] != "<a1@sending.example>" || sl.Sent[0]["to"] != "lin@acme.example" {
		t.Fatalf("sent list: %+v", sl)
	}
	if code := call(t, s, "POST", "/_fake/inbound", `{"raw":""}`, nil); code != 400 {
		t.Fatalf("empty inbound should be refused: %d", code)
	}
}

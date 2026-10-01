package mailer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// lockedBuffer is a bytes.Buffer safe for concurrent writers (slog handlers on workers).
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func testMessage() Message {
	return Message{
		To:             "priya@acme.com",
		Subject:        "Sign in to Deedglass",
		HTML:           "<p>Sign in</p>",
		Text:           "Sign in\n",
		Kind:           "login",
		IdempotencyKey: "login/ltk_01j9zr3m5k8q2w7x4t6v0b1c9d",
		UserID:         "usr_01j9zr3m5k8q2w7x4t6v0b1c9d",
		RequestID:      "req_01j9zr3m5k8q2w7x4t6v0b1c9d",
	}
}

func TestLogRefusesProd(t *testing.T) {
	l, err := NewLog(&bytes.Buffer{}, true)
	if err == nil || l != nil {
		t.Fatal("NewLog accepted prod")
	}
	if err.Error() != "the log mailer is for dev; set DEEDGLASS_RESEND_API_KEY" {
		t.Errorf("error %q", err)
	}
	if _, err := NewLog(nil, false); err == nil {
		t.Error("NewLog accepted a nil writer")
	}
}

func TestLogWritesBlock(t *testing.T) {
	var buf bytes.Buffer
	l, err := NewLog(&buf, false)
	if err != nil {
		t.Fatal(err)
	}
	m := testMessage()
	m.Text = "Sign in to Acme\n\nhttp://127.0.0.1:8080/auth/confirm#t=abc\n"
	if err := l.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	want := "──── deedglass mail · login ────\n" +
		"To: priya@acme.com\n" +
		"Subject: Sign in to Deedglass\n" +
		"\n" +
		"Sign in to Acme\n\nhttp://127.0.0.1:8080/auth/confirm#t=abc\n" +
		strings.Repeat("─", 32) + "\n"
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
	m.To = ""
	if err := l.Send(context.Background(), m); err == nil {
		t.Error("Log sent a message with no recipient")
	}
}

func TestNone(t *testing.T) {
	var m Mailer = None{}
	if err := m.Send(context.Background(), testMessage()); !errors.Is(err, ErrNoMailer) {
		t.Fatalf("None.Send = %v", err)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*Message)
		want string
	}{
		{"ok", func(*Message) {}, ""},
		{"no to", func(m *Message) { m.To = "" }, "no recipient"},
		{"to with newline", func(m *Message) { m.To = "a@b.com\r\nBcc: c@d.com" }, "line break"},
		{"no subject", func(m *Message) { m.Subject = "" }, "no subject"},
		{"no html", func(m *Message) { m.HTML = "" }, "both an HTML and a text body"},
		{"no text", func(m *Message) { m.Text = "" }, "both an HTML and a text body"},
		{"no kind", func(m *Message) { m.Kind = "" }, "no kind"},
		{"long key", func(m *Message) { m.IdempotencyKey = strings.Repeat("k", 257) }, "257 bytes"},
		{"max key", func(m *Message) { m.IdempotencyKey = strings.Repeat("k", 256) }, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := testMessage()
			c.edit(&m)
			err := m.validate()
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("validate = %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), "@") {
				t.Errorf("error carries an address: %v", err)
			}
		})
	}
}

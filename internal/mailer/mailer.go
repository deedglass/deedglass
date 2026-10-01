package mailer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// Message is one email to one recipient. HTML and Text are always both set.
type Message struct {
	// To is the recipient's address. It is never logged.
	To string
	// Subject is the subject line. It is never logged.
	Subject string
	// HTML is the HTML body. It is never logged.
	HTML string
	// Text is the plain-text alternative. It is never logged.
	Text string
	// Kind names the email: "login" or "invite" (S22 adds "approval"). It is sent as the "kind"
	// tag and logged.
	Kind string
	// IdempotencyKey, when set, is sent as Resend's Idempotency-Key header so that a retried
	// attempt never becomes a second email (for example "login/ltk_…"). At most 256 characters.
	IdempotencyKey string
	// Tags are extra Resend tags (name to value). Names and values are reduced to [A-Za-z0-9_-]
	// and 256 characters; the "kind" tag always comes from Kind.
	Tags map[string]string
	// UserID is the recipient's user id (usr_…), for logs only; it is never sent.
	UserID string
	// RequestID is the id of the request that caused the email (req_…), for logs only; it is
	// never sent.
	RequestID string
}

// Mailer sends one Message. Implementations are safe for concurrent use.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// ErrNoMailer is returned by None: prod has no Resend API key, so no email can be sent.
var ErrNoMailer = errors.New("mailer: no mailer is configured; set DEEDGLASS_RESEND_API_KEY")

// ErrRejected wraps a permanent 4xx from Resend. Such a send is not retried; the error text
// carries the HTTP status and Resend's error name, never the response body or the API key.
var ErrRejected = errors.New("mailer: Resend rejected the message")

// maxIdempotencyKey is Resend's limit on the Idempotency-Key header.
const maxIdempotencyKey = 256

// validate checks the fields every mailer needs. Its errors never contain message content.
func (m Message) validate() error {
	switch {
	case m.To == "":
		return errors.New("mailer: the message has no recipient; set Message.To")
	case strings.ContainsAny(m.To, "\r\n"):
		return errors.New("mailer: the recipient contains a line break; check Message.To")
	case m.Subject == "":
		return errors.New("mailer: the message has no subject; set Message.Subject")
	case m.HTML == "" || m.Text == "":
		return errors.New("mailer: the message needs both an HTML and a text body; render it with RenderLogin or RenderInvite")
	case m.Kind == "":
		return errors.New("mailer: the message has no kind; set Message.Kind")
	case len(m.IdempotencyKey) > maxIdempotencyKey:
		return fmt.Errorf("mailer: the idempotency key is %d bytes; the limit is %d", len(m.IdempotencyKey), maxIdempotencyKey)
	}
	return nil
}

// None is the mailer for prod without a Resend API key. Every Send returns ErrNoMailer.
type None struct{}

// Send implements Mailer. It always returns ErrNoMailer.
func (None) Send(context.Context, Message) error { return ErrNoMailer }

// Log is the dev mailer: it writes each message to a writer (usually stderr) as a fenced block
// so that a developer can copy the link or the code. It is refused in prod.
type Log struct {
	mu sync.Mutex
	w  io.Writer
}

// NewLog returns a Log mailer that writes to w. With prod true it returns an error: the log
// mailer prints links and codes, so it is for dev only.
func NewLog(w io.Writer, prod bool) (*Log, error) {
	if prod {
		return nil, errors.New("the log mailer is for dev; set DEEDGLASS_RESEND_API_KEY")
	}
	if w == nil {
		return nil, errors.New("mailer: the log mailer needs a writer")
	}
	return &Log{w: w}, nil
}

// Send implements Mailer. It writes a block like:
//
//	──── deedglass mail · login ────
//	To: priya@acme.com
//	Subject: Sign in to Deedglass
//
//	<the text body>
//	────────────────────────────────
func (l *Log) Send(_ context.Context, m Message) error {
	if err := m.validate(); err != nil {
		return err
	}
	head := "──── deedglass mail · " + m.Kind + " ────"
	var b strings.Builder
	b.WriteString(head + "\n")
	b.WriteString("To: " + m.To + "\n")
	b.WriteString("Subject: " + m.Subject + "\n\n")
	b.WriteString(strings.TrimRight(m.Text, "\n") + "\n")
	b.WriteString(strings.Repeat("─", utf8.RuneCountInString(head)) + "\n")
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err := io.WriteString(l.w, b.String())
	return err
}

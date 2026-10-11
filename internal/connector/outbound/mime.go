//go:build outboundmail

package outbound

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Limits of a message.
const (
	MaxBodyBytes = 16 << 10
	MaxRaw       = 64 << 10
	MaxSubject   = 120
)

// NormaliseBody is the one body rule, shared with the growth engine by a golden
// (testdata/outbound/normalise): CRLF and CR become LF, trailing spaces and tabs are stripped from
// every line, the text ends with exactly one LF, and it is NFC.
func NormaliseBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	s = strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	return norm.NFC.String(s)
}

// BodySHA256 is "sha256:" + hex of the normalised body.
func BodySHA256(body string) string {
	sum := sha256.Sum256([]byte(NormaliseBody(body)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// MessageID is the deterministic Message-ID of an action's message:
// <first 26 chars of lowercase base32(sha256("deedglass/outbound/message-id/v1\n"+action+"\n"+connector))@domain>.
func MessageID(actionID, connectorID, domain string) string {
	sum := sha256.Sum256([]byte("deedglass/outbound/message-id/v1\n" + actionID + "\n" + connectorID))
	return "<" + strings.ToLower(b32.EncodeToString(sum[:]))[:26] + "@" + domain + ">"
}

// Message is what Build turns into RFC 5322 bytes.
type Message struct {
	FromName, From string
	To             string
	Subject        string
	BodyText       string // already checked; normalised here
	Date           time.Time
	MessageID      string
	InReplyTo      string
	References     []string
	UnsubURL       string // https://<domain>/u/<token>
	UnsubMailto    string // mailto:u+<token>@<domain>?subject=unsubscribe
}

// ErrInjection is a header value with a line break.
var ErrInjection = errors.New("outbound: a header value holds a line break")

// Build writes the message: plain text only, quoted-printable, the two RFC 8058 headers, no
// X-Mailer, tracking header, HTML part, image or attachment.
func Build(m Message) ([]byte, error) {
	for _, v := range append([]string{m.FromName, m.From, m.To, m.Subject, m.MessageID, m.InReplyTo, m.UnsubURL, m.UnsubMailto}, m.References...) {
		if strings.ContainsAny(v, "\r\n") {
			return nil, ErrInjection
		}
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	from := (&mail.Address{Name: m.FromName, Address: m.From}).String()
	h("From", from)
	h("To", m.To)
	subj := m.Subject
	if !isASCII(subj) {
		subj = mime.QEncoding.Encode("UTF-8", subj)
	}
	h("Subject", subj)
	h("Date", m.Date.UTC().Format(time.RFC1123Z))
	h("Message-ID", m.MessageID)
	if m.InReplyTo != "" {
		h("In-Reply-To", m.InReplyTo)
	}
	if len(m.References) > 0 {
		h("References", strings.Join(m.References, " "))
	}
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=UTF-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	h("List-Unsubscribe", "<"+m.UnsubURL+">, <"+m.UnsubMailto+">")
	h("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(NormaliseBody(m.BodyText), "\n", "\r\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	if b.Len() > MaxRaw {
		return nil, fmt.Errorf("outbound: the message is %d bytes; the limit is 64 KiB", b.Len())
	}
	return b.Bytes(), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

var (
	reURL = regexp.MustCompile(`(?i)\bhttps?://[^\s<>()"']+`)
	// reBare is a token Gmail would turn into a link: a host with a common top-level domain, or
	// www.<anything>. Email addresses aren't counted (the @ check below).
	reBare  = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9@./_-])((?:www\.)?(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|ai|dev|app|co|sh|xyz|us|uk|in|de|me|so|gg|tech|cloud|info|biz|ly|to|tv|fm|run|page|site|online|store)|www\.[a-z0-9-]+\.[a-z]{2,})(?:[/?#][^\s]*)?\b`)
	reWord  = regexp.MustCompile(`\S+`)
	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

// CountLinks counts http(s) URLs in a body.
func CountLinks(body string) int { return len(reURL.FindAllString(body, -1)) }

// CountBareDomains counts domains outside URLs and email addresses that Gmail would auto-link.
func CountBareDomains(body string) int {
	s := reURL.ReplaceAllString(body, " ")
	s = reEmail.ReplaceAllString(s, " ")
	return len(reBare.FindAllString(s, -1))
}

// CountWords counts the words above the signature line (the first line equal to sig), or the whole
// body when sig is empty or absent.
func CountWords(body, sig string) int {
	body = NormaliseBody(body)
	if sig != "" {
		lines := strings.Split(body, "\n")
		for i, l := range lines {
			if strings.TrimSpace(l) == sig {
				body = strings.Join(lines[:i], "\n")
				break
			}
		}
	}
	return len(reWord.FindAllString(body, -1))
}

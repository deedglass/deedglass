package gmail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"unicode/utf8"
)

// Limits of a draft (S30 §3.2).
const (
	MaxSubject = 250
	MaxBody    = 256 << 10
	MaxRaw     = 1 << 20
)

// MsgHeaderInjection is the refusal for a line break in a header value.
const MsgHeaderInjection = "Email headers can't contain line breaks."

// Draft is what a draft says. Addresses are already parsed and lowercased.
type Draft struct {
	To, Cc, Bcc []*mail.Address
	Subject     string
	BodyText    string
	BodyHTML    string
	InReplyTo   string // a Message-ID, "<…>"
	Date        string // fixed by tests; the builder leaves it out when empty
	Boundary    string // fixed by tests; random otherwise
}

// ErrInjection is a header value with CR or LF.
var ErrInjection = errors.New(MsgHeaderInjection)

// ParseAddresses parses each entry as exactly one address (mail.ParseAddress), so a display-name
// comma can never add a recipient. Addresses are lowercased.
func ParseAddresses(param string, in []string) ([]*mail.Address, error) {
	out := make([]*mail.Address, 0, len(in))
	for i, s := range in {
		if strings.ContainsAny(s, "\r\n") {
			return nil, fmt.Errorf("%s[%d]: %w", param, i, ErrInjection)
		}
		a, err := mail.ParseAddress(s)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: isn't one email address", param, i)
		}
		if strings.ContainsAny(a.Name, "\r\n") {
			return nil, fmt.Errorf("%s[%d]: %w", param, i, ErrInjection)
		}
		a.Address = strings.ToLower(a.Address)
		out = append(out, a)
	}
	return out, nil
}

// Build writes the draft as an RFC 5322 message: CRLF lines, a Q-encoded subject when it isn't
// ASCII, text/plain UTF-8 quoted-printable, multipart/alternative when HTML is present,
// In-Reply-To and References for replies. Nothing about Deedglass goes in a header: when a person
// sends the draft, recipients see only what the agent wrote.
func Build(d Draft) ([]byte, error) {
	for _, v := range []string{d.Subject, d.InReplyTo, d.Date} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, ErrInjection
		}
	}
	if !utf8.ValidString(d.Subject) || !utf8.ValidString(d.BodyText) || !utf8.ValidString(d.BodyHTML) {
		return nil, errors.New("the draft isn't valid UTF-8")
	}
	var b bytes.Buffer
	hdr := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	if d.Date != "" {
		hdr("Date", d.Date)
	}
	if len(d.To) > 0 {
		hdr("To", addrList(d.To))
	}
	if len(d.Cc) > 0 {
		hdr("Cc", addrList(d.Cc))
	}
	if len(d.Bcc) > 0 {
		hdr("Bcc", addrList(d.Bcc))
	}
	hdr("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	if d.InReplyTo != "" {
		hdr("In-Reply-To", d.InReplyTo)
		hdr("References", d.InReplyTo)
	}
	hdr("MIME-Version", "1.0")
	text := normalizeCRLF(d.BodyText)
	if d.BodyHTML == "" {
		hdr("Content-Type", `text/plain; charset="UTF-8"`)
		hdr("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, text); err != nil {
			return nil, err
		}
	} else {
		mw := multipart.NewWriter(&b)
		if d.Boundary != "" {
			if err := mw.SetBoundary(d.Boundary); err != nil {
				return nil, err
			}
		}
		hdr("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
		b.WriteString("\r\n")
		for _, part := range []struct{ ctype, body string }{{"text/plain", text}, {"text/html", normalizeCRLF(d.BodyHTML)}} {
			w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {part.ctype + `; charset="UTF-8"`}, "Content-Transfer-Encoding": {"quoted-printable"}})
			if err != nil {
				return nil, err
			}
			var pb bytes.Buffer
			if err := writeQP(&pb, part.body); err != nil {
				return nil, err
			}
			if _, err := w.Write(pb.Bytes()); err != nil {
				return nil, err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
	}
	if b.Len() > MaxRaw {
		return nil, fmt.Errorf("the draft is %d bytes; the limit is 1 MiB", b.Len())
	}
	return b.Bytes(), nil
}

func writeQP(b *bytes.Buffer, s string) error {
	w := quotedprintable.NewWriter(b)
	if _, err := w.Write([]byte(s)); err != nil {
		return err
	}
	return w.Close()
}

func addrList(as []*mail.Address) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// normalizeCRLF turns every line ending into CRLF.
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}

// NormalizeBody is how read-back compares bodies: CRLF → LF, trailing spaces trimmed on each
// line, exactly one final newline.
func NormalizeBody(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// BodyDigest is "sha256:<hex>" of NormalizeBody(s).
func BodyDigest(s string) string {
	sum := sha256.Sum256([]byte(NormalizeBody(s)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

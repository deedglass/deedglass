package gmail

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

// FuzzMIMEHeaders: whatever the subject, address or reply id, no header injection survives: the
// built message has only Deedglass's headers and exactly the recipients asked for.
func FuzzMIMEHeaders(f *testing.F) {
	f.Add("Order 4121", "priya@acme.com", "<a@b>")
	f.Add("Order 4121\r\nBcc: x@evil.test", "a@acme.com", "")
	f.Add("ok", "\"x\r\nBcc: y@evil\" <a@acme.com>", "<a>\nBcc: z")
	f.Add("Rückerstattung =?utf-8?q?x?=", "\"Smith, John\" <john@acme.com>", "")
	f.Fuzz(func(t *testing.T, subject, addr, reply string) {
		to, err := ParseAddresses("to", []string{addr})
		if err != nil {
			return
		}
		raw, err := Build(Draft{To: to, Subject: subject, BodyText: "hi", InReplyTo: reply})
		if err != nil {
			return
		}
		m, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("unreadable message: %v\n%q", err, raw)
		}
		allowed := map[string]bool{"To": true, "Subject": true, "Mime-Version": true, "Content-Type": true, "Content-Transfer-Encoding": true,
			"In-Reply-To": true, "References": true}
		for k := range m.Header {
			if !allowed[k] {
				t.Fatalf("an injected header %q in %q", k, raw)
			}
		}
		as, err := m.Header.AddressList("To")
		if err != nil || len(as) != 1 || !strings.EqualFold(as[0].Address, to[0].Address) {
			t.Fatalf("recipients %v (%v) from %q", as, err, raw)
		}
	})
}

package vault

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// RedactedText is what a Secret prints in every format, and what redaction puts in place of a
// secret value.
const RedactedText = "[redacted]"

// Secret is an opened credential. It never prints its values: String, GoString, every fmt verb,
// JSON and slog all show "[redacted]". Field is the one accessor, and Destroy zeroes the bytes it
// holds (Go may still have copies; docs/VAULT.md says so).
type Secret struct {
	mu        sync.Mutex
	kind      string
	fields    map[string][]byte
	redactor  *Redactor
	destroyed bool
}

func newSecret(kind string, fields map[string][]byte) *Secret {
	s := &Secret{kind: kind, fields: fields}
	vals := make([]string, 0, len(fields))
	for _, v := range fields {
		vals = append(vals, string(v))
	}
	r := NewRedactor(vals...)
	if kind == KindBasic {
		r.AddBasic(string(fields["username"]), string(fields["password"]))
	}
	s.redactor = r
	return s
}

// NewSecretForTest builds a Secret outside the vault, for connector tests.
func NewSecretForTest(kind string, fields map[string]string) *Secret {
	fs := make(map[string][]byte, len(fields))
	for k, v := range fields {
		fs[k] = []byte(v)
	}
	return newSecret(kind, fs)
}

// Kind is the credential's kind (bearer, header, basic, query, dsn, fields).
func (s *Secret) Kind() string { return s.kind }

// Field returns one value as a string, or "" when absent or destroyed. Use it at the point the
// value is sent, never to print it.
func (s *Secret) Field(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return ""
	}
	return string(s.fields[name])
}

// Names lists the field names, sorted.
func (s *Secret) Names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.fields))
	for k := range s.fields {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Redactor covers every form of every value in this secret.
func (s *Secret) Redactor() *Redactor { return s.redactor }

// Destroy zeroes the values. The Redactor keeps working (it holds its own copies, which are
// needed until the call's output has been redacted).
func (s *Secret) Destroy() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.fields {
		clear(v)
	}
	s.destroyed = true
}

func (s *Secret) String() string               { return RedactedText }
func (s *Secret) GoString() string             { return "vault.Secret(" + RedactedText + ")" }
func (s *Secret) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(RedactedText)) }
func (s *Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + RedactedText + `"`), nil }
func (s *Secret) MarshalText() ([]byte, error) { return []byte(RedactedText), nil }
func (s *Secret) LogValue() slog.Value         { return slog.StringValue(RedactedText) }
func (s *Secret) UnmarshalJSON([]byte) error   { return fmt.Errorf("vault: a Secret can't be decoded") }
func (s *Secret) GobEncode() ([]byte, error) {
	return nil, fmt.Errorf("vault: a Secret can't be encoded")
}
func (s *Secret) MarshalBinary() ([]byte, error) {
	return nil, fmt.Errorf("vault: a Secret can't be encoded")
}

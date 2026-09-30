package config

import (
	"fmt"
	"log/slog"
)

// Redacted is what every formatter prints in place of a secret.
const Redacted = "••••••••"

// Secret holds a secret string. Every formatter prints Redacted: fmt with any verb, JSON, text
// marshalling and slog. Reveal is the only accessor. The value lives inside a closure, so
// even reflection-based printing of a struct that holds a Secret in an unexported field, with any
// verb, shows a function address, never the bytes.
type Secret struct{ reveal func() string }

// NewSecret wraps v. An empty v is an unset secret.
func NewSecret(v string) Secret {
	if v == "" {
		return Secret{}
	}
	return Secret{reveal: func() string { return v }}
}

// Reveal returns the secret. Call it only where the value is used, never to print it.
func (s Secret) Reveal() string {
	if s.reveal == nil {
		return ""
	}
	return s.reveal()
}

// IsSet reports whether the secret has a value.
func (s Secret) IsSet() bool { return s.reveal != nil }

func (s Secret) String() string   { return Redacted }
func (s Secret) GoString() string { return `config.Secret("` + Redacted + `")` }

// Format implements fmt.Formatter, so every verb (%v %+v %#v %s %q %x …) prints Redacted.
func (s Secret) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		fmt.Fprint(f, s.GoString())
		return
	}
	fmt.Fprint(f, Redacted)
}

func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }
func (s Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }
func (s Secret) LogValue() slog.Value         { return slog.StringValue(Redacted) }

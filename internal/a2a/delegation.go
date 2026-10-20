package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SpecVersion is the A2A version this package implements (A2A-Version, Major.Minor).
const SpecVersion = "1.0"

// ExtensionURI names Deedglass's delegation-chain extension. Its data sits in Message.metadata
// under this key and the URI is listed in Message.extensions (A2A §4.6.2). It is an identifier;
// docs/A2A.md defines the shape.
const ExtensionURI = "https://deedglass.com/a2a/delegation/v1"

// MaxHops bounds a chain (the person, every agent, the peer).
const MaxHops = 16

// Hop kinds.
const (
	HopPerson = "person"
	HopAgent  = "agent"
)

// Hop is one link of a delegation chain.
type Hop struct {
	Kind string `json:"kind"`           // person | agent
	Ref  string `json:"ref"`            // an opaque reference (on_behalf_of's charset) or the peer's URL
	Name string `json:"name,omitempty"` // a display name, ≤ 64 bytes
}

// Delegation is the extension's data.
type Delegation struct {
	Chain []Hop `json:"chain"`
}

var (
	reRef     = regexp.MustCompile(`^[A-Za-z0-9._~:@/+=-]{1,256}$`)
	errNoHops = errors.New("the chain is empty")
)

func (h Hop) check() error {
	switch {
	case h.Kind != HopPerson && h.Kind != HopAgent:
		return fmt.Errorf("kind %q isn't person or agent", clip(h.Kind, 16))
	case !reRef.MatchString(h.Ref):
		return errors.New("ref must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -")
	case len(h.Name) > 64 || !utf8.ValidString(h.Name) || strings.ContainsFunc(h.Name, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return errors.New("name must be at most 64 bytes of printable UTF-8")
	}
	return nil
}

// CheckChain applies the chain's rules: 1 to MaxHops hops, each well formed, and a person only
// first.
func CheckChain(c []Hop) error {
	if len(c) == 0 {
		return errNoHops
	}
	if len(c) > MaxHops {
		return fmt.Errorf("the chain has %d hops; at most %d", len(c), MaxHops)
	}
	for i, h := range c {
		if err := h.check(); err != nil {
			return fmt.Errorf("hop %d: %w", i+1, err)
		}
		if i > 0 && h.Kind == HopPerson {
			return fmt.Errorf("hop %d: only the first hop may be a person", i+1)
		}
	}
	return nil
}

// ChainFromMetadata reads the chain a caller sent in a message's metadata. No extension entry is
// (nil, nil).
func ChainFromMetadata(meta json.RawMessage) ([]Hop, error) {
	if len(meta) == 0 || string(meta) == "null" {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(meta, &m); err != nil {
		return nil, errors.New("message.metadata isn't an object")
	}
	raw, ok := m[ExtensionURI]
	if !ok {
		return nil, nil
	}
	var d Delegation
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the delegation entry isn't {\"chain\":[{kind,ref,name}]}: %v", err)
	}
	if err := CheckChain(d.Chain); err != nil {
		return nil, err
	}
	return d.Chain, nil
}

// Person is the chain's person, or "".
func Person(c []Hop) string {
	if len(c) > 0 && c[0].Kind == HopPerson {
		return c[0].Ref
	}
	return ""
}

// Recorded is the part of a recorded a2a action's arguments that actions show reads.
type Recorded struct {
	Peer       Peer  `json:"peer"`
	Delegation []Hop `json:"delegation"`
}

// Peer names the remote agent in a recorded action.
type Peer struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
}

// ChainFromRequest reads the delegation chain from a recorded action's arguments. ok is false for
// any action the proxy didn't record, or whose arguments were pruned.
func ChainFromRequest(args json.RawMessage) ([]Hop, bool) {
	if len(args) == 0 {
		return nil, false
	}
	var r Recorded
	if json.Unmarshal(args, &r) != nil || r.Peer.URL == "" || len(r.Delegation) < 2 || CheckChain(r.Delegation) != nil {
		return nil, false
	}
	return r.Delegation, true
}

// FormatChain is the chain on one line: "u_5780 → planner → researcher".
func FormatChain(c []Hop) string {
	parts := make([]string, len(c))
	for i, h := range c {
		parts[i] = h.Ref
		if h.Name != "" {
			parts[i] = h.Name
		}
	}
	return strings.Join(parts, " → ")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

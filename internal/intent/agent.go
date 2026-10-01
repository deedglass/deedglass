package intent

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/gowebpki/jcs"
)

// Agent intents (S09, docs/INTENTS.md): deedglass.intent/1 as an agent's runtime signs it for one
// Gate call. This file is the one implementation of the wire format; internal/identity checks
// intents with it and pkg/deedglass signs them with it, so the signer and the checker can't drift.
//
// The V0 transfer intent above (Parse, op TRANSFER) is a different closed schema that shares the
// version string; the two field sets don't overlap, so no document satisfies both.
const (
	AgentVersion      = "deedglass.intent/1"
	MaxAgentBytes     = 2048      // the intent's JCS bytes
	MaxAgentTTLMillis = 300000    // 0 < exp − iat ≤ 5 minutes
	MaxArgsBytes      = 256 << 10 // the arguments sent with the call
	MaxArgsDepth      = 32
	SignatureDomain   = "deedglass/intent/v1\n" // signed bytes: SignatureDomain + intent_hash
	SignatureBytes    = 64
)

// Field patterns of deedglass.intent/1 (docs/INTENTS.md §2).
const (
	PatternOrg    = `^org_[0-7][0-9a-hjkmnp-tv-z]{25}$`
	PatternAgent  = `^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$`
	PatternTool   = `^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`
	PatternDigest = `^sha256:[0-9a-f]{64}$`
	PatternRef    = `^[A-Za-z0-9._~:@/+=-]{1,256}$` // on_behalf_of and session (the OBO grammar)
	MaxToolBytes  = 200
)

var (
	reOrg    = regexp.MustCompile(PatternOrg)
	reAgent  = regexp.MustCompile(PatternAgent)
	reTool   = regexp.MustCompile(PatternTool)
	reDigest = regexp.MustCompile(PatternDigest)
	reRef    = regexp.MustCompile(PatternRef)
)

// ValidTool reports whether s is a canonical connector.tool name.
func ValidTool(s string) bool { return len(s) <= MaxToolBytes && reTool.MatchString(s) }

// ValidRef reports whether s matches the OBO grammar (Deedglass-On-Behalf-Of, Deedglass-Session).
func ValidRef(s string) bool { return reRef.MatchString(s) }

// AgentRequired and AgentOptional form the closed key set of an agent intent.
var (
	AgentRequired = []string{"agent", "args_digest", "exp", "iat", "kid", "nonce", "org", "tool", "v"}
	AgentOptional = []string{"on_behalf_of", "session"}
)

// AgentIntent is a parsed agent intent. Iat and Exp are Unix milliseconds.
type AgentIntent struct {
	V          string `json:"v"`
	Org        string `json:"org"`
	Agent      string `json:"agent"`
	KID        string `json:"kid"`
	Tool       string `json:"tool"`
	ArgsDigest string `json:"args_digest"`
	Nonce      string `json:"nonce"`
	Iat        int64  `json:"iat"`
	Exp        int64  `json:"exp"`
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	Session    string `json:"session,omitempty"`
}

// Reasons an agent intent or its arguments are refused (docs/INTENTS.md §6). They are stable:
// logs, the golden vectors and the SDKs use them.
const (
	WhySyntax       = "syntax"
	WhyTooLarge     = "too_large"
	WhyStrict       = "strict"
	WhyNotCanonical = "not_canonical"
	WhySchema       = "schema"
	WhyVersion      = "version"
	WhyBigInt       = "big_int"
	WhyArgsInvalid  = "args_not_canonicalizable"
)

// AgentError is a refused agent intent: a stable reason, the field it is about, and detail for
// the message.
type AgentError struct {
	Reason string
	Field  string // the intent field, "" for the whole document
	Detail string
	Err    error // the strict-parser error, if any
}

func (e *AgentError) Error() string {
	s := "intent: " + e.Reason
	if e.Field != "" {
		s += " (" + e.Field + ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

func (e *AgentError) Unwrap() error { return e.Err }

func agentErr(reason, field, detail string, err error) *AgentError {
	return &AgentError{Reason: reason, Field: field, Detail: detail, Err: err}
}

var b64 = base64.RawURLEncoding

// SplitHeader decodes Deedglass-Intent: two unpadded base64url parts joined by one dot, the
// intent bytes and a 64-byte Ed25519 signature (check 1). It does not look at the intent.
func SplitHeader(h string) (intentBytes, sig []byte, err error) {
	a, s, ok := strings.Cut(h, ".")
	if !ok || a == "" || s == "" || strings.Contains(s, ".") {
		return nil, nil, agentErr(WhySyntax, "", "the header isn't two parts joined by a dot", nil)
	}
	// base64.RawURLEncoding is strict about the alphabet and padding; it also refuses non-zero
	// trailing bits only in Strict mode, so the same bytes have exactly one encoding.
	enc := b64.Strict()
	if intentBytes, err = enc.DecodeString(a); err != nil {
		return nil, nil, agentErr(WhySyntax, "", "the intent part isn't unpadded base64url", nil)
	}
	if sig, err = enc.DecodeString(s); err != nil {
		return nil, nil, agentErr(WhySyntax, "", "the signature part isn't unpadded base64url", nil)
	}
	if len(sig) != SignatureBytes {
		return nil, nil, agentErr(WhySyntax, "", "the signature isn't 64 bytes", nil)
	}
	return intentBytes, sig, nil
}

// JoinHeader is the inverse of SplitHeader.
func JoinHeader(intentBytes, sig []byte) string {
	return b64.EncodeToString(intentBytes) + "." + b64.EncodeToString(sig)
}

// ParseAgent runs checks 2–5 on the intent bytes: size, the strict profile, canonical form, then
// the closed schema and its patterns (version last, so a v2 intent with v2 fields reports schema).
func ParseAgent(b []byte) (*AgentIntent, error) {
	if len(b) > MaxAgentBytes {
		return nil, agentErr(WhyTooLarge, "", "the intent is larger than 2048 bytes", nil)
	}
	v, err := ParseStrict(b)
	if err != nil {
		return nil, agentErr(WhyStrict, pathOf(err), err.Error(), err)
	}
	if err := CheckCanonical(b); err != nil {
		return nil, agentErr(WhyNotCanonical, "", "the bytes aren't their RFC 8785 form", err)
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, agentErr(WhySchema, "", "the intent must be a JSON object", nil)
	}
	if err := closedAgentKeys(top); err != nil {
		return nil, err
	}
	in := &AgentIntent{}
	strs := []struct {
		key string
		dst *string
		re  *regexp.Regexp
	}{
		{"v", &in.V, nil}, {"org", &in.Org, reOrg}, {"agent", &in.Agent, reAgent}, {"kid", &in.KID, reKid},
		{"tool", &in.Tool, reTool}, {"args_digest", &in.ArgsDigest, reDigest}, {"nonce", &in.Nonce, reNonce},
		{"on_behalf_of", &in.OnBehalfOf, reRef}, {"session", &in.Session, reRef},
	}
	for _, f := range strs {
		raw, present := top[f.key]
		if !present {
			continue // closedAgentKeys checked the required ones
		}
		s, ok := raw.(string)
		if !ok {
			return nil, agentErr(WhySchema, f.key, "must be a string", nil)
		}
		if f.re != nil && !f.re.MatchString(s) {
			return nil, agentErr(WhySchema, f.key, "doesn't match "+f.re.String(), nil)
		}
		*f.dst = s
	}
	if len(in.Tool) > MaxToolBytes {
		return nil, agentErr(WhySchema, "tool", "longer than 200 bytes", nil)
	}
	for _, f := range []struct {
		key string
		dst *int64
	}{{"iat", &in.Iat}, {"exp", &in.Exp}} {
		n, ok := top[f.key].(int64)
		if !ok {
			return nil, agentErr(WhySchema, f.key, "must be an integer (Unix milliseconds)", nil)
		}
		if n <= 0 {
			return nil, agentErr(WhySchema, f.key, "must be positive Unix milliseconds", nil)
		}
		*f.dst = n
	}
	if in.V != AgentVersion {
		return nil, agentErr(WhyVersion, "v", in.V, nil) // Detail is the refused version

	}
	return in, nil
}

func closedAgentKeys(obj map[string]any) error {
	allowed := map[string]bool{}
	for _, k := range AgentRequired {
		allowed[k] = true
	}
	for _, k := range AgentOptional {
		allowed[k] = true
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return agentErr(WhySchema, k, "unknown field", nil)
		}
	}
	for _, k := range AgentRequired {
		if _, ok := obj[k]; !ok {
			return agentErr(WhySchema, k, "missing field", nil)
		}
	}
	return nil
}

func pathOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Path
	}
	return ""
}

// EncodeAgent returns the intent's exact JCS bytes. It refuses an intent ParseAgent would refuse
// (the signer must never produce one the checker rejects).
func EncodeAgent(in *AgentIntent) ([]byte, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	b, err := jcs.Transform(raw)
	if err != nil {
		return nil, err
	}
	if _, err := ParseAgent(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Digest is the contract's digest rule: "sha256:" + lowercase hex of SHA-256(b).
func Digest(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// SigningInput is the bytes an agent signs: "deedglass/intent/v1\n" + intent_hash.
func SigningInput(intentHash string) []byte { return []byte(SignatureDomain + intentHash) }

// ParseStrictArgs parses tool arguments under the args profile (docs/INTENTS.md §3): valid UTF-8,
// no duplicate keys, depth at most 32, integers with magnitude at most 2^53−1, and, unlike the
// intent itself, fractions and exponents (IEEE-754 doubles). It returns Go values with float64
// for fractions.
func ParseStrictArgs(b []byte) (any, error) {
	return parseStrict(b, profile{floats: true, maxDepth: MaxArgsDepth})
}

// ArgsDigest is "sha256:" + hex(SHA-256(JCS(args))) after the args profile. It doesn't depend on
// key order or whitespace in args. The error is an *AgentError: big_int for an integer JCS would
// round (send it as a string), else args_not_canonicalizable.
func ArgsDigest(args []byte) (string, error) {
	if len(args) > MaxArgsBytes {
		return "", agentErr(WhyArgsInvalid, "args", "the arguments are larger than 256 KiB", nil)
	}
	if _, err := ParseStrictArgs(args); err != nil {
		if CodeOf(err) == CodeIntegerRange {
			return "", agentErr(WhyBigInt, "args", err.Error(), err)
		}
		return "", agentErr(WhyArgsInvalid, "args", err.Error(), err)
	}
	c, err := jcs.Transform(args)
	if err != nil {
		return "", agentErr(WhyArgsInvalid, "args", "canonicalization failed: "+err.Error(), err)
	}
	return Digest(c), nil
}

// CanonicalArgs returns JCS(args) after the args profile (for --explain and the vectors).
func CanonicalArgs(args []byte) ([]byte, error) {
	if _, err := ArgsDigest(args); err != nil {
		return nil, err
	}
	return jcs.Transform(args)
}

// SameBytes reports whether two digests or hashes are equal. They are public values, so a plain
// comparison is fine; it exists so callers don't reach for bytes.Equal on strings.
func SameBytes(a, b string) bool { return bytes.Equal([]byte(a), []byte(b)) }

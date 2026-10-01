package intent

import (
	"bytes"
	"crypto/sha256"
	"regexp"
	"sort"

	"github.com/gowebpki/jcs"
)

// Fixed values of the deedglass.transfer/1 format (spec §5).
const (
	Type         = "deedglass.transfer/1"
	OpTransfer   = "TRANSFER"
	OpVersion    = 1
	MaxBytes     = 4096   // same bound as the controlled function (spec §4.4 step 1)
	MaxTTLMillis = 300000 // exp - iat <= 300000 (spec §5)
)

// Field patterns. They are mirrored in bundles/transfer_v1/intent.schema.json
// and in sql/003_transfer_v1.sql; a unit test keeps the schema file in step.
const (
	PatternIss      = `^[a-z0-9._-]{1,64}$`
	PatternKid      = `^[0-9a-f]{32}$`
	PatternAud      = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	PatternBundle   = `^sha256:[0-9a-f]{64}$`
	PatternNonce    = `^[0-9a-f]{32}$`
	PatternAmount   = `^[1-9][0-9]{0,17}$`
	PatternCurrency = `^[A-Z]{3}$`
	PatternAccount  = `^[a-z0-9_]{1,40}$`
)

var (
	reIss      = regexp.MustCompile(PatternIss)
	reKid      = regexp.MustCompile(PatternKid)
	reAud      = regexp.MustCompile(PatternAud)
	reBundle   = regexp.MustCompile(PatternBundle)
	reNonce    = regexp.MustCompile(PatternNonce)
	reAmount   = regexp.MustCompile(PatternAmount)
	reCurrency = regexp.MustCompile(PatternCurrency)
	reAccount  = regexp.MustCompile(PatternAccount)
)

// RequiredKeys and OptionalKeys form the closed top-level key set.
var (
	RequiredKeys     = []string{"args", "aud", "bundle", "exp", "iat", "iss", "kid", "nonce", "op", "op_v", "v"}
	OptionalKeys     = []string{"agent"}
	RequiredArgsKeys = []string{"amount", "currency", "from", "to"}
)

// Intent is a parsed deedglass.transfer/1 TRANSFER intent.
type Intent struct {
	V      string
	Iss    string
	Kid    string
	Aud    string
	Op     string
	OpV    int64
	Bundle string
	Nonce  string
	Iat    int64 // Unix ms
	Exp    int64 // Unix ms
	Args   Args
	Agent  *string // informational only in V0
}

// Args are the TRANSFER arguments. Amount is integer minor units as a string.
type Args struct {
	Amount   string
	Currency string
	From     string
	To       string
}

// Parse checks b in the order spec §5 requires and returns the intent:
//  1. size <= MaxBytes;
//  2. the strict profile (ParseStrict);
//  3. the closed deedglass.transfer/1 schema (no unknown or missing keys, types, patterns);
//  4. canonical form: b must equal JCS(b) byte for byte.
//
// Parse does not verify the signature or any registry, policy or clock rule.
func Parse(b []byte) (*Intent, error) {
	if len(b) > MaxBytes {
		return nil, errAt(CodeTooLarge, "", -1, "intent exceeds 4096 bytes")
	}
	v, err := ParseStrict(b)
	if err != nil {
		return nil, err
	}
	in, err := fromValue(v)
	if err != nil {
		return nil, err
	}
	if err := CheckCanonical(b); err != nil {
		return nil, err
	}
	return in, nil
}

// CheckCanonical reports whether b equals its RFC 8785 canonical form.
// Call it only on input that passed ParseStrict: gowebpki/jcs rounds integers
// above 2^53-1 and accepts fractions (spike S4), which ParseStrict refuses.
func CheckCanonical(b []byte) error {
	c, err := jcs.Transform(b)
	if err != nil {
		return errAt(CodeNonCanonical, "", -1, "canonicalization failed: "+err.Error())
	}
	if !bytes.Equal(c, b) {
		return errAt(CodeNonCanonical, "", -1, "bytes differ from their JCS (RFC 8785) form")
	}
	return nil
}

// Hash returns intent_hash = SHA-256 of the exact intent bytes.
func Hash(b []byte) [32]byte { return sha256.Sum256(b) }

func fromValue(v any) (*Intent, error) {
	top, ok := v.(map[string]any)
	if !ok {
		return nil, errField(CodeWrongType, "", "intent must be a JSON object")
	}
	if err := closedKeys(top, "", RequiredKeys, OptionalKeys); err != nil {
		return nil, err
	}
	in := &Intent{}
	var err error
	str := func(key string, re *regexp.Regexp) string {
		if err != nil {
			return ""
		}
		var s string
		s, err = getString(top, key, key, re)
		return s
	}
	in.V = str("v", nil)
	in.Iss = str("iss", reIss)
	in.Kid = str("kid", reKid)
	in.Aud = str("aud", reAud)
	in.Op = str("op", nil)
	in.Bundle = str("bundle", reBundle)
	in.Nonce = str("nonce", reNonce)
	if err != nil {
		return nil, err
	}
	if in.V != Type {
		return nil, errField(CodeBadValue, "v", "must be "+Type)
	}
	if in.Op != OpTransfer {
		return nil, errField(CodeBadValue, "op", "must be "+OpTransfer)
	}
	if in.OpV, err = getInt(top, "op_v"); err != nil {
		return nil, err
	}
	if in.OpV != OpVersion {
		return nil, errField(CodeBadValue, "op_v", "must be 1")
	}
	if in.Iat, err = getInt(top, "iat"); err != nil {
		return nil, err
	}
	if in.Exp, err = getInt(top, "exp"); err != nil {
		return nil, err
	}
	if in.Iat < 0 {
		return nil, errField(CodeBadValue, "iat", "must be non-negative Unix milliseconds")
	}
	if in.Exp <= in.Iat {
		return nil, errField(CodeBadValue, "exp", "must be greater than iat")
	}
	if in.Exp-in.Iat > MaxTTLMillis {
		return nil, errField(CodeBadValue, "exp", "exp - iat exceeds 300000 ms")
	}
	if a, present := top["agent"]; present {
		s, ok := a.(string)
		if !ok {
			return nil, errField(CodeWrongType, "agent", "must be a string")
		}
		in.Agent = &s
	}

	args, ok := top["args"].(map[string]any)
	if !ok {
		return nil, errField(CodeWrongType, "args", "must be an object")
	}
	if err := closedKeys(args, "args", RequiredArgsKeys, nil); err != nil {
		return nil, err
	}
	if in.Args.Amount, err = getString(args, "amount", "args.amount", reAmount); err != nil {
		return nil, err
	}
	if in.Args.Currency, err = getString(args, "currency", "args.currency", reCurrency); err != nil {
		return nil, err
	}
	if in.Args.From, err = getString(args, "from", "args.from", reAccount); err != nil {
		return nil, err
	}
	if in.Args.To, err = getString(args, "to", "args.to", reAccount); err != nil {
		return nil, err
	}
	return in, nil
}

// closedKeys enforces that obj has every required key and nothing outside
// required+optional. Errors are reported in sorted key order so the result
// does not depend on map iteration.
func closedKeys(obj map[string]any, path string, required, optional []string) error {
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
	}
	for _, k := range optional {
		allowed[k] = true
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return errField(CodeUnknownKey, joinPath(path, k), "key not allowed in "+Type)
		}
	}
	for _, k := range required {
		if _, ok := obj[k]; !ok {
			return errField(CodeMissingKey, joinPath(path, k), "required key missing")
		}
	}
	return nil
}

func getString(obj map[string]any, key, path string, re *regexp.Regexp) (string, error) {
	s, ok := obj[key].(string)
	if !ok {
		return "", errField(CodeWrongType, path, "must be a string")
	}
	if re != nil && !re.MatchString(s) {
		return "", errField(CodeBadValue, path, "does not match "+re.String())
	}
	return s, nil
}

func getInt(obj map[string]any, key string) (int64, error) {
	n, ok := obj[key].(int64)
	if !ok {
		return 0, errField(CodeWrongType, key, "must be an integer")
	}
	return n, nil
}

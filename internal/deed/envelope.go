package deed

import (
	"encoding/json"
	"fmt"

	"github.com/deedglass/deedglass/internal/intent"
)

// ObjectDeed is the envelope's "object" value.
const ObjectDeed = "deed"

// MaxInputBytes bounds one deed as a verifier reads it (one line of a .jsonl export, or one file).
// It is larger than MaxEnvelopeBytes so a pretty-printed deed still parses; the canonical form must
// still fit in MaxEnvelopeBytes.
const MaxInputBytes = 64 << 10

// Envelope is what is stored, exported and served. Its canonical bytes are JCS(Envelope).
//
// A v2 envelope (ADR-109) has seals instead of seal and seal_key_id; Seal and SealKeyID then hold
// the primary seal (Seals[0]) for code that reads one seal, and are not serialized.
type Envelope struct {
	Object    string      `json:"object"`          // "deed"
	ID        string      `json:"id"`              // = Body.DeedID (unsigned convenience; checked equal)
	Body      Body        `json:"body"`            //
	BodyHash  string      `json:"body_hash"`       // sha256:<hex of sha256(JCS(body))>
	Seal      string      `json:"seal"`            // ed25519:<base64url, no padding, 64 bytes>
	SealKeyID string      `json:"seal_key_id"`     // sk_…
	Seals     []SealValue `json:"seals,omitempty"` // v2 only: one per Body.SealKeys entry, same order
}

// SealValue is one seal of a v2 deed: its algorithm, the seal string and the key that made it.
type SealValue struct {
	Alg       string `json:"alg"`
	Seal      string `json:"seal"`
	SealKeyID string `json:"seal_key_id"`
}

// MarshalCanonical returns JCS(envelope): the stored, exported and served bytes.
func (e *Envelope) MarshalCanonical() ([]byte, error) { return StrictCanonical(e) }

// MarshalJSON writes a v2 envelope in its own shape (seals, no seal or seal_key_id), canonical. A
// v1 envelope marshals field by field as always.
func (e Envelope) MarshalJSON() ([]byte, error) {
	if e.Body.V == VersionV2 {
		bodyJCS, err := canonicalBody(&e.Body)
		if err != nil {
			return nil, err
		}
		return canonicalEnvelope(&e, bodyJCS), nil
	}
	type plain Envelope
	return json.Marshal(plain(e))
}

// maxEnvelopeBytes is the canonical size limit of a deed of version v.
func maxEnvelopeBytes(v int64) int {
	if v == VersionV2 {
		return MaxEnvelopeBytesV2
	}
	return MaxEnvelopeBytes
}

// parsed is an envelope read from bytes, with the canonical body bytes a verifier hashes.
type parsed struct {
	env     *Envelope
	bodyJCS []byte
	envJCS  []byte
}

// ParseEnvelope reads a deed envelope under the strict profile and the closed schema, in the
// verifier's order: size and strict JSON (malformed), object, id and version, then the schema.
// It does not check the hash or the seal; VerifyEnvelope does.
func ParseEnvelope(raw []byte) (*Envelope, error) {
	p, err := parseEnvelope(raw)
	if err != nil {
		return nil, err
	}
	return p.env, nil
}

func parseEnvelope(raw []byte) (*parsed, *VerifyError) {
	if len(raw) > MaxInputBytes {
		return nil, errMalformed(fmt.Sprintf("larger than %d KiB", MaxInputBytes>>10))
	}
	tree, err := intent.ParseStrictNonNegative(raw)
	if err != nil {
		return nil, malformedFrom(err)
	}
	top, ok := tree.(map[string]any)
	if !ok {
		return nil, errMalformed("a deed must be a JSON object")
	}
	o := obj{m: top}
	// object, id and version come first (DEED_SPEC.md §11 steps 2–4).
	if s, _ := top["object"].(string); s != ObjectDeed {
		return nil, errSchemaField("object", `must be "deed"`)
	}
	bodyTree, _ := top["body"].(map[string]any)
	if id, ok := top["id"].(string); ok && bodyTree != nil {
		if did, ok := bodyTree["deed_id"].(string); ok && did != id {
			return nil, withRefs(&VerifyError{Code: CodeIDMismatch, Detail: "envelope id " + id + ", body deed_id " + did}, bodyTree)
		}
	}
	var ver int64 = Version
	if bodyTree != nil {
		if n, ok := bodyTree["v"].(int64); ok {
			if n != Version && n != VersionV2 {
				return nil, withRefs(errUnsupported(n), bodyTree)
			}
			ver = n
		}
	}
	keys := envelopeKeys
	if ver == VersionV2 {
		keys = envelopeKeysV2
	}
	if err := o.closed(keys); err != nil {
		return nil, withRefs(err, bodyTree)
	}
	d := &decoder{}
	env := &Envelope{Object: ObjectDeed, ID: d.s(o, "id"), BodyHash: d.s(o, "body_hash")}
	if ver == VersionV2 {
		if d.err == nil {
			env.Seals, d.err = sealValues(top["seals"])
		}
	} else {
		env.Seal, env.SealKeyID = d.s(o, "seal"), d.s(o, "seal_key_id")
	}
	if d.err != nil {
		return nil, withRefs(d.err, bodyTree)
	}
	body, verr := bodyFromTree(top["body"])
	if verr != nil {
		if verr.Code == CodeSchemaInvalid && verr.Field != "" && verr.Field != "body" {
			verr.Field = "body." + verr.Field
		}
		return nil, withRefs(verr, bodyTree)
	}
	env.Body = *body
	if err := body.Validate(); err != nil {
		ve := err.(*VerifyError)
		if ve.Code == CodeSchemaInvalid {
			ve.Field = "body." + ve.Field
		}
		return nil, withRefs(ve, bodyTree)
	}
	if !reDigest.MatchString(env.BodyHash) {
		return nil, withRefs(errSchemaField("body_hash", "must be sha256:<64 hex>"), bodyTree)
	}
	if ver == VersionV2 {
		env.Seal, env.SealKeyID = env.Seals[0].Seal, env.Seals[0].SealKeyID
	} else {
		if !reSeal.MatchString(env.Seal) {
			return nil, withRefs(errSchemaField("seal", "must be ed25519:<86 base64url characters>"), bodyTree)
		}
		if !isID("sk", env.SealKeyID) {
			return nil, withRefs(errSchemaField("seal_key_id", "must be an sk_ id"), bodyTree)
		}
	}
	// The body is hashed from the parsed tree, so whitespace and key order in transit don't matter
	// while every value does.
	c, verr := canonicalTree(top["body"])
	if verr != nil {
		return nil, withRefs(verr, bodyTree)
	}
	p := &parsed{env: env, bodyJCS: c}
	p.envJCS = canonicalEnvelope(env, c)
	if limit := maxEnvelopeBytes(ver); len(p.envJCS) > limit {
		return nil, withRefs(errMalformed(fmt.Sprintf("its canonical form is larger than %d KiB", limit>>10)), bodyTree)
	}
	return p, nil
}

// canonicalEnvelope assembles JCS(envelope) from JCS(body). Every other member is an ASCII string
// the schema has already checked (no escaping), and RFC 8785 orders the members
// body, body_hash, id, object, seal, seal_key_id. A test keeps it equal to StrictCanonical.
//
// A v2 envelope orders body, body_hash, id, object, seals; each seal is alg, seal, seal_key_id.
func canonicalEnvelope(e *Envelope, bodyJCS []byte) []byte {
	b := make([]byte, 0, len(bodyJCS)+320)
	b = append(b, `{"body":`...)
	b = append(b, bodyJCS...)
	b = append(b, `,"body_hash":"`...)
	b = append(b, e.BodyHash...)
	b = append(b, `","id":"`...)
	b = append(b, e.ID...)
	if e.Body.V == VersionV2 {
		b = append(b, `","object":"deed","seals":[`...)
		for i, s := range e.Seals {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"alg":"`...)
			b = append(b, s.Alg...)
			b = append(b, `","seal":"`...)
			b = append(b, s.Seal...)
			b = append(b, `","seal_key_id":"`...)
			b = append(b, s.SealKeyID...)
			b = append(b, `"}`...)
		}
		return append(b, `]}`...)
	}
	b = append(b, `","object":"deed","seal":"`...)
	b = append(b, e.Seal...)
	b = append(b, `","seal_key_id":"`...)
	b = append(b, e.SealKeyID...)
	return append(b, `"}`...)
}

// withRefs copies the deed's seq and id into e when the body tree has them, so a message can say
// "Deed 812" even when the deed is otherwise unreadable.
func withRefs(e *VerifyError, body map[string]any) *VerifyError {
	if body == nil {
		return e
	}
	if n, ok := body["seq"].(int64); ok && n > 0 && e.Seq == 0 {
		e.Seq = n
	}
	if s, ok := body["deed_id"].(string); ok && e.DeedID == "" && isID("deed", s) {
		e.DeedID = s
	}
	return e
}

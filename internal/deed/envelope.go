package deed

import (
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
type Envelope struct {
	Object    string `json:"object"`      // "deed"
	ID        string `json:"id"`          // = Body.DeedID (unsigned convenience; checked equal)
	Body      Body   `json:"body"`        //
	BodyHash  string `json:"body_hash"`   // sha256:<hex of sha256(JCS(body))>
	Seal      string `json:"seal"`        // ed25519:<base64url, no padding, 64 bytes>
	SealKeyID string `json:"seal_key_id"` // sk_…
}

// MarshalCanonical returns JCS(envelope): the stored, exported and served bytes.
func (e *Envelope) MarshalCanonical() ([]byte, error) { return StrictCanonical(e) }

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
	if bodyTree != nil {
		if n, ok := bodyTree["v"].(int64); ok && n != Version {
			return nil, withRefs(errUnsupported(n), bodyTree)
		}
	}
	if err := o.closed(envelopeKeys); err != nil {
		return nil, withRefs(err, bodyTree)
	}
	d := &decoder{}
	env := &Envelope{Object: ObjectDeed, ID: d.s(o, "id"), BodyHash: d.s(o, "body_hash"), Seal: d.s(o, "seal"), SealKeyID: d.s(o, "seal_key_id")}
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
	if !reSeal.MatchString(env.Seal) {
		return nil, withRefs(errSchemaField("seal", "must be ed25519:<86 base64url characters>"), bodyTree)
	}
	if !isID("sk", env.SealKeyID) {
		return nil, withRefs(errSchemaField("seal_key_id", "must be an sk_ id"), bodyTree)
	}
	// The body is hashed from the parsed tree, so whitespace and key order in transit don't matter
	// while every value does.
	c, verr := canonicalTree(top["body"])
	if verr != nil {
		return nil, withRefs(verr, bodyTree)
	}
	p := &parsed{env: env, bodyJCS: c}
	p.envJCS = canonicalEnvelope(env, c)
	if len(p.envJCS) > MaxEnvelopeBytes {
		return nil, withRefs(errMalformed(fmt.Sprintf("its canonical form is larger than %d KiB", MaxEnvelopeBytes>>10)), bodyTree)
	}
	return p, nil
}

// canonicalEnvelope assembles JCS(envelope) from JCS(body). Every other member is an ASCII string
// the schema has already checked (no escaping), and RFC 8785 orders the members
// body, body_hash, id, object, seal, seal_key_id. A test keeps it equal to StrictCanonical.
func canonicalEnvelope(e *Envelope, bodyJCS []byte) []byte {
	b := make([]byte, 0, len(bodyJCS)+320)
	b = append(b, `{"body":`...)
	b = append(b, bodyJCS...)
	b = append(b, `,"body_hash":"`...)
	b = append(b, e.BodyHash...)
	b = append(b, `","id":"`...)
	b = append(b, e.ID...)
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

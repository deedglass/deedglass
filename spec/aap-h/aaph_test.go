// Package aaph_test checks the AAP-H v0.1 draft (spec/aap-h): the examples against the JSON Schemas,
// the FHIR conformance resources and views for internal consistency, the signed examples and their
// chains (SPEC.md sections 8 and 9), and that Deedglass's own records convert to the envelope and stay
// consistent with the sealed deed (SPEC.md section 11).
//
//	go test ./spec/aap-h/                 # check
//	go test ./spec/aap-h/ -update         # re-sign the aaph-jcs examples, rewrite keys.json and the Deedglass example
//	python3 spec/aap-h/tools/envelope2fhir.py …  # then regenerate the FHIR views of the signed examples
package aaph_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/google/jsonschema-go/jsonschema"
)

var update = flag.Bool("update", false, "re-sign the aaph-jcs examples and rewrite keys.json and the Deedglass example")

const (
	fhirBase   = "https://deedglass.com/fhir/aap-h"
	sigPrefix  = "aap-h/record/v0.1/"
	genesisTag = "aap-h/genesis\n"
	testKeyID  = "aaph-test-1"
	testIssuer = "https://agent-gateway.example"
	deedIssuer = "https://deedglass.example" // testdata/deeds/keys/test-keys.json
)

// testKey is the public-seed test key of examples/keys.json. Never trust it.
func testKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("aap-h/test-key/1"))
	return ed25519.NewKeyFromSeed(seed[:])
}

// --- envelope model (SPEC.md section 6) --------------------------------------------------------

type Envelope struct {
	AAPH          string         `json:"aaph"`
	RecordID      string         `json:"record_id"`
	Issuer        string         `json:"issuer"`
	RecordedAt    string         `json:"recorded_at"`
	Agent         Agent          `json:"agent"`
	Action        Action         `json:"action"`
	Authorization []Authz        `json:"authorization"`
	Policy        *Policy        `json:"policy"`
	Approvals     []Approval     `json:"approvals"`
	Claimed       *Claimed       `json:"claimed"`
	Verified      *Verified      `json:"verified"`
	Supersedes    *string        `json:"supersedes"`
	Chain         *Chain         `json:"chain"`
	Seal          *Seal          `json:"seal"`
	Extensions    map[string]any `json:"extensions"`
}

type Agent struct {
	Kind       string  `json:"kind"`
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Model      *Model  `json:"model"`
	OnBehalfOf *string `json:"on_behalf_of"`
	Session    *string `json:"session"`
}

type Model struct {
	Provider string  `json:"provider"`
	Name     string  `json:"name"`
	Version  *string `json:"version"`
}

type Action struct {
	ID            string   `json:"id"`
	Tool          string   `json:"tool"`
	Interaction   string   `json:"interaction"`
	AgentRole     string   `json:"agent_role"`
	System        System   `json:"system"`
	Target        []Target `json:"target"`
	RequestDigest string   `json:"request_digest"`
	IntentDigest  *string  `json:"intent_digest"`
}

type System struct {
	Kind string  `json:"kind"`
	Base *string `json:"base"`
}

type Target struct {
	Ref     string  `json:"ref"`
	Version *string `json:"version"`
}

type Authz struct {
	Kind   string  `json:"kind"`
	Ref    string  `json:"ref"`
	Scope  *string `json:"scope"`
	Digest *string `json:"digest"`
}

type Policy struct {
	Decision      string  `json:"decision"`
	Mode          string  `json:"mode"`
	Policy        *string `json:"policy"`
	VersionDigest *string `json:"version_digest"`
	Rule          *string `json:"rule"`
}

type Approval struct {
	Step      int64    `json:"step"`
	Outcome   string   `json:"outcome"`
	Approvers []string `json:"approvers"`
	Role      *string  `json:"role"`
	DecidedAt *string  `json:"decided_at"`
}

type Claimed struct {
	Status       string  `json:"status"`
	Assertion    *string `json:"assertion"`
	ResultDigest *string `json:"result_digest"`
	ErrorCode    *string `json:"error_code"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
}

type Verified struct {
	Result       string  `json:"result"`
	Depth        string  `json:"depth"`
	Method       string  `json:"method"`
	DetailDigest *string `json:"detail_digest"`
	CheckedAt    *string `json:"checked_at"`
}

type Chain struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
	Prev   string `json:"prev"`
}

type Seal struct {
	Format       string          `json:"format"`
	Alg          string          `json:"alg,omitempty"`
	KeyID        string          `json:"key_id,omitempty"`
	KeySet       string          `json:"key_set,omitempty"`
	RecordDigest string          `json:"record_digest"`
	Signature    string          `json:"signature,omitempty"`
	Native       json.RawMessage `json:"native,omitempty"`
}

var writes = []string{"create", "update", "patch", "delete", "operation", "execute"}

// checkRules is SPEC.md section 4's rules that a schema can't express.
func checkRules(e *Envelope) []string {
	var bad []string
	isWrite := slices.Contains(writes, e.Action.Interaction)
	if e.Policy == nil && e.Supersedes == nil {
		bad = append(bad, "policy is null in a record that supersedes nothing")
	}
	if e.Policy != nil && e.Policy.Decision == "require-approval" && len(e.Approvals) == 0 {
		bad = append(bad, "require-approval without an approval step (aaph-1)")
	}
	if e.Policy != nil && e.Policy.Decision == "deny" && e.Claimed != nil {
		bad = append(bad, "a refused action has a claimed outcome")
	}
	if v := e.Verified; v != nil && isWrite && v.Result == "match" && v.Depth == "reported" {
		bad = append(bad, "a write recorded as match at depth reported (rule 3, aaph-2)")
	}
	if e.Claimed != nil && e.Claimed.Status == "succeeded" && isWrite && e.Verified == nil && e.Supersedes == nil {
		bad = append(bad, "a write the tool reported as succeeded has no verified outcome (aaph-3)")
	}
	for i, a := range e.Approvals {
		if a.Step != int64(i+1) {
			bad = append(bad, "approval steps are not numbered 1, 2, 3…")
		}
		if (a.Outcome == "approved" || a.Outcome == "denied") && len(a.Approvers) == 0 {
			bad = append(bad, "an approved or denied step names no approver")
		}
	}
	return bad
}

// --- helpers -----------------------------------------------------------------------------------

func readJSON(t *testing.T, path string, v any) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return b
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func schema(t *testing.T, name string) *jsonschema.Resolved {
	t.Helper()
	var s jsonschema.Schema
	readJSON(t, filepath.Join("schema", name), &s)
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return rs
}

func instance(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func glob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil || len(m) == 0 {
		t.Fatalf("no files match %s (%v)", pattern, err)
	}
	return m
}

func genesis(stream string) string { return deed.Digest([]byte(genesisTag + stream)) }

// recordDigest is SPEC.md section 8.2 step 2: the digest of JCS(envelope without seal), after the
// strict profile.
func recordDigest(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return "", err
	}
	delete(m, "seal")
	c, err := deed.StrictCanonical(m)
	if err != nil {
		return "", err
	}
	return deed.Digest(c), nil
}

func sigInput(alg, digest string) []byte {
	return []byte(sigPrefix + strings.ToLower(alg) + "\n" + digest)
}

// --- key set (SPEC.md section 8.2 step 5) -------------------------------------------------------

type keySet struct {
	Issuer string `json:"issuer"`
	Keys   []struct {
		ID          string  `json:"id"`
		Alg         string  `json:"alg"`
		PublicKey   string  `json:"public_key"`
		Status      string  `json:"status"`
		ActivatedAt string  `json:"activated_at"`
		RetiredAt   *string `json:"retired_at"`
		RevokedAt   *string `json:"revoked_at"`
	} `json:"keys"`
}

// verifyJCS runs SPEC.md section 9.1 steps 6–12 for an aaph-jcs envelope and returns the code of
// the first failure, or "".
func verifyJCS(raw []byte, e *Envelope, ks *keySet) string {
	d, err := recordDigest(raw)
	if err != nil {
		return "malformed"
	}
	if d != e.Seal.RecordDigest {
		return "digest_mismatch"
	}
	if e.Seal.Alg != "Ed25519" {
		return "unsupported_algorithm"
	}
	for _, k := range ks.Keys {
		if k.ID != e.Seal.KeyID {
			continue
		}
		if k.Alg != e.Seal.Alg {
			return "signature_invalid"
		}
		pub, err := base64.RawURLEncoding.DecodeString(k.PublicKey)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return "key_set_rejected"
		}
		if k.RevokedAt != nil || k.Status == "revoked" {
			return "key_revoked"
		}
		at, err1 := time.Parse(time.RFC3339Nano, e.RecordedAt)
		from, err2 := time.Parse(time.RFC3339Nano, k.ActivatedAt)
		if err1 != nil || err2 != nil || at.Before(from) {
			return "key_not_valid_at"
		}
		if k.RetiredAt != nil {
			if until, err := time.Parse(time.RFC3339Nano, *k.RetiredAt); err != nil || !at.Before(until) {
				return "key_not_valid_at"
			}
		}
		sig, ok := strings.CutPrefix(e.Seal.Signature, "ed25519:")
		if !ok {
			return "signature_invalid"
		}
		sb, err := base64.RawURLEncoding.DecodeString(sig)
		if err != nil || !ed25519.Verify(pub, sigInput(e.Seal.Alg, d), sb) {
			return "signature_invalid"
		}
		return ""
	}
	return "key_unknown"
}

// --- tests -------------------------------------------------------------------------------------

func TestEnvelopeExamples(t *testing.T) {
	rs := schema(t, "aaph-envelope.schema.json")
	for _, f := range glob(t, "examples/*.envelope.json") {
		b := readJSON(t, f, nil)
		if err := rs.Validate(instance(t, b)); err != nil {
			t.Errorf("%s: schema: %v", f, err)
			continue
		}
		var e Envelope
		readJSON(t, f, &e)
		for _, r := range checkRules(&e) {
			t.Errorf("%s: %s", f, r)
		}
	}
}

func TestInvalidExamples(t *testing.T) {
	rs := schema(t, "aaph-envelope.schema.json")
	for _, f := range glob(t, "examples/invalid/*.json") {
		if err := rs.Validate(instance(t, readJSON(t, f, nil))); err == nil {
			t.Errorf("%s: the schema accepted an envelope it must refuse", f)
		}
	}
}

func TestKeySet(t *testing.T) {
	pub := testKey().Public().(ed25519.PublicKey)
	want := map[string]any{
		"issuer": testIssuer,
		"note":   "TEST KEY ONLY. Its seed is public: SHA-256 of the ASCII string aap-h/test-key/1. Never trust a record it signs.",
		"keys": []any{map[string]any{
			"id": testKeyID, "alg": "Ed25519", "public_key": base64.RawURLEncoding.EncodeToString(pub),
			"status": "active", "activated_at": "2026-01-01T00:00:00Z", "retired_at": nil, "revoked_at": nil,
		}},
	}
	if *update {
		writeJSON(t, "examples/keys.json", want)
	}
	var got map[string]any
	readJSON(t, "examples/keys.json", &got)
	wb, _ := json.Marshal(want)
	gb, _ := json.Marshal(got)
	if !bytes.Equal(wb, gb) {
		t.Errorf("examples/keys.json differs from the key derived from the public test seed; run with -update")
	}
}

func TestSignedExamples(t *testing.T) {
	var ks keySet
	readJSON(t, "examples/keys.json", &ks)
	n := 0
	for _, f := range glob(t, "examples/*.envelope.json") {
		var e Envelope
		raw := readJSON(t, f, &e)
		if e.Seal == nil || e.Seal.Format != "aaph-jcs" {
			continue
		}
		n++
		if *update {
			var m map[string]any
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&m); err != nil {
				t.Fatal(err)
			}
			if e.Chain.Seq != 1 {
				t.Fatalf("%s: -update signs only first records of a stream", f)
			}
			m["chain"].(map[string]any)["prev"] = genesis(e.Chain.Stream)
			delete(m, "seal")
			c, err := deed.StrictCanonical(m)
			if err != nil {
				t.Fatal(err)
			}
			d := deed.Digest(c)
			sig := ed25519.Sign(testKey(), sigInput("Ed25519", d))
			m["seal"] = map[string]any{"format": "aaph-jcs", "alg": "Ed25519", "key_id": testKeyID,
				"key_set": testIssuer + "/.well-known/aaph/keys.json", "record_digest": d,
				"signature": "ed25519:" + base64.RawURLEncoding.EncodeToString(sig)}
			writeJSON(t, f, orderLike(raw, m))
			raw = readJSON(t, f, &e)
		}
		if code := verifyJCS(raw, &e, &ks); code != "" {
			t.Errorf("%s: %s", f, code)
		}
		if e.Chain == nil || e.Chain.Seq != 1 || e.Chain.Prev != genesis(e.Chain.Stream) {
			t.Errorf("%s: chain_genesis_mismatch", f)
		}
		// Tampering with any member covered by the seal is detected.
		for _, mut := range []struct{ from, to string }{
			{`"result": "`, `"result": "x`},
			{`"approvers": [`, `"approvers": ["usr_ref:someone-else", `},
			{`"recorded_at": "2026`, `"recorded_at": "2027`},
		} {
			if !bytes.Contains(raw, []byte(mut.from)) {
				continue
			}
			bad := bytes.Replace(raw, []byte(mut.from), []byte(mut.to), 1)
			var be Envelope
			if err := json.Unmarshal(bad, &be); err != nil {
				t.Fatal(err)
			}
			if code := verifyJCS(bad, &be, &ks); code == "" {
				t.Errorf("%s: a change (%s) still verified", f, mut.to)
			}
		}
	}
	if n < 2 {
		t.Errorf("want at least 2 aaph-jcs examples, have %d", n)
	}
}

// orderLike keeps the member order of the original file when rewriting it (readability only; the
// digest is over JCS).
func orderLike(orig []byte, m map[string]any) any { return ordered{m: m, keys: topKeys(orig)} }

type ordered struct {
	m    map[string]any
	keys []string
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	seen := map[string]bool{}
	first := true
	emit := func(k string) error {
		v, ok := o.m[k]
		if !ok || seen[k] {
			return nil
		}
		seen[k] = true
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
		return nil
	}
	for _, k := range o.keys {
		if err := emit(k); err != nil {
			return nil, err
		}
	}
	rest := make([]string, 0, len(o.m))
	for k := range o.m {
		rest = append(rest, k)
	}
	slices.Sort(rest)
	for _, k := range rest {
		if err := emit(k); err != nil {
			return nil, err
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// topKeys lists the top-level member names of a JSON object in file order.
func topKeys(b []byte) []string {
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var keys []string
	if _, err := dec.Token(); err != nil {
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		k, _ := tok.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// --- FHIR -------------------------------------------------------------------------------------

type conformance struct {
	ResourceType string `json:"resourceType"`
	ID           string `json:"id"`
	URL          string `json:"url"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	FHIRVersion  string `json:"fhirVersion"`
	Type         string `json:"type"`
	Derivation   string `json:"derivation"`
	BaseDef      string `json:"baseDefinition"`
	ValueSet     string `json:"valueSet"`
	Count        int    `json:"count"`
	Concept      []struct {
		Code string `json:"code"`
	} `json:"concept"`
	Compose struct {
		Include []struct {
			System string `json:"system"`
		} `json:"include"`
	} `json:"compose"`
	Differential struct {
		Element []struct {
			ID        string `json:"id"`
			Path      string `json:"path"`
			SliceName string `json:"sliceName"`
			FixedURI  string `json:"fixedUri"`
			Binding   *struct {
				ValueSet string `json:"valueSet"`
			} `json:"binding"`
			Type []struct {
				Code    string   `json:"code"`
				Profile []string `json:"profile"`
			} `json:"type"`
		} `json:"element"`
	} `json:"differential"`
}

type fhirSet struct {
	byURL   map[string]conformance
	subs    map[string]map[string]string // extension url → sub url → bound value set ("" when unbound)
	codes   map[string][]string          // value set url → codes
	profile map[string]conformance
}

func loadConformance(t *testing.T) fhirSet {
	t.Helper()
	fs := fhirSet{byURL: map[string]conformance{}, subs: map[string]map[string]string{}, codes: map[string][]string{}, profile: map[string]conformance{}}
	systems := map[string][]string{}
	for _, f := range glob(t, "fhir/*.json") {
		var c conformance
		readJSON(t, f, &c)
		if want := c.ResourceType + "-" + c.ID + ".json"; filepath.Base(f) != want {
			t.Errorf("%s: file name should be %s", f, want)
		}
		if !strings.HasPrefix(c.URL, fhirBase+"/"+c.ResourceType+"/") || !strings.HasSuffix(c.URL, "/"+c.ID) {
			t.Errorf("%s: url %s is not %s/%s/%s", f, c.URL, fhirBase, c.ResourceType, c.ID)
		}
		if c.Version != "0.1.0-draft" || c.Status != "draft" {
			t.Errorf("%s: version %q status %q", f, c.Version, c.Status)
		}
		if _, dup := fs.byURL[c.URL]; dup {
			t.Errorf("%s: duplicate url", f)
		}
		fs.byURL[c.URL] = c
		switch c.ResourceType {
		case "CodeSystem":
			if c.Count != len(c.Concept) || len(c.Concept) == 0 {
				t.Errorf("%s: count %d, %d concepts", f, c.Count, len(c.Concept))
			}
			for _, k := range c.Concept {
				systems[c.URL] = append(systems[c.URL], k.Code)
			}
		case "StructureDefinition":
			if c.FHIRVersion != "4.0.1" || c.Derivation != "constraint" || !strings.HasPrefix(c.BaseDef, "http://hl7.org/fhir/StructureDefinition/") {
				t.Errorf("%s: fhirVersion %q derivation %q base %q", f, c.FHIRVersion, c.Derivation, c.BaseDef)
			}
			for _, el := range c.Differential.Element {
				if el.ID != c.Type && !strings.HasPrefix(el.ID, c.Type+".") {
					t.Errorf("%s: element %s is not under %s", f, el.ID, c.Type)
				}
			}
			if c.Type == "Extension" {
				subs := map[string]string{}
				for _, el := range c.Differential.Element {
					if el.Path == "Extension.extension.url" {
						subs[el.FixedURI] = ""
					}
					if el.Path == "Extension.extension.value[x]" && el.Binding != nil {
						name := strings.TrimSuffix(strings.TrimPrefix(el.ID, "Extension.extension:"), ".value[x]")
						subs[name] = el.Binding.ValueSet
					}
				}
				fs.subs[c.URL] = subs
			} else {
				fs.profile[c.URL] = c
			}
		}
	}
	for _, c := range fs.byURL {
		if c.ResourceType != "ValueSet" {
			continue
		}
		for _, inc := range c.Compose.Include {
			codes, ok := systems[inc.System]
			if !ok {
				t.Errorf("value set %s includes unknown system %s", c.URL, inc.System)
			}
			fs.codes[c.URL] = append(fs.codes[c.URL], codes...)
		}
	}
	// Every binding, and every extension a profile names, resolves inside the package.
	for url, subs := range fs.subs {
		for sub, vs := range subs {
			if _, ok := fs.codes[vs]; vs != "" && !ok {
				t.Errorf("%s: sub-extension %s binds unknown value set %s", url, sub, vs)
			}
		}
	}
	for url, p := range fs.profile {
		n := 0
		for _, el := range p.Differential.Element {
			for _, ty := range el.Type {
				for _, pr := range ty.Profile {
					n++
					if _, ok := fs.subs[pr]; !ok {
						t.Errorf("%s: element %s names unknown extension %s", url, el.ID, pr)
					}
				}
			}
		}
		if n != 7 {
			t.Errorf("%s: names %d AAP-H extensions, want 7", url, n)
		}
	}
	if len(fs.profile) != 2 || len(fs.subs) != 7 {
		t.Errorf("want 2 profiles and 7 extensions, have %d and %d", len(fs.profile), len(fs.subs))
	}
	return fs
}

func TestFHIRConformanceResources(t *testing.T) { loadConformance(t) }

type fhirExt struct {
	URL       string    `json:"url"`
	Extension []fhirExt `json:"extension"`
	ValueCode *string   `json:"valueCode"`
	ValueStr  *string   `json:"valueString"`
	ValueURI  *string   `json:"valueUri"`
}

func (x fhirExt) sub(name string) string {
	for _, s := range x.Extension {
		if s.URL == name {
			switch {
			case s.ValueCode != nil:
				return *s.ValueCode
			case s.ValueStr != nil:
				return *s.ValueStr
			case s.ValueURI != nil:
				return *s.ValueURI
			}
		}
	}
	return ""
}

func TestFHIRExamples(t *testing.T) {
	fs := loadConformance(t)
	rs := schema(t, "aaph-fhir.schema.json")
	for _, f := range glob(t, "examples/fhir/*.json") {
		b := readJSON(t, f, nil)
		if err := rs.Validate(instance(t, b)); err != nil {
			t.Errorf("%s: schema: %v", f, err)
		}
		var res struct {
			ResourceType string `json:"resourceType"`
			ID           string `json:"id"`
			Meta         struct {
				Profile []string `json:"profile"`
			} `json:"meta"`
			Extension []fhirExt `json:"extension"`
			Signature []struct {
				Data string `json:"data"`
			} `json:"signature"`
		}
		readJSON(t, f, &res)
		// Every extension and sub-extension is defined, and every bound code is in its value set.
		ext := map[string]fhirExt{}
		for _, x := range res.Extension {
			subs, ok := fs.subs[x.URL]
			if !ok {
				t.Errorf("%s: extension %s is not defined in fhir/", f, x.URL)
				continue
			}
			ext[strings.TrimPrefix(x.URL, fhirBase+"/StructureDefinition/")] = x
			for _, s := range x.Extension {
				vs, ok := subs[s.URL]
				if !ok {
					t.Errorf("%s: %s has an unknown sub-extension %s", f, x.URL, s.URL)
					continue
				}
				if vs != "" && s.ValueCode != nil && !slices.Contains(fs.codes[vs], *s.ValueCode) {
					t.Errorf("%s: %s.%s = %q is not in %s", f, x.URL, s.URL, *s.ValueCode, vs)
				}
			}
		}
		// The view says what its envelope says (SPEC.md section 9.3).
		var e Envelope
		raw := readJSON(t, filepath.Join("examples", res.ID+".envelope.json"), &e)
		check := func(what, got, want string) {
			if got != want {
				t.Errorf("%s: %s is %q, the envelope says %q (view_mismatch)", f, what, got, want)
			}
		}
		check("action id", ext["aaph-action"].sub("actionId"), e.Action.ID)
		check("interaction", ext["aaph-action"].sub("interaction"), e.Action.Interaction)
		check("request digest", ext["aaph-action"].sub("requestDigest"), e.Action.RequestDigest)
		check("decision", ext["aaph-policy-decision"].sub("decision"), e.Policy.Decision)
		if e.Claimed != nil {
			check("claimed status", ext["aaph-claimed-outcome"].sub("status"), e.Claimed.Status)
		}
		if e.Verified != nil {
			check("verified result", ext["aaph-verified-outcome"].sub("result"), e.Verified.Result)
			check("verified depth", ext["aaph-verified-outcome"].sub("depth"), e.Verified.Depth)
		}
		if e.Seal != nil {
			check("record id", ext["aaph-record-seal"].sub("recordId"), e.RecordID)
			check("record digest", ext["aaph-record-seal"].sub("recordDigest"), e.Seal.RecordDigest)
			if d, err := recordDigest(raw); err != nil || d != e.Seal.RecordDigest {
				t.Errorf("%s: the envelope's record digest is stale", f)
			}
		} else if _, ok := ext["aaph-record-seal"]; ok {
			t.Errorf("%s: a view of an unsigned envelope carries aaph-record-seal", f)
		}
		if res.ResourceType == "Provenance" && e.Seal != nil {
			sig, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(e.Seal.Signature, "ed25519:"))
			if len(res.Signature) != 1 || res.Signature[0].Data != base64.StdEncoding.EncodeToString(sig) {
				t.Errorf("%s: Provenance.signature.data is not the envelope's signature", f)
			}
		}
	}
}

// --- Deedglass records (SPEC.md section 11) ------------------------------------------------------

var (
	verdict = map[string]string{"VERIFIED": "match", "MISMATCH": "mismatch", "UNVERIFIABLE": "cannot-check"}
	depth   = map[string]string{"L0": "reported", "L1": "read-back", "L2": "change-log"}
)

func interaction(tool string) string {
	last := tool[strings.LastIndexByte(tool, '.')+1:]
	switch last {
	case "create", "read", "update", "patch", "delete", "search", "execute":
		return last
	}
	return "other"
}

// fromDeed converts a Deedglass action or amendment deed to an AAP-H envelope (SPEC.md section 11).
// ok is false for admin deeds, which aren't agent actions.
func fromDeed(raw []byte, issuer string) (*Envelope, bool, error) {
	env, err := deed.ParseEnvelope(raw)
	if err != nil {
		return nil, false, err
	}
	b := env.Body
	if b.Kind == deed.KindAdmin {
		return nil, false, nil
	}
	e := &Envelope{
		AAPH: "0.1", RecordID: b.DeedID, Issuer: issuer, RecordedAt: b.IssuedAt,
		Agent:         Agent{Kind: "ai-agent", ID: b.Agent.ID, Name: b.Agent.Name, OnBehalfOf: b.OnBehalfOf, Session: b.Session},
		Authorization: []Authz{}, Approvals: []Approval{}, Supersedes: b.Amends, Extensions: map[string]any{},
		Chain: &Chain{Stream: b.Org, Seq: b.Seq, Prev: b.PrevHash},
	}
	sys := "other"
	switch {
	case strings.HasPrefix(b.Action.Tool, "postgres."):
		sys = "postgres"
	case strings.HasPrefix(b.Action.Tool, "fhir."):
		sys = "fhir"
	}
	role := "proposed"
	if b.Execution != nil || b.Kind == deed.KindAmendment {
		role = "made"
	}
	e.Action = Action{ID: b.Action.ID, Tool: b.Action.Tool, Interaction: interaction(b.Action.Tool), AgentRole: role,
		System: System{Kind: sys}, Target: []Target{}, RequestDigest: b.Action.RequestDigest, IntentDigest: b.Action.IntentHash}
	if p := b.Policy; p != nil {
		e.Policy = &Policy{Decision: strings.ReplaceAll(p.Decision, "_", "-"), Mode: p.Mode, Policy: p.ID, VersionDigest: p.VersionHash, Rule: p.RuleID}
	}
	if a := b.Approval; a != nil {
		e.Approvals = []Approval{{Step: 1, Outcome: a.Outcome, Approvers: append([]string{}, a.DecidedBy...), DecidedAt: a.DecidedAt}}
	}
	if x := b.Execution; x != nil {
		e.Claimed = &Claimed{Status: x.Status, ResultDigest: x.ResultDigest, ErrorCode: x.ErrorCode, StartedAt: &x.StartedAt, FinishedAt: &x.FinishedAt}
	}
	if w := b.Witness; w != nil {
		e.Verified = &Verified{Result: verdict[w.Verdict], Depth: depth[w.Level], Method: w.Method, DetailDigest: w.DetailDigest}
		// Deedglass L0 VERIFIED means "the connector returned success": a claim, not a check (rule 3).
		if e.Verified.Result == "match" && e.Verified.Depth == "reported" && slices.Contains(writes, e.Action.Interaction) {
			e.Verified.Result = "cannot-check"
		}
	}
	var native bytes.Buffer
	if err := json.Compact(&native, raw); err != nil {
		return nil, false, err
	}
	e.Seal = &Seal{Format: "deedglass-deed", RecordDigest: env.BodyHash, Native: json.RawMessage(native.Bytes())}
	return e, true, nil
}

// checkNative is SPEC.md section 9.1 steps 6n–8n: the native deed verifies, its digest is the
// seal's, and every member the mapping derives equals the envelope's. Members the mapping doesn't
// derive are copied from the envelope: they are not covered by the seal.
func checkNative(e *Envelope, ks *deed.KeySet) string {
	if e.Seal == nil || e.Seal.Format != "deedglass-deed" {
		return "unsupported_format"
	}
	res, err := deed.VerifyEnvelope(e.Seal.Native, ks, deed.VerifyOptions{})
	if err != nil {
		return "native_invalid"
	}
	if res.BodyHash != e.Seal.RecordDigest {
		return "digest_mismatch"
	}
	d, ok, err := fromDeed(e.Seal.Native, e.Issuer)
	if err != nil || !ok {
		return "native_invalid"
	}
	d.Agent.Model = e.Agent.Model
	d.Action.System.Base = e.Action.System.Base
	d.Action.Target = e.Action.Target
	d.Authorization = e.Authorization
	d.Extensions = e.Extensions
	if d.Claimed != nil && e.Claimed != nil {
		d.Claimed.Assertion = e.Claimed.Assertion
	}
	if d.Verified != nil && e.Verified != nil {
		d.Verified.CheckedAt = e.Verified.CheckedAt
	}
	a, _ := json.Marshal(d)
	b, _ := json.Marshal(e)
	var am, bm any
	_ = json.Unmarshal(a, &am)
	_ = json.Unmarshal(b, &bm)
	if !reflect.DeepEqual(am, bm) {
		return "native_mismatch"
	}
	return ""
}

func TestDeedglassRecordsConvert(t *testing.T) {
	rs := schema(t, "aaph-envelope.schema.json")
	sets := []struct{ keys, glob string }{
		{"../../testdata/deeds/keys/test-keys.json", "../../testdata/deeds/valid/*.json"},
		{"../../testdata/deeds/v2/keys.json", "../../testdata/deeds/v2/valid/*.json"},
	}
	converted := 0
	for _, s := range sets {
		ks, err := deed.ParseKeySet(readJSON(t, s.keys, nil))
		if err != nil {
			t.Fatalf("%s: %v", s.keys, err)
		}
		var expected map[string]struct {
			Keys string `json:"keys"`
		}
		readJSON(t, filepath.Join(filepath.Dir(filepath.Dir(s.glob)), "expected.json"), &expected)
		for _, f := range glob(t, s.glob) {
			raw := readJSON(t, f, nil)
			ks := ks
			rel, _ := filepath.Rel("../../testdata/deeds", f)
			if k := expected[filepath.ToSlash(rel)].Keys; k != "" {
				if ks, err = deed.ParseKeySet(readJSON(t, filepath.Join("../../testdata/deeds", k), nil)); err != nil {
					t.Fatalf("%s: %v", k, err)
				}
			}
			e, ok, err := fromDeed(raw, deedIssuer)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if !ok {
				continue
			}
			converted++
			b, _ := json.Marshal(e)
			if err := rs.Validate(instance(t, b)); err != nil {
				t.Errorf("%s: converted envelope fails the schema: %v", f, err)
			}
			for _, r := range checkRules(e) {
				t.Errorf("%s: %s", f, r)
			}
			if code := checkNative(e, ks); code != "" {
				t.Errorf("%s: %s", f, code)
			}
			// A changed derived member is native_mismatch; context the seal doesn't cover isn't.
			bad := *e
			bad.Agent.Name += "x"
			if code := checkNative(&bad, ks); code != "native_mismatch" {
				t.Errorf("%s: a changed agent name gave %q, want native_mismatch", f, code)
			}
			ctx := *e
			ctx.Authorization = []Authz{{Kind: "smart-scope", Ref: "client:x"}}
			if code := checkNative(&ctx, ks); code != "" {
				t.Errorf("%s: uncovered context changed the result: %q", f, code)
			}
			// A changed native deed is native_invalid.
			nb := bytes.Replace(e.Seal.Native, []byte(e.Action.RequestDigest), []byte(strings.Repeat("0", 7)+e.Action.RequestDigest[7:]), 1)
			if !bytes.Equal(nb, e.Seal.Native) {
				tampered := *e
				s := *e.Seal
				s.Native = nb
				tampered.Seal = &s
				if code := checkNative(&tampered, ks); code != "native_invalid" {
					t.Errorf("%s: a changed native deed gave %q, want native_invalid", f, code)
				}
			}
			if strings.HasSuffix(f, "05-action-approved-then-executed.json") {
				const ex = "examples/deedglass-native.envelope.json"
				if *update {
					writeJSON(t, ex, e)
				}
				var got Envelope
				readJSON(t, ex, &got)
				g, _ := json.Marshal(got)
				if !bytes.Equal(g, b) {
					t.Errorf("%s is stale; run go test ./spec/aap-h/ -update", ex)
				}
			}
		}
	}
	if converted < 8 {
		t.Errorf("converted %d deeds, want at least 8", converted)
	}
}

// Package stamptest runs a fake RFC 3161 TSA and a fake transparency service on loopback, for
// tests (TY-03). Nothing here talks to a real TSA or service, and CI never uses the network. Test
// keys: these keys and the bytes they sign mean nothing outside tests.
package stamptest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"time"

	"github.com/veraison/go-cose"

	"github.com/deedglass/deedglass/internal/deed/stamp"
)

// TSA modes.
const (
	TSAOK           = ""
	TSARefuse       = "refuse"        // status 2 (rejection)
	TSAWrongImprint = "wrong-imprint" // a valid token over other data
	TSABadSignature = "bad-signature" // a token whose signature doesn't verify
	TSANoESS        = "no-ess"        // no ESS signing-cert attribute
	TSAGarbage      = "garbage"       // not DER
)

var (
	oidSignedData   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidTSTInfo      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	oidContentType  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidDigest       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningV2    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 47}
	oidSHA256       = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidECDSASHA256  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidPolicy       = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}
	fixedNotBefore  = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fixedNotAfter   = time.Date(2045, 1, 1, 0, 0, 0, 0, time.UTC)
	defaultTSAClock = time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
)

// TSA is a fake time-stamping authority.
type TSA struct {
	*httptest.Server
	Name    string
	RootDER []byte // the root's DER (stamp.EncodePEM for --tsa-roots)

	mu     sync.Mutex
	mode   string
	now    time.Time
	serial int64
	calls  int

	key     *ecdsa.PrivateKey
	leaf    *stamp.X509
	rootDER []byte
}

// NewTSA starts a fake TSA whose clock reads 2026-10-20 09:30:00 UTC.
func NewTSA(name string) *TSA {
	rootKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	root := &stamp.X509{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name + " test root"},
		NotBefore: fixedNotBefore, NotAfter: fixedNotAfter, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		panic(err)
	}
	rootParsed, _ := x509.ParseCertificate(rootDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafT := &stamp.X509{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: name + " test TSA"},
		NotBefore: fixedNotBefore, NotAfter: fixedNotAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping}, SubjectKeyId: []byte{1, 2, 3, 4}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafT, rootParsed, &key.PublicKey, rootKey)
	if err != nil {
		panic(err)
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	t := &TSA{Name: name, RootDER: rootDER, rootDER: rootDER, key: key, leaf: leaf, now: defaultTSAClock}
	t.Server = httptest.NewServer(http.HandlerFunc(t.serve))
	return t
}

// SetMode switches how the TSA answers.
func (t *TSA) SetMode(m string) { t.mu.Lock(); t.mode = m; t.mu.Unlock() }

// SetNow sets the TSA's clock.
func (t *TSA) SetNow(n time.Time) { t.mu.Lock(); t.now = n.UTC(); t.mu.Unlock() }

// Calls is how many requests the TSA has answered.
func (t *TSA) Calls() int { t.mu.Lock(); defer t.mu.Unlock(); return t.calls }

// RootPEM is the root as PEM.
func (t *TSA) RootPEM() []byte { return stamp.EncodePEM(t.rootDER) }

type messageImprint struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	HashedMessage []byte
}

type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	Nonce          *big.Int `asn1:"optional"`
	CertReq        bool     `asn1:"optional,default:false"`
}

type tstInfo struct {
	Version        int
	Policy         asn1.ObjectIdentifier
	MessageImprint messageImprint
	SerialNumber   *big.Int
	GenTime        time.Time `asn1:"generalized"`
	Nonce          *big.Int  `asn1:"optional"`
}

func (t *TSA) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	t.mu.Lock()
	mode, now := t.mode, t.now
	t.serial++
	serial := t.serial
	t.calls++
	t.mu.Unlock()
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/timestamp-query" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req timeStampReq
	if _, err := asn1.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/timestamp-reply")
	switch mode {
	case TSAGarbage:
		_, _ = w.Write([]byte("not a time-stamp response"))
		return
	case TSARefuse:
		st, _ := asn1.Marshal(2)
		_, _ = w.Write(tlv(0x30, tlv(0x30, st)))
		return
	}
	resp, err := t.Respond(req.MessageImprint.HashedMessage, req.Nonce, serial, now, mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(resp)
}

// Token returns a token over imprint at now, as Respond would put in a response.
func (t *TSA) Token(imprint []byte, now time.Time) []byte {
	t.mu.Lock()
	t.serial++
	s := t.serial
	t.mu.Unlock()
	resp, err := t.Respond(imprint, big.NewInt(s), s, now, TSAOK)
	if err != nil {
		panic(err)
	}
	tok, err := stamp.ParseResponse(resp)
	if err != nil {
		panic(err)
	}
	return tok
}

// Respond builds a DER TimeStampResp granting a token over imprint.
func (t *TSA) Respond(imprint []byte, nonce *big.Int, serial int64, now time.Time, mode string) ([]byte, error) {
	if mode == TSAWrongImprint {
		other := sha256.Sum256([]byte("other data"))
		imprint = other[:]
	}
	tst, err := asn1.Marshal(tstInfo{Version: 1, Policy: oidPolicy,
		MessageImprint: messageImprint{HashAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}, HashedMessage: imprint},
		SerialNumber:   big.NewInt(serial), GenTime: now.UTC(), Nonce: nonce})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(tst)
	ctAttr := attr(oidContentType, mustMarshal(oidTSTInfo))
	dAttr := attr(oidDigest, mustMarshal(digest[:]))
	leafHash := sha256.Sum256(t.leaf.Raw)
	essAttr := attr(oidSigningV2, tlv(0x30, tlv(0x30, tlv(0x30, mustMarshal(leafHash[:])))))
	attrs := [][]byte{ctAttr, dAttr}
	if mode != TSANoESS {
		attrs = append(attrs, essAttr)
	}
	sort.Slice(attrs, func(i, j int) bool { return bytes.Compare(attrs[i], attrs[j]) < 0 })
	signedSet := tlv(0x31, attrs...)
	h := sha256.Sum256(signedSet)
	sig, err := ecdsa.SignASN1(rand.Reader, t.key, h[:])
	if err != nil {
		return nil, err
	}
	if mode == TSABadSignature {
		sig[len(sig)-1] ^= 0x01
	}
	sid := tlv(0x30, t.leaf.RawIssuer, mustMarshal(t.leaf.SerialNumber))
	signerInfo := tlv(0x30, mustMarshal(1), sid, algID(oidSHA256),
		append([]byte{0xa0}, signedSet[1:]...), algID(oidECDSASHA256), mustMarshal(sig))
	eci := tlv(0x30, mustMarshal(oidTSTInfo), tlv(0xa0, mustMarshal(tst)))
	signedData := tlv(0x30, mustMarshal(3), tlv(0x31, algID(oidSHA256)), eci,
		tlv(0xa0, t.leaf.Raw, t.rootDER), tlv(0x31, signerInfo))
	contentInfo := tlv(0x30, mustMarshal(oidSignedData), tlv(0xa0, signedData))
	status := tlv(0x30, mustMarshal(0))
	return tlv(0x30, status, contentInfo), nil
}

func attr(oid asn1.ObjectIdentifier, value []byte) []byte {
	return tlv(0x30, mustMarshal(oid), tlv(0x31, value))
}

func algID(oid asn1.ObjectIdentifier) []byte { return tlv(0x30, mustMarshal(oid)) }

func mustMarshal(v any) []byte {
	b, err := asn1.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// tlv encodes one DER value with a definite length.
func tlv(tag byte, parts ...[]byte) []byte {
	var content []byte
	for _, p := range parts {
		content = append(content, p...)
	}
	n := len(content)
	var l []byte
	switch {
	case n < 0x80:
		l = []byte{byte(n)}
	case n <= 0xff:
		l = []byte{0x81, byte(n)}
	case n <= 0xffff:
		l = []byte{0x82, byte(n >> 8), byte(n)}
	default:
		l = []byte{0x83, byte(n >> 16), byte(n >> 8), byte(n)}
	}
	return append(append([]byte{tag}, l...), content...)
}

// Service modes.
const (
	ServiceOK           = ""
	ServiceBadSignature = "bad-signature" // the root signature doesn't verify
	ServiceWrongLeaf    = "wrong-leaf"    // the path is for another leaf
	ServiceRefuse       = "refuse"        // 400 with a problem document
)

// Service is a fake transparency service: POST /entries with a COSE_Sign1 Signed Statement
// answers 201 with an RFC 9942 COSE inclusion proof (RFC9162_SHA256) over every entry so far.
type Service struct {
	*httptest.Server
	Key stamp.JWK

	mu     sync.Mutex
	mode   string
	leaves [][]byte
	priv   *ecdsa.PrivateKey
}

// NewService starts a fake transparency service with a fresh P-256 key. It holds seed entries
// first, so inclusion paths are not trivial.
func NewService() *Service {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	jwk, _ := stamp.JWKOf(&priv.PublicKey)
	s := &Service{Key: jwk, priv: priv}
	for i := 0; i < 5; i++ {
		s.leaves = append(s.leaves, stamp.LeafHash([]byte{byte(i)}))
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// SetMode switches how the service answers.
func (s *Service) SetMode(m string) { s.mu.Lock(); s.mode = m; s.mu.Unlock() }

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/entries" || r.Header.Get("Content-Type") != "application/cose" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	stmt, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	s.mu.Lock()
	mode := s.mode
	s.leaves = append(s.leaves, stamp.LeafHash(stmt))
	leaves := append([][]byte(nil), s.leaves...)
	s.mu.Unlock()
	if mode == ServiceRefuse {
		w.Header().Set("Content-Type", "application/concise-problem-details+cbor")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	index := uint64(len(leaves) - 1)
	if mode == ServiceWrongLeaf {
		index = 0
	}
	incl, err := s.Inclusion(leaves, index, mode == ServiceBadSignature)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/cose")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(incl)
}

// Inclusion signs an inclusion proof for leaves[index] over the tree of leaves.
func (s *Service) Inclusion(leaves [][]byte, index uint64, badSig bool) ([]byte, error) {
	root := TreeHead(leaves)
	path := Path(index, leaves)
	signer, err := cose.NewSigner(cose.AlgorithmES256, s.priv)
	if err != nil {
		return nil, err
	}
	msg := cose.NewSign1Message()
	msg.Headers.Protected = cose.ProtectedHeader{cose.HeaderLabelAlgorithm: cose.AlgorithmES256, int64(395): int64(1)}
	// The path is for leaves[index] but always labelled as the newest leaf: with index set to
	// another leaf (ServiceWrongLeaf) the root it leads to is not the one signed.
	msg.Headers.Unprotected = cose.UnprotectedHeader{int64(396): map[any]any{int64(-1): [][]byte{
		stamp.EncodeInclusion(uint64(len(leaves)), uint64(len(leaves)-1), path)}}}
	if badSig {
		root = append([]byte(nil), root...)
		root[0] ^= 0xff
	}
	msg.Payload = root
	if err := msg.Sign(rand.Reader, nil, signer); err != nil {
		return nil, err
	}
	msg.Payload = nil
	return msg.MarshalCBOR()
}

// TreeHead is the RFC 9162 Merkle tree hash over leaf hashes.
func TreeHead(leaves [][]byte) []byte {
	switch n := len(leaves); n {
	case 0:
		s := sha256.Sum256(nil)
		return s[:]
	case 1:
		return leaves[0]
	default:
		k := split(n)
		return stamp.NodeHash(TreeHead(leaves[:k]), TreeHead(leaves[k:]))
	}
}

// Path is the RFC 9162 inclusion path for leaves[m].
func Path(m uint64, leaves [][]byte) [][]byte {
	n := uint64(len(leaves))
	if n <= 1 {
		return nil
	}
	k := uint64(split(int(n)))
	if m < k {
		return append(Path(m, leaves[:k]), TreeHead(leaves[k:]))
	}
	return append(Path(m-k, leaves[k:]), TreeHead(leaves[:k]))
}

// split is the largest power of two smaller than n.
func split(n int) int {
	k := 1
	for k<<1 < n {
		k <<= 1
	}
	return k
}

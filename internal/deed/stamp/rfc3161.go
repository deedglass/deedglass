package stamp

import (
	"bytes"
	"crypto"
	"crypto/sha1" //nolint:gosec // ESSCertID (RFC 2634) names the signer cert by SHA-1; it is compared, never trusted alone
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// RFC 3161 (Time-Stamp Protocol) and the part of CMS (RFC 5652) a time-stamp token uses, with the
// standard library only.

var (
	oidSignedData      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidTSTInfo         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	oidAttrContentType = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidAttrDigest      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidAttrSigningV1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 12}
	oidAttrSigningV2   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 47}

	oidSHA1   = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}

	oidRSA          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidRSASHA256    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidRSASHA384    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}
	oidRSASHA512    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}
	oidECPublicKey  = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidECDSASHA256  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidECDSASHA384  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	oidECDSASHA512  = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}
	oidEd25519      = asn1.ObjectIdentifier{1, 3, 101, 112}
	oidTimeStamping = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 8}
)

// MaxTokenBytes bounds one time-stamp token (a TSA's chain is a few KiB).
const MaxTokenBytes = 64 << 10

type messageImprint struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	HashedMessage []byte
}

type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	Nonce          *big.Int
	CertReq        bool `asn1:"optional,default:false"`
}

// TokenInfo is what a verified token says.
type TokenInfo struct {
	GenTime time.Time // when the TSA saw the imprint (TSTInfo.genTime)
	Serial  *big.Int
	Policy  string
	Signer  string // the signing cert's subject, for people
	Pinned  bool   // the signer chains to a root the reader passed (--tsa-roots)
	Nonce   *big.Int
}

// NewRequest returns a DER TimeStampReq for a SHA-256 imprint, asking for the TSA's chain.
func NewRequest(imprint []byte, nonce *big.Int) ([]byte, error) {
	if len(imprint) != sha256.Size {
		return nil, errors.New("stamp: the imprint must be a SHA-256 digest")
	}
	return asn1.Marshal(timeStampReq{Version: 1,
		MessageImprint: messageImprint{HashAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}, HashedMessage: imprint},
		Nonce:          nonce, CertReq: true})
}

// ParseResponse reads a DER TimeStampResp and returns the token (a CMS ContentInfo) when the TSA
// granted the request (status 0 or 1).
func ParseResponse(b []byte) ([]byte, error) {
	rv, err := one(b)
	if err != nil {
		return nil, errors.New("the answer is not a time-stamp response")
	}
	els, err := seqOf(rv)
	if err != nil || len(els) < 1 {
		return nil, errors.New("the answer is not a time-stamp response")
	}
	st, err := seqOf(els[0])
	if err != nil || len(st) < 1 {
		return nil, errors.New("the answer has no status")
	}
	var status int
	if _, err := asn1.Unmarshal(st[0].FullBytes, &status); err != nil {
		return nil, errors.New("the answer has no status")
	}
	if status != 0 && status != 1 {
		return nil, fmt.Errorf("the TSA refused the request (status %d)", status)
	}
	if len(els) != 2 {
		return nil, errors.New("the TSA granted the request but sent no token")
	}
	return els[1].FullBytes, nil
}

// token is a parsed time-stamp token.
type token struct {
	tst         []byte // TSTInfo DER (eContent)
	certs       []*X509
	sid         asn1.RawValue
	digestAlg   asn1.ObjectIdentifier
	signedAttrs []byte // DER with the SET tag, as signed
	attrs       []asn1.RawValue
	sigAlg      asn1.ObjectIdentifier
	sig         []byte
}

func parseToken(der []byte) (*token, error) {
	bad := func(what string) error { return fmt.Errorf("the token is not valid CMS (%s)", what) }
	if len(der) > MaxTokenBytes {
		return nil, errors.New("the token is larger than 64 KiB")
	}
	ci, err := one(der)
	if err != nil {
		return nil, bad("content info")
	}
	cie, err := seqOf(ci)
	if err != nil || len(cie) != 2 || !isContext(cie[1], 0) {
		return nil, bad("content info")
	}
	var ct asn1.ObjectIdentifier
	if _, err := asn1.Unmarshal(cie[0].FullBytes, &ct); err != nil || !ct.Equal(oidSignedData) {
		return nil, bad("not signed data")
	}
	sdv, err := one(cie[1].Bytes)
	if err != nil {
		return nil, bad("signed data")
	}
	sd, err := seqOf(sdv)
	if err != nil || len(sd) < 4 {
		return nil, bad("signed data")
	}
	t := &token{}
	// sd[0] version, sd[1] digestAlgorithms, sd[2] encapContentInfo, then [0] certs, [1] crls, signerInfos.
	eci, err := seqOf(sd[2])
	if err != nil || len(eci) != 2 || !isContext(eci[1], 0) {
		return nil, bad("encapsulated content")
	}
	var ect asn1.ObjectIdentifier
	if _, err := asn1.Unmarshal(eci[0].FullBytes, &ect); err != nil || !ect.Equal(oidTSTInfo) {
		return nil, bad("the content is not TSTInfo")
	}
	if _, err := asn1.Unmarshal(eci[1].Bytes, &t.tst); err != nil {
		return nil, bad("TSTInfo octets")
	}
	rest := sd[3:]
	if len(rest) > 0 && isContext(rest[0], 0) {
		if t.certs, err = x509.ParseCertificates(rest[0].Bytes); err != nil {
			return nil, bad("the TSA chain")
		}
		rest = rest[1:]
	}
	if len(rest) > 0 && isContext(rest[0], 1) {
		rest = rest[1:] // CRLs are not used
	}
	if len(rest) != 1 || !isUniversal(rest[0], asn1.TagSet) {
		return nil, bad("signer infos")
	}
	sis, err := elements(rest[0].Bytes)
	if err != nil || len(sis) != 1 {
		return nil, bad("exactly one signer info")
	}
	si, err := seqOf(sis[0])
	if err != nil || len(si) < 6 {
		return nil, bad("signer info")
	}
	// si[0] version, si[1] sid, si[2] digestAlgorithm, si[3] [0] signedAttrs, si[4] signatureAlgorithm, si[5] signature.
	t.sid = si[1]
	var da pkix.AlgorithmIdentifier
	if _, err := asn1.Unmarshal(si[2].FullBytes, &da); err != nil {
		return nil, bad("digest algorithm")
	}
	t.digestAlg = da.Algorithm
	if !isContext(si[3], 0) || !si[3].IsCompound {
		return nil, bad("signed attributes are required")
	}
	t.signedAttrs = append([]byte{0x31}, si[3].FullBytes[1:]...)
	if t.attrs, err = elements(si[3].Bytes); err != nil {
		return nil, bad("signed attributes")
	}
	var sa pkix.AlgorithmIdentifier
	if _, err := asn1.Unmarshal(si[4].FullBytes, &sa); err != nil {
		return nil, bad("signature algorithm")
	}
	t.sigAlg = sa.Algorithm
	if _, err := asn1.Unmarshal(si[5].FullBytes, &t.sig); err != nil {
		return nil, bad("signature")
	}
	return t, nil
}

// tstInfo is the part of TSTInfo the checks use.
type tstInfo struct {
	imprint messageImprint
	policy  asn1.ObjectIdentifier
	serial  *big.Int
	genTime time.Time
	nonce   *big.Int
}

func parseTSTInfo(b []byte) (*tstInfo, error) {
	bad := errors.New("the token's TSTInfo is not valid")
	rv, err := one(b)
	if err != nil {
		return nil, bad
	}
	els, err := seqOf(rv)
	if err != nil || len(els) < 5 {
		return nil, bad
	}
	var v int
	if _, err := asn1.Unmarshal(els[0].FullBytes, &v); err != nil || v != 1 {
		return nil, bad
	}
	t := &tstInfo{}
	if _, err := asn1.Unmarshal(els[1].FullBytes, &t.policy); err != nil {
		return nil, bad
	}
	if _, err := asn1.Unmarshal(els[2].FullBytes, &t.imprint); err != nil {
		return nil, bad
	}
	if _, err := asn1.Unmarshal(els[3].FullBytes, &t.serial); err != nil {
		return nil, bad
	}
	if _, err := asn1.UnmarshalWithParams(els[4].FullBytes, &t.genTime, "generalized"); err != nil {
		return nil, bad
	}
	for _, e := range els[5:] { // accuracy, ordering, nonce, [0] tsa, [1] extensions
		if isUniversal(e, asn1.TagInteger) {
			t.nonce = new(big.Int)
			if _, err := asn1.Unmarshal(e.FullBytes, &t.nonce); err != nil {
				return nil, bad
			}
		}
	}
	return t, nil
}

func hashOf(oid asn1.ObjectIdentifier) (crypto.Hash, bool) {
	switch {
	case oid.Equal(oidSHA256):
		return crypto.SHA256, true
	case oid.Equal(oidSHA384):
		return crypto.SHA384, true
	case oid.Equal(oidSHA512):
		return crypto.SHA512, true
	}
	return 0, false
}

func sum(h crypto.Hash, b []byte) []byte {
	switch h {
	case crypto.SHA384:
		s := sha512.Sum384(b)
		return s[:]
	case crypto.SHA512:
		s := sha512.Sum512(b)
		return s[:]
	}
	s := sha256.Sum256(b)
	return s[:]
}

// sigAlgOf maps a SignerInfo's (digest, signature) algorithms to the x509 algorithm.
func sigAlgOf(digest, sig asn1.ObjectIdentifier) (x509.SignatureAlgorithm, bool) {
	h, ok := hashOf(digest)
	if !ok {
		return 0, false
	}
	switch {
	case sig.Equal(oidEd25519):
		return x509.PureEd25519, true
	case sig.Equal(oidRSASHA256) && h == crypto.SHA256, sig.Equal(oidRSA) && h == crypto.SHA256:
		return x509.SHA256WithRSA, true
	case sig.Equal(oidRSASHA384) && h == crypto.SHA384, sig.Equal(oidRSA) && h == crypto.SHA384:
		return x509.SHA384WithRSA, true
	case sig.Equal(oidRSASHA512) && h == crypto.SHA512, sig.Equal(oidRSA) && h == crypto.SHA512:
		return x509.SHA512WithRSA, true
	case sig.Equal(oidECDSASHA256) && h == crypto.SHA256, sig.Equal(oidECPublicKey) && h == crypto.SHA256:
		return x509.ECDSAWithSHA256, true
	case sig.Equal(oidECDSASHA384) && h == crypto.SHA384, sig.Equal(oidECPublicKey) && h == crypto.SHA384:
		return x509.ECDSAWithSHA384, true
	case sig.Equal(oidECDSASHA512) && h == crypto.SHA512, sig.Equal(oidECPublicKey) && h == crypto.SHA512:
		return x509.ECDSAWithSHA512, true
	}
	return 0, false
}

// signer finds the cert the SignerInfo names (issuer and serial, or subject key id).
func (t *token) signer() (*X509, error) {
	if isContext(t.sid, 0) {
		for _, c := range t.certs {
			if len(c.SubjectKeyId) > 0 && bytes.Equal(c.SubjectKeyId, t.sid.Bytes) {
				return c, nil
			}
		}
		return nil, errors.New("the token doesn't include the cert that signed it")
	}
	els, err := seqOf(t.sid)
	if err != nil || len(els) != 2 {
		return nil, errors.New("the token's signer id is not valid")
	}
	var serial *big.Int
	if _, err := asn1.Unmarshal(els[1].FullBytes, &serial); err != nil {
		return nil, errors.New("the token's signer id is not valid")
	}
	for _, c := range t.certs {
		if bytes.Equal(c.RawIssuer, els[0].FullBytes) && c.SerialNumber.Cmp(serial) == 0 {
			return c, nil
		}
	}
	return nil, errors.New("the token doesn't include the cert that signed it")
}

// checkAttrs checks the signed attributes: the content type, the content's digest, and the ESS
// signing-cert attribute (RFC 3161 §2.4.2, RFC 5816) naming the signer.
func (t *token) checkAttrs(h crypto.Hash, signer *X509) error {
	var gotType, gotDigest, gotSigning bool
	for _, a := range t.attrs {
		els, err := seqOf(a)
		if err != nil || len(els) != 2 || !isUniversal(els[1], asn1.TagSet) {
			return errors.New("a signed attribute is not valid")
		}
		var oid asn1.ObjectIdentifier
		if _, err := asn1.Unmarshal(els[0].FullBytes, &oid); err != nil {
			return errors.New("a signed attribute is not valid")
		}
		vals, err := elements(els[1].Bytes)
		if err != nil || len(vals) != 1 {
			return errors.New("a signed attribute is not valid")
		}
		v := vals[0].FullBytes
		switch {
		case oid.Equal(oidAttrContentType):
			var ct asn1.ObjectIdentifier
			if _, err := asn1.Unmarshal(v, &ct); err != nil || !ct.Equal(oidTSTInfo) {
				return errors.New("the signed content type is not TSTInfo")
			}
			gotType = true
		case oid.Equal(oidAttrDigest):
			var d []byte
			if _, err := asn1.Unmarshal(v, &d); err != nil || !bytes.Equal(d, sum(h, t.tst)) {
				return errors.New("the signed digest doesn't match the TSTInfo")
			}
			gotDigest = true
		case oid.Equal(oidAttrSigningV2):
			if err := checkESS(v, signer, true); err != nil {
				return err
			}
			gotSigning = true
		case oid.Equal(oidAttrSigningV1):
			if err := checkESS(v, signer, false); err != nil {
				return err
			}
			gotSigning = true
		}
	}
	switch {
	case !gotType:
		return errors.New("the token has no signed content type")
	case !gotDigest:
		return errors.New("the token has no signed digest")
	case !gotSigning:
		return errors.New("the token doesn't name its signing cert (ESS signing-cert attribute)")
	}
	return nil
}

// checkESS checks that the first ESSCertID(v2) hashes to the signer's DER.
func checkESS(v []byte, signer *X509, v2 bool) error {
	bad := errors.New("the token's signing-cert attribute doesn't match its signer")
	rv, err := one(v)
	if err != nil {
		return bad
	}
	outer, err := seqOf(rv)
	if err != nil || len(outer) < 1 {
		return bad
	}
	ids, err := seqOf(outer[0])
	if err != nil || len(ids) < 1 {
		return bad
	}
	id, err := seqOf(ids[0])
	if err != nil || len(id) < 1 {
		return bad
	}
	var want []byte
	h := 0 // 0 sha256, 1 sha1, 2 sha384, 3 sha512
	if v2 {
		if isUniversal(id[0], asn1.TagSequence) {
			var alg pkix.AlgorithmIdentifier
			if _, err := asn1.Unmarshal(id[0].FullBytes, &alg); err != nil {
				return bad
			}
			switch {
			case alg.Algorithm.Equal(oidSHA256):
			case alg.Algorithm.Equal(oidSHA384):
				h = 2
			case alg.Algorithm.Equal(oidSHA512):
				h = 3
			default:
				return bad
			}
			id = id[1:]
			if len(id) < 1 {
				return bad
			}
		}
	} else {
		h = 1
	}
	if _, err := asn1.Unmarshal(id[0].FullBytes, &want); err != nil {
		return bad
	}
	var got []byte
	switch h {
	case 1:
		s := sha1.Sum(signer.Raw) //nolint:gosec // ESSCertID v1 is defined with SHA-1
		got = s[:]
	case 2:
		s := sha512.Sum384(signer.Raw)
		got = s[:]
	case 3:
		s := sha512.Sum512(signer.Raw)
		got = s[:]
	default:
		s := sha256.Sum256(signer.Raw)
		got = s[:]
	}
	if !bytes.Equal(got, want) {
		return bad
	}
	return nil
}

func hasTimeStampingEKU(c *X509) bool {
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageTimeStamping {
			return true
		}
	}
	for _, u := range c.UnknownExtKeyUsage {
		if u.Equal(oidTimeStamping) {
			return true
		}
	}
	return false
}

// VerifyToken checks a DER time-stamp token over imprint (SHA-256 of the timestamped bytes): the
// CMS structure, the signed attributes, the signature by the cert the token names, that cert's
// time-stamping key usage and, when roots is not nil, its chain to one of roots at genTime. With
// roots nil the token is checked against the chain it carries (integrity, not authenticity) and
// TokenInfo.Pinned is false.
func VerifyToken(der, imprint []byte, roots *x509.CertPool) (*TokenInfo, error) {
	t, err := parseToken(der)
	if err != nil {
		return nil, err
	}
	info, err := parseTSTInfo(t.tst)
	if err != nil {
		return nil, err
	}
	if !info.imprint.HashAlgorithm.Algorithm.Equal(oidSHA256) || !bytes.Equal(info.imprint.HashedMessage, imprint) { //deedglass:allow-compare the imprint is SHA-256 of a public head
		return nil, errors.New("the token is for other data (its imprint doesn't match this head)")
	}
	h, ok := hashOf(t.digestAlg)
	if !ok {
		return nil, errors.New("the token uses a digest algorithm this verifier doesn't support")
	}
	alg, ok := sigAlgOf(t.digestAlg, t.sigAlg)
	if !ok {
		return nil, errors.New("the token uses a signature algorithm this verifier doesn't support")
	}
	signer, err := t.signer()
	if err != nil {
		return nil, err
	}
	if err := t.checkAttrs(h, signer); err != nil {
		return nil, err
	}
	if err := signer.CheckSignature(alg, t.signedAttrs, t.sig); err != nil {
		return nil, errors.New("the TSA's signature doesn't verify")
	}
	if !hasTimeStampingEKU(signer) {
		return nil, errors.New("the signing cert is not for time-stamping")
	}
	out := &TokenInfo{GenTime: info.genTime.UTC(), Serial: info.serial, Policy: info.policy.String(),
		Signer: signer.Subject.String(), Nonce: info.nonce}
	if roots != nil {
		inter := x509.NewCertPool()
		for _, c := range t.certs {
			if c != signer {
				inter.AddCert(c)
			}
		}
		if _, err := signer.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: info.genTime,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping}}); err != nil {
			return nil, errors.New("the token is not from a TSA you trust (its chain doesn't reach --tsa-roots)")
		}
		out.Pinned = true
	}
	return out, nil
}

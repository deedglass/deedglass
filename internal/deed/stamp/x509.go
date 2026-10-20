package stamp

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
)

// X509 is the standard library's parsed X.509 type. RFC 3161 tokens carry the TSA's X.509 chain;
// this is the only file that names the type, so the rest of the package talks about a TSA's
// "chain" and "signer cert".
type X509 = x509.Certificate

// pemType is the PEM block type of an X.509 cert (RFC 7468 §5).
const pemType = "CERTIFICATE"

// ParseRoots reads PEM-encoded X.509 roots (the TSAs a reader trusts, verify --tsa-roots). It
// refuses a file with no such block.
func ParseRoots(b []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	n := 0
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		if blk.Type != pemType {
			continue
		}
		c, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			return nil, errors.New("a PEM block does not parse as X.509")
		}
		pool.AddCert(c)
		n++
	}
	if n == 0 {
		return nil, errors.New("no X.509 PEM blocks")
	}
	return pool, nil
}

// EncodePEM is one X.509 DER as PEM (tests and docs write TSA roots with it).
func EncodePEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der})
}

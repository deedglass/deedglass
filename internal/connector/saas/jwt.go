package saas

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
)

// MinRSABits is the smallest RSA key SignRS256 and ParseRSAPrivateKey accept.
const MinRSABits = 2048

// SignRS256 returns a compact JWS (RFC 7515) over claims with RS256 (RSASSA-PKCS1-v1_5 with
// SHA-256), as GitHub App JWTs need. The header is {"alg":"RS256","typ":"JWT"}; claims are
// encoded with encoding/json (sorted keys). No dependency: crypto/rsa only.
func SignRS256(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	return signRS256(rand.Reader, key, claims)
}

func signRS256(r io.Reader, key *rsa.PrivateKey, claims map[string]any) (string, error) {
	if key == nil || key.N.BitLen() < MinRSABits {
		return "", errors.New("saas: an RS256 key must be RSA with at least 2048 bits")
	}
	h, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(r, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + enc.EncodeToString(sig), nil
}

// ParseRSAPrivateKey reads one PEM block (PKCS#1 "RSA PRIVATE KEY" or PKCS#8 "PRIVATE KEY") holding
// an RSA key of at least MinRSABits. Errors never quote the key.
func ParseRSAPrivateKey(pemText []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemText)
	if block == nil {
		return nil, errors.New("saas: the private key isn't PEM")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("saas: the RSA private key can't be read")
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("saas: the private key can't be read")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("saas: the private key isn't RSA")
		}
		key = rk
	default:
		return nil, errors.New("saas: the PEM block isn't a private key")
	}
	if key.N.BitLen() < MinRSABits {
		return nil, errors.New("saas: the RSA key is shorter than 2048 bits")
	}
	return key, nil
}

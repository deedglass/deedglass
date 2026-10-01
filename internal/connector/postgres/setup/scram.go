package setup

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// SCRAM-SHA-256 verifiers (RFC 5802, RFC 7677), computed on the client as psql's \password does, so
// Postgres stores the verifier and never sees the password: not in the statement, not in its logs.
const (
	scramIterations = 4096
	scramSaltLen    = 16
	// PasswordLen is the length of a generated password: 32 base62 characters, about 190 bits.
	PasswordLen = 32
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GeneratePassword returns a PasswordLen-character base62 password from crypto/rand.
func GeneratePassword(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	max := big.NewInt(int64(len(base62)))
	b := make([]byte, PasswordLen)
	for i := range b {
		n, err := rand.Int(r, max)
		if err != nil {
			return "", fmt.Errorf("setup: generating a password: %w", err)
		}
		b[i] = base62[n.Int64()]
	}
	return string(b), nil
}

// SCRAMVerifier returns "SCRAM-SHA-256$4096:<salt>$<StoredKey>:<ServerKey>" for password, with a
// fresh 16-byte salt from r (crypto/rand when nil). The password must be ASCII (generated
// passwords are base62), so SASLprep leaves it unchanged.
func SCRAMVerifier(password string, r io.Reader) (string, error) {
	for i := 0; i < len(password); i++ {
		if password[i] < 0x21 || password[i] > 0x7e {
			return "", errors.New("setup: a SCRAM password here must be printable ASCII")
		}
	}
	if r == nil {
		r = rand.Reader
	}
	salt := make([]byte, scramSaltLen)
	if _, err := io.ReadFull(r, salt); err != nil {
		return "", fmt.Errorf("setup: reading a salt: %w", err)
	}
	return scramWithSalt(password, salt, scramIterations)
}

func scramWithSalt(password string, salt []byte, iter int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iter, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("setup: pbkdf2: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	clear(salted)
	clear(clientKey)
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iter, b64(salt), b64(stored[:]), b64(serverKey)), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

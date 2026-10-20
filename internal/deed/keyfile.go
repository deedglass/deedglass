package deed

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/keys"
)

// KeyFile is a seal key on disk: .deedglass/keys/sk_<ulid>.json, directory 0700, file 0600. The
// seed is the secret; everything else is public.
type KeyFile struct {
	Object    string `json:"object"` // "seal_key_file"
	V         int64  `json:"v"`      // 1
	ID        string `json:"id"`
	Alg       string `json:"alg"`        // "Ed25519"
	Seed      string `json:"seed"`       // base64url, 32 bytes
	PublicKey string `json:"public_key"` // base64url, 32 bytes
	CreatedAt string `json:"created_at"`
}

// String never shows the seed.
func (k KeyFile) String() string { return "seal key file " + k.ID }

// GoString never shows the seed.
func (k KeyFile) GoString() string { return "deed.KeyFile{" + k.ID + "}" }

// DefaultKeyDir is where keys gen, keys rotate and dev serve put key files.
const DefaultKeyDir = ".deedglass/keys"

// NewSeed reads a 32-byte seed from r (crypto/rand in production).
func NewSeed(r io.Reader) ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(r, seed); err != nil {
		return nil, fmt.Errorf("deed: reading randomness: %w", err)
	}
	return seed, nil
}

// NewKeyFile builds a key file for id and seed.
func NewKeyFile(id string, seed []byte, created time.Time) (KeyFile, *FileSigner, error) {
	s, err := NewFileSigner(id, seed)
	if err != nil {
		return KeyFile{}, nil, err
	}
	return KeyFile{Object: "seal_key_file", V: 1, ID: id, Alg: AlgEd25519, Seed: b64.EncodeToString(seed),
		PublicKey: b64.EncodeToString(s.PublicKey()), CreatedAt: FormatTime(created)}, s, nil
}

// WriteKeyFile writes kf as dir/<id>.json. It creates dir with mode 0700 when missing, refuses to
// overwrite an existing file, and writes the file with mode 0600.
func WriteKeyFile(dir string, kf KeyFile) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("deed: creating %s: %w", dir, err)
	}
	b, err := StrictCanonical(kf)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, kf.ID+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("deed: creating %s: %w", path, err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return "", fmt.Errorf("deed: writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("deed: writing %s: %w", path, err)
	}
	return path, nil
}

// KeyFileProblem is why LoadKeyFile refused a file.
type KeyFileProblem int

const (
	KeyFileUnreadable KeyFileProblem = iota + 1 // missing, not a regular file, or not a seal key file
	KeyFileTooOpen                              // readable or writable by group or others
	KeyFileUnsafe                               // public_key is small-order or non-canonical (A13)
	KeyFileMismatch                             // the seed does not derive public_key
	KeyFileTestKey                              // a published test-vector key
)

// KeyFileError is every LoadKeyFile refusal. It never holds the seed.
type KeyFileError struct {
	Problem KeyFileProblem
	Path    string
	ID      string      // the key id, when the file could be read
	Mode    fs.FileMode // KeyFileTooOpen
	Detail  string
}

func (e *KeyFileError) Error() string {
	switch e.Problem {
	case KeyFileTooOpen:
		return fmt.Sprintf("deed: seal key file %s can be read by other users (mode %04o)", e.Path, e.Mode.Perm())
	case KeyFileUnsafe:
		return fmt.Sprintf("deed: seal key %s is not a safe Ed25519 key (%s)", e.ID, e.Detail)
	case KeyFileMismatch:
		return fmt.Sprintf("deed: seal key file %s: the seed does not match public_key", e.Path)
	case KeyFileTestKey:
		return "deed: this is a published test key"
	}
	return fmt.Sprintf("deed: seal key file %s: %s", e.Path, e.Detail)
}

// LoadOptions tunes LoadKeyFile.
type LoadOptions struct {
	Prod bool // DEEDGLASS_ENV=prod
}

// LoadKeyFile reads a seal key file and returns its signer. It refuses, in this order: an
// unreadable or invalid file; a published test-vector key (in prod always, and in dev outside
// tests); a file group or others can read or write (like ssh); a public key keys.ParsePublicKey
// refuses (small-order or non-canonical, attack case A13); and a seed that does not derive the
// public key.
func LoadKeyFile(path string, o LoadOptions) (*FileSigner, error) {
	bad := func(p KeyFileProblem, detail string) error {
		return &KeyFileError{Problem: p, Path: path, Detail: detail}
	}
	kf, fi, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	if kf.Alg != AlgEd25519 {
		return nil, bad(KeyFileUnreadable, "it holds an "+kf.Alg+" key; DEEDGLASS_SEAL_KEY_FILE takes the Ed25519 key")
	}
	pubRaw, err := b64.DecodeString(kf.PublicKey)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return nil, bad(KeyFileUnreadable, "public_key is not 32 bytes of base64url")
	}
	seed, err := b64.DecodeString(kf.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, bad(KeyFileUnreadable, "seed is not 32 bytes of base64url")
	}
	if IsTestKey(pubRaw) && (o.Prod || !testing.Testing()) {
		return nil, &KeyFileError{Problem: KeyFileTestKey, Path: path, ID: kf.ID}
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, &KeyFileError{Problem: KeyFileTooOpen, Path: path, ID: kf.ID, Mode: fi.Mode()}
	}
	if _, err := keys.ParsePublicKey(pubRaw); err != nil {
		return nil, &KeyFileError{Problem: KeyFileUnsafe, Path: path, ID: kf.ID, Detail: unsafeReason(err)}
	}
	s, err := NewFileSigner(kf.ID, seed)
	if err != nil {
		return nil, bad(KeyFileUnreadable, err.Error())
	}
	if !s.PublicKey().Equal(ed25519.PublicKey(pubRaw)) {
		return nil, &KeyFileError{Problem: KeyFileMismatch, Path: path, ID: kf.ID}
	}
	return s, nil
}

// readKeyFile reads and parses a seal key file of any algorithm (no key checks).
func readKeyFile(path string) (*KeyFile, fs.FileInfo, error) {
	bad := func(p KeyFileProblem, detail string) error {
		return &KeyFileError{Problem: p, Path: path, Detail: detail}
	}
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, bad(KeyFileUnreadable, "the file does not exist")
		}
		return nil, nil, bad(KeyFileUnreadable, "it can't be read")
	}
	if !fi.Mode().IsRegular() {
		return nil, nil, bad(KeyFileUnreadable, "it is not a regular file")
	}
	if fi.Size() > 4096 {
		return nil, nil, bad(KeyFileUnreadable, "it is too large to be a seal key file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, bad(KeyFileUnreadable, "it can't be read")
	}
	kf, err := parseKeyFile(raw)
	if err != nil {
		return nil, nil, bad(KeyFileUnreadable, err.Error())
	}
	return kf, fi, nil
}

// NewMLDSAKeyFile builds an ML-DSA-65 key file for id and a 32-byte FIPS 204 seed (ADR-109).
func NewMLDSAKeyFile(id string, seed []byte, created time.Time) (KeyFile, *MLDSASigner, error) {
	s, err := NewMLDSASigner(id, seed)
	if err != nil {
		return KeyFile{}, nil, err
	}
	return KeyFile{Object: "seal_key_file", V: 1, ID: id, Alg: AlgMLDSA65, Seed: b64.EncodeToString(seed),
		PublicKey: b64.EncodeToString(s.PublicKeyBytes()), CreatedAt: FormatTime(created)}, s, nil
}

// LoadPQKeyFile reads an ML-DSA-65 seal key file (DEEDGLASS_SEAL_PQ_KEY_FILE, ADR-109) and returns
// its signer. It refuses, in this order: an unreadable or invalid file, or one holding another
// algorithm; a published test-vector key (in prod always, and in dev outside tests); a file group or
// others can read or write; and a seed that does not derive the public key.
func LoadPQKeyFile(path string, o LoadOptions) (*MLDSASigner, error) {
	bad := func(p KeyFileProblem, detail string) error {
		return &KeyFileError{Problem: p, Path: path, Detail: detail}
	}
	kf, fi, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}
	if kf.Alg != AlgMLDSA65 {
		return nil, bad(KeyFileUnreadable, "it holds an "+kf.Alg+" key; DEEDGLASS_SEAL_PQ_KEY_FILE takes an ML-DSA-65 key")
	}
	pubRaw, err := b64.DecodeString(kf.PublicKey)
	if err != nil || len(pubRaw) != mldsaPublicKeySize {
		return nil, bad(KeyFileUnreadable, fmt.Sprintf("public_key is not %d bytes of base64url", mldsaPublicKeySize))
	}
	seed, err := b64.DecodeString(kf.Seed)
	if err != nil || len(seed) != 32 {
		return nil, bad(KeyFileUnreadable, "seed is not 32 bytes of base64url")
	}
	if IsTestKey(pubRaw) && (o.Prod || !testing.Testing()) {
		return nil, &KeyFileError{Problem: KeyFileTestKey, Path: path, ID: kf.ID}
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, &KeyFileError{Problem: KeyFileTooOpen, Path: path, ID: kf.ID, Mode: fi.Mode()}
	}
	s, err := NewMLDSASigner(kf.ID, seed)
	if err != nil {
		return nil, bad(KeyFileUnreadable, err.Error())
	}
	if !bytes.Equal(s.PublicKeyBytes(), pubRaw) {
		return nil, &KeyFileError{Problem: KeyFileMismatch, Path: path, ID: kf.ID}
	}
	return s, nil
}

func unsafeReason(err error) string {
	switch {
	case errors.Is(err, keys.ErrSmallOrder):
		return "small-order point"
	case errors.Is(err, keys.ErrNonCanonical):
		return "non-canonical encoding"
	case errors.Is(err, keys.ErrNotOnCurve):
		return "not a point on the curve"
	}
	return err.Error()
}

// UnsafeReason names why keys.ParsePublicKey refused a key, for messages.
func UnsafeReason(err error) string { return unsafeReason(err) }

func parseKeyFile(raw []byte) (*KeyFile, error) {
	tree, err := intent.ParseStrictNonNegative(raw)
	if err != nil {
		return nil, errors.New("it is not strict JSON")
	}
	o, verr := asObj(tree, "")
	if verr != nil {
		return nil, errors.New("it is not a JSON object")
	}
	if verr := o.closed([]string{"object", "v", "id", "alg", "seed", "public_key", "created_at"}); verr != nil {
		return nil, fmt.Errorf("%s %s", verr.Field, verr.Detail)
	}
	d := &decoder{}
	kf := &KeyFile{Object: d.s(o, "object"), V: d.i(o, "v"), ID: d.s(o, "id"), Alg: d.s(o, "alg"),
		Seed: d.s(o, "seed"), PublicKey: d.s(o, "public_key"), CreatedAt: d.s(o, "created_at")}
	switch {
	case d.err != nil:
		return nil, fmt.Errorf("%s %s", d.err.Field, d.err.Detail)
	case kf.Object != "seal_key_file" || kf.V != 1:
		return nil, errors.New(`it is not a seal_key_file v1`)
	case kf.Alg != AlgEd25519 && kf.Alg != AlgMLDSA65:
		return nil, errors.New("alg must be Ed25519 or ML-DSA-65")
	case !isID(pSealKey, kf.ID):
		return nil, errors.New("id must be an sk_ id")
	}
	return kf, nil
}

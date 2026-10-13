package licence

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultTokenFile is where activate writes the token (DEEDGLASS_LICENCE_FILE overrides it).
const DefaultTokenFile = ".deedglass/licence.json"

// KeyFileFor is the licence key's file next to the token: licence.json → licence.key.
func KeyFileFor(tokenPath string) string {
	return strings.TrimSuffix(tokenPath, filepath.Ext(tokenPath)) + ".key"
}

// ErrNoLicence means there is no token file.
var ErrNoLicence = errors.New("licence: no licence on this machine")

// UnsafeFileError means a licence file is a symlink or readable by others.
type UnsafeFileError struct {
	Path string
	Why  string
}

func (e *UnsafeFileError) Error() string { return "licence: " + e.Path + " " + e.Why }

// readPrivate reads a file that must be a regular file with no group or other permissions.
func readPrivate(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoLicence
	}
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, &UnsafeFileError{Path: path, Why: "is a symlink"}
	}
	if !fi.Mode().IsRegular() {
		return nil, &UnsafeFileError{Path: path, Why: "is not a regular file"}
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, &UnsafeFileError{Path: path, Why: fmt.Sprintf("has mode %#o; it must be 0600", fi.Mode().Perm())}
	}
	if fi.Size() > 64<<10 {
		return nil, &UnsafeFileError{Path: path, Why: "is larger than 64 KiB"}
	}
	return os.ReadFile(path)
}

// writePrivate writes b atomically with mode 0600, creating the directory with 0700.
func writePrivate(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return &UnsafeFileError{Path: path, Why: "is a symlink"}
	}
	tmp, err := os.CreateTemp(dir, ".licence-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LoadToken reads and parses the token file (the signature is checked by Verify).
func LoadToken(path string) (Token, error) {
	b, err := readPrivate(path)
	if err != nil {
		return Token{}, err
	}
	return Parse(b)
}

// SaveToken writes the token file (0600).
func SaveToken(path string, t Token) error {
	b, err := t.Marshal()
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

// LoadKey reads the licence key next to the token.
func LoadKey(tokenPath string) (string, error) {
	b, err := readPrivate(KeyFileFor(tokenPath))
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if !ValidKey(k) {
		return "", &UnsafeFileError{Path: KeyFileFor(tokenPath), Why: "doesn't hold a licence key"}
	}
	return k, nil
}

// SaveKey writes the licence key next to the token (0600).
func SaveKey(tokenPath, key string) error {
	return writePrivate(KeyFileFor(tokenPath), []byte(key+"\n"))
}

// Remove deletes the token, the key and the clock mark. Missing files are fine.
func Remove(tokenPath string) error {
	var errs []error
	for _, p := range []string{tokenPath, KeyFileFor(tokenPath), ClockFileFor(tokenPath)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ValidKey is the shape of a Dodo licence key: letters, digits and dashes, 8 to 128 characters.
func ValidKey(k string) bool {
	if len(k) < 8 || len(k) > 128 {
		return false
	}
	for _, c := range k {
		if !(c == '-' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

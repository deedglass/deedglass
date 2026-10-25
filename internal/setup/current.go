package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ~/.deedglass/current.json (adoption-v1 F4): which local stack an MCP client uses when it runs in
// a project that has no .deedglass/ of its own. init writes it (mode 0600, in a 0700 directory); it
// holds the server URL and the paths of the key files, never a key. mcp serve reads it after the
// walk up from its working directory finds nothing.

// CurrentFile is the file's name under ~/.deedglass/.
const CurrentFile = "current.json"

// Current is ~/.deedglass/current.json.
type Current struct {
	Object       string `json:"object"` // "deedglass_current"
	Version      int    `json:"version"`
	Server       string `json:"server"`
	KeyFile      string `json:"key_file"`       // the agent key (quickstart.key), absolute
	AdminKeyFile string `json:"admin_key_file"` // the org admin key (admin.key), absolute
	Dir          string `json:"dir"`            // the directory deedglass init set up
	UpdatedAt    string `json:"updated_at"`
}

// CurrentPath is home/.deedglass/current.json.
func CurrentPath(home string) string { return filepath.Join(home, DirName, CurrentFile) }

// WriteCurrent points ~/.deedglass/current.json at the stack in dir. home "" writes nothing.
func WriteCurrent(home, dir string, stack *Stack, now time.Time) error {
	if home == "" || stack == nil || stack.Listen == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	d := filepath.Join(home, DirName)
	if fi, err := os.Lstat(d); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return errors.New("~/.deedglass is a symlink")
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(d, 0o700)
	c := Current{Object: "deedglass_current", Version: 1, Server: stack.Server(), KeyFile: Path(abs, AgentKeyFile),
		AdminKeyFile: Path(abs, AdminKeyFile), Dir: abs, UpdatedAt: now.UTC().Format(time.RFC3339)}
	return writeJSON(CurrentPath(home), c)
}

// ReadCurrent reads ~/.deedglass/current.json (nil, nil when there is none or home is "").
func ReadCurrent(home string) (*Current, error) {
	if home == "" {
		return nil, nil
	}
	var c Current
	ok, err := readJSON(CurrentPath(home), &c)
	if err != nil || !ok {
		return nil, err
	}
	return &c, nil
}

// RemoveCurrent deletes ~/.deedglass/current.json when it points at dir.
func RemoveCurrent(home, dir string) bool {
	c, err := ReadCurrent(home)
	if err != nil || c == nil {
		return false
	}
	abs, _ := filepath.Abs(dir)
	if c.Dir != abs {
		return false
	}
	return os.Remove(CurrentPath(home)) == nil
}

// ReadKeyFile reads a one-line key file by absolute path ("" when it is missing or unreadable). A
// file anyone else can read or write is refused.
func ReadKeyFile(p string) string {
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

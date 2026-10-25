// Package setup is `deedglass init` (S32 §3.9): each step is a function that returns a
// StepResult; the CLI only renders. Init is idempotent (run it twice: "Nothing to change."), never
// overwrites a secret, writes every secret file 0600 under a 0700 .deedglass/, and records what
// it made in .deedglass/state.json (no secret there).
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The files init writes under .deedglass/.
const (
	DirName      = ".deedglass"
	StateFile    = "state.json"       // init_state: ids only, never a secret
	StackFile    = "stack.json"       // the local stack: ports, container, passwords (0600)
	AdminKeyFile = "admin.key"        // the org admin key the CLI and doctor use (0600)
	AgentKeyFile = "quickstart.key"   // quickstart-agent's key, shown once (0600)
	SecretsFile  = "dev-secrets.json" // the vault key and API key pepper (config's dev file, 0600)
	ServeLog     = "serve.log"
	ServePID     = "serve.pid"
	LockFile     = "init.lock"
	KeysDir      = "keys"    // the seal key file(s), 0600
	PQKeysDir    = "keys/pq" // init --lockdown's ML-DSA-65 seal key, 0600
)

// State is .deedglass/state.json (S32 §3.9).
type State struct {
	Object    string   `json:"object"`
	Version   int      `json:"version"`
	Org       string   `json:"org"`
	Agent     string   `json:"agent"`
	KeyID     string   `json:"key_id"`
	Connector string   `json:"connector"`
	Policies  []string `json:"policies"`
	Stack     string   `json:"stack"`
	UpdatedAt string   `json:"updated_at"`
}

// Stack is .deedglass/stack.json: what doctor and a second init need to reach the local stack.
type Stack struct {
	Object           string `json:"object"`
	Version          int    `json:"version"`
	Project          string `json:"project"`
	Container        string `json:"container"`
	Volume           string `json:"volume"`
	Image            string `json:"image"`
	PGPort           int    `json:"pg_port"`
	DemoContainer    string `json:"demo_container"`
	DemoVolume       string `json:"demo_volume"`
	DemoPort         int    `json:"demo_port"`
	DemoPassword     string `json:"demo_password"`
	Listen           string `json:"listen"`
	AdminPassword    string `json:"admin_password"`
	MigratorPassword string `json:"migrator_password"`
	AppPassword      string `json:"app_password"`
	SealKeyFile      string `json:"seal_key_file"`
	OwnerEmail       string `json:"owner_email"`
	// Lockdown (ADR-117): init --lockdown ran here; later runs keep it. SealPQKeyFile is the
	// ML-DSA-65 key it made (in keys/pq, apart from the Ed25519 keys newestKey reads).
	Lockdown      bool   `json:"lockdown,omitempty"`
	SealPQKeyFile string `json:"seal_pq_key_file,omitempty"`
}

// AppURL is the runtime role's URL (deedglass_app on the deedglass database).
func (s *Stack) AppURL() string {
	return fmt.Sprintf("postgres://deedglass_app:%s@127.0.0.1:%d/deedglass?sslmode=disable", s.AppPassword, s.PGPort)
}

// MigratorURL is the migrator role's URL.
func (s *Stack) MigratorURL() string {
	return fmt.Sprintf("postgres://deedglass_migrator:%s@127.0.0.1:%d/deedglass?sslmode=disable", s.MigratorPassword, s.PGPort)
}

// DemoURL is the demo database as its bootstrap superuser (connector postgres init reads it once).
// The demo runs in its own container: Deedglass never connects to its own database through a
// connector (the egress door refuses that address).
func (s *Stack) DemoURL() string {
	return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/demo?sslmode=disable", s.DemoPassword, s.DemoPort)
}

// Server is http://<listen>.
func (s *Stack) Server() string { return "http://" + s.Listen }

// Path joins dir/.deedglass/name.
func Path(dir, name string) string { return filepath.Join(dir, DirName, name) }

// ReadStack reads .deedglass/stack.json under dir (nil, nil when there is none).
func ReadStack(dir string) (*Stack, error) {
	var s Stack
	ok, err := readJSON(Path(dir, StackFile), &s)
	if err != nil || !ok {
		return nil, err
	}
	return &s, nil
}

// ReadState reads .deedglass/state.json (zero State when there is none).
func ReadState(dir string) (State, error) {
	var s State
	_, err := readJSON(Path(dir, StateFile), &s)
	return s, err
}

// ReadSecretFile reads a one-line secret file (a key), "" when it doesn't exist.
func ReadSecretFile(dir, name string) (string, error) {
	b, err := os.ReadFile(Path(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

func readJSON(p string, v any) (bool, error) {
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%s is damaged: %w", filepath.Base(p), err)
	}
	return true, nil
}

// writeSecret writes p with mode 0600, refusing to follow a symlink or overwrite unless asked.
func writeSecret(p string, b []byte, overwrite bool) error {
	if fi, err := os.Lstat(p); err == nil {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symlink", filepath.Base(p))
		}
		if !overwrite {
			return fs.ErrExist
		}
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func writeJSON(p string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(p, append(b, '\n'), true)
}

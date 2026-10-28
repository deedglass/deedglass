package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/setup"
)

// The one server resolver (adoption-v1 F2). Every client command finds its Deedglass server the same
// way: --server, then DEEDGLASS_SERVER, then DEEDGLASS_BASE_URL, then the .deedglass/stack.json that
// deedglass init wrote in this directory or the nearest parent, then the running local app
// (deedglass app), then http://127.0.0.1:8080. A key
// read from .deedglass/ only ever goes to the server that same directory's stack.json names, so a
// stack on another port never sends its key to whatever listens on :8080. And inside an init
// directory, no key at all (not even DEEDGLASS_API_KEY) goes to a server the environment names that
// differs from the stack's: only an explicit --server sends it elsewhere.

// defaultServer is the last resort when nothing names a server (a var so tests can point it at a
// sentinel listener and prove nothing reaches it).
var defaultServer = "http://127.0.0.1:8080"

// Where a target's server came from.
const (
	fromFlag    = "flag"
	fromEnv     = "env"
	fromStack   = "stack"
	fromApp     = "app" // the running local app (deedglass app, DL-143)
	fromDefault = "default"
)

// target is a resolved server: its URL, where it came from, and the init directory when a
// stack.json named it.
type target struct {
	Server   string
	From     string
	StackDir string // the directory holding .deedglass/stack.json ("" unless From is stack)
	Stack    *setup.Stack
	AppDir   string // the app's data directory ("" unless From is app)
	// Conflict is the stack's server when the environment named a different one inside an init
	// directory: no key is sent then (keyFor returns "", conflictErr says why).
	Conflict string
}

// resolveServer is the resolver every client command uses.
func resolveServer(e *Env) target {
	if s := strings.TrimSpace(e.Globals.Server); s != "" {
		return target{Server: s, From: fromFlag}
	}
	dir, st := findStack(e)
	if st != nil && st.Listen == "" {
		st = nil
	}
	if s := firstNonEmpty(e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL")); s != "" {
		t := target{Server: s, From: fromEnv}
		if st != nil && !sameServer(s, st.Server()) {
			t.Conflict = st.Server()
		}
		return t
	}
	if st != nil {
		return target{Server: st.Server(), From: fromStack, StackDir: dir, Stack: st}
	}
	// DL-143: while deedglass app runs on this computer, client commands talk to it with no flags.
	if ad, as := runningApp(e); as != nil {
		return target{Server: as.Server, From: fromApp, AppDir: ad}
	}
	return target{Server: defaultServer, From: fromDefault}
}

// keyFor is the API key for t: --api-key, then DEEDGLASS_API_KEY, then (only when the stack named
// the server) the stack directory's key file. file is setup.AdminKeyFile or setup.AgentKeyFile.
func (t target) keyFor(e *Env, file string) string {
	if t.Conflict != "" {
		return ""
	}
	if k := firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY")); k != "" {
		return k
	}
	if t.From == fromStack && t.StackDir != "" {
		k, _ := setup.ReadSecretFile(t.StackDir, file)
		return k
	}
	// The app keeps only an admin key, and sends it only to the app's own loopback server.
	if t.From == fromApp && t.AppDir != "" && file == setup.AdminKeyFile {
		return setup.ReadKeyFile(filepath.Join(t.AppDir, appAdminKey))
	}
	return ""
}

// conflictErr explains a key held back because the environment names another server than the
// stack's (nil when there is no conflict).
func (t target) conflictErr() error {
	if t.Conflict == "" {
		return nil
	}
	return &ExitError{Code: ExitNotAuthed, ErrCode: "server_mismatch",
		Err:  fmt.Errorf("DEEDGLASS_SERVER or DEEDGLASS_BASE_URL names %s, but .deedglass/stack.json here names %s, so no API key was sent", t.Server, t.Conflict),
		Next: "Unset DEEDGLASS_SERVER and DEEDGLASS_BASE_URL to use the local stack, or pass --server " + t.Server + " to send the key there."}
}

// sameServer compares two server URLs, ignoring a trailing slash.
func sameServer(a, b string) bool {
	return strings.TrimSuffix(strings.TrimSpace(a), "/") == strings.TrimSuffix(strings.TrimSpace(b), "/")
}

// workDir is the directory a command runs in: the config's Dir when set (tests), else the process's.
func workDir(e *Env) string {
	if e.LoadOptions != nil {
		if d := e.LoadOptions().Dir; d != "" {
			if abs, err := filepath.Abs(d); err == nil {
				return abs
			}
		}
	}
	d, err := os.Getwd()
	if err != nil {
		return ""
	}
	return d
}

// findStack walks from the working directory up to the nearest directory whose .deedglass/ holds a
// stack.json (never the home directory's own ~/.deedglass, which holds current.json) and reads it.
// It returns "", nil when there is none.
func findStack(e *Env) (string, *setup.Stack) {
	dir := findInitDir(workDir(e), e.Getenv("HOME"), setup.StackFile)
	if dir == "" {
		return "", nil
	}
	st, err := setup.ReadStack(dir)
	if err != nil || st == nil {
		return "", nil
	}
	return dir, st
}

// findInitDir is the nearest directory at or above start whose .deedglass/ holds the file named
// (a .deedglass/ without it, like the dev secrets a config load leaves in a subfolder, is passed
// over), skipping home itself; "" when there is none.
func findInitDir(start, home, file string) string {
	if start == "" {
		return ""
	}
	if home != "" {
		if h, err := filepath.Abs(home); err == nil {
			home = h
		}
	}
	dir := start
	for i := 0; i < 64; i++ {
		if dir != home {
			if fi, err := os.Stat(filepath.Join(dir, setup.DirName, file)); err == nil && fi.Mode().IsRegular() {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// stackLoadOptions are the config options for a DB-direct command in an init directory found by
// the walk: Dir points at it (for .deedglass/dev-secrets.json), and its stack fills the settings.
// cliLoadOptions is the config a DB-direct command loads: the environment first, then the local
// stack deedglass init set up here or in a parent folder (stackLoadOptions). A stack.json that
// can't be read leaves the plain environment, and the command's own checks say what's missing.
func cliLoadOptions(e *Env) config.LoadOptions {
	if opts, _, err := stackLoadOptions(e); err == nil {
		return opts
	}
	if e.LoadOptions != nil {
		return e.LoadOptions()
	}
	return config.LoadOptions{}
}

func stackLoadOptions(e *Env) (config.LoadOptions, *setup.Stack, error) {
	var opts config.LoadOptions
	if e.LoadOptions != nil {
		opts = e.LoadOptions()
	}
	if dir, st := findStack(e); st != nil {
		opts.Dir = dir
		if opts.FS == nil {
			opts.FS = os.DirFS(dir)
		}
	} else if opts.Env["DEEDGLASS_DATABASE_URL"] == "" {
		// DL-143: no stack here and no database named, but the local app runs: its database.
		if ad, as := runningApp(e); as != nil {
			env := maps.Clone(opts.Env)
			if env == nil {
				env = map[string]string{}
			}
			for k, v := range appEnvFor(ad, as.Server, as.PGPort) {
				if env[k] == "" {
					env[k] = v
				}
			}
			opts.Env, opts.Dir, opts.FS = env, ad, os.DirFS(ad)
			return opts, nil, nil
		}
	}
	return withInitStack(opts)
}

// runningApp is the local app's data directory and state while deedglass app runs on this
// computer; a nil state otherwise.
func runningApp(e *Env) (string, *appState) {
	ad, err := appDir(e)
	if err != nil {
		return "", nil
	}
	if as := readAppState(ad); as != nil && processAlive(as.PID) {
		return ad, as
	}
	return "", nil
}

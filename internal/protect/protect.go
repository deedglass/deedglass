// Package protect installs the shell guard (internal/shellguard) as the before-a-command hook of
// every coding agent it finds, from the hook configs in shellguard.Clients, and takes it out again.
//
// Rules (docs/protect.md): it only adds. It merges into the JSON already there, keeps every key and
// hook the person wrote, in their order, and never adds a second guard. Before it changes a file it
// copies the file to a backup directory and writes a manifest; Undo puts each file back exactly as
// it was, or, when the person has changed the file since, removes only the entries protect added.
// A file it can't read as plain JSON is left alone. No network, no server.
package protect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/shellguard"
)

// Status is what protect does, or would do, to one agent's config file.
type Status string

const (
	Add     Status = "add"     // the hook goes in
	Present Status = "present" // a Deedglass guard is already there; nothing to do
	Skip    Status = "skip"    // the file can't be read as JSON (or has an unexpected shape); left alone
	Absent  Status = "absent"  // the agent's config folder isn't there
)

// Agent is one coding agent protect knows: where its user and project configs live.
type Agent struct {
	Client   string // the shellguard.Clients name
	Dir      string // the config folder that says the agent is installed, under HOME
	File     string // the file protect writes, under HOME
	ProjDir  string // the same, under the project directory
	ProjFile string
}

// Agents is every agent protect installs into, in the order it prints them. Claude Code's project
// file is settings.local.json: personal and not committed, unlike settings.json.
var Agents = []Agent{
	{Client: "claude-code", Dir: ".claude", File: ".claude/settings.json", ProjDir: ".claude", ProjFile: ".claude/settings.local.json"},
	{Client: "cursor", Dir: ".cursor", File: ".cursor/hooks.json", ProjDir: ".cursor", ProjFile: ".cursor/hooks.json"},
	{Client: "codex", Dir: ".codex", File: ".codex/hooks.json", ProjDir: ".codex", ProjFile: ".codex/hooks.json"},
	{Client: "gemini", Dir: ".gemini", File: ".gemini/settings.json", ProjDir: ".gemini", ProjFile: ".gemini/settings.json"},
	{Client: "copilot", Dir: ".copilot", File: ".copilot/hooks/deedglass.json", ProjDir: ".github", ProjFile: ".github/hooks/deedglass.json"},
}

// ClientNames lists the agents protect knows, comma-separated, for help and errors.
func ClientNames() string {
	ns := make([]string, len(Agents))
	for i, a := range Agents {
		ns[i] = a.Client
	}
	return strings.Join(ns, ", ")
}

// Options says where to look and how the hook starts deedglass.
type Options struct {
	Home    string   // the person's home directory (user scope)
	Dir     string   // the project directory (project scope, with Project)
	Project bool     // also install into the project's config folders
	Clients []string // only these agents; nil is every one
	Command string   // how the hook starts deedglass ("/usr/local/bin/deedglass", "npx -y deedglass@0.12.0")
	Now     func() time.Time
}

// Change is one agent's file and what protect does to it.
type Change struct {
	Client string   `json:"client"`
	Title  string   `json:"title"`
	Scope  string   `json:"scope"` // user | project
	File   string   `json:"file"`
	Event  string   `json:"event"`
	Status Status   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Notes  []string `json:"notes,omitempty"` // what this agent can't do, from shellguard.Clients

	existed bool
	mode    fs.FileMode
	before  []byte
	after   []byte
}

// StateDir is where backups and manifests live.
func StateDir(home string) string { return filepath.Join(home, ".deedglass", "protect") }

// guardMatcher reports whether a hook command runs the shell guard: any deedglass's, whoever
// wrote it, or the exact command protect writes (whatever the binary is called).
func guardMatcher(command string) func(string) bool {
	return func(s string) bool {
		return strings.Contains(s, "guard shell") && (strings.Contains(s, "deedglass") || command != "" && strings.Contains(s, command+" guard shell"))
	}
}

// Plan works out every change, writing nothing.
func Plan(o Options) ([]Change, error) {
	if o.Command == "" {
		return nil, errors.New("protect: no hook command")
	}
	var out []Change
	for _, a := range Agents {
		if len(o.Clients) > 0 && !slices.Contains(o.Clients, a.Client) {
			continue
		}
		hook, ok := shellguard.LookupClient(a.Client)
		if !ok {
			return nil, fmt.Errorf("protect: no shell guard client %s", a.Client)
		}
		scopes := []struct{ scope, root, dir, file string }{{"user", o.Home, a.Dir, a.File}}
		if o.Project && o.Dir != "" && filepath.Clean(o.Dir) != filepath.Clean(o.Home) {
			scopes = append(scopes, struct{ scope, root, dir, file string }{"project", o.Dir, a.ProjDir, a.ProjFile})
		}
		for _, s := range scopes {
			c := Change{Client: a.Client, Title: strings.Split(hook.Title, " (")[0], Scope: s.scope, File: filepath.Join(s.root, filepath.FromSlash(s.file)), Event: hook.Event, Notes: hook.Limits}
			if st, err := os.Stat(filepath.Join(s.root, s.dir)); err != nil || !st.IsDir() {
				if s.scope == "project" {
					continue // a project without this agent's folder isn't worth a row
				}
				c.Status = Absent
				out = append(out, c)
				continue
			}
			plan(&c, hook, o.Command)
			out = append(out, c)
		}
	}
	return out, nil
}

// plan fills in c's status and, for Add, the bytes before and after.
func plan(c *Change, hook shellguard.ClientHook, command string) {
	b, err := os.ReadFile(c.File)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.mode = 0o644
	case err != nil:
		c.Status, c.Reason = Skip, "can't read it: "+err.Error()
		return
	default:
		c.existed, c.before = true, b
		if st, err := os.Stat(c.File); err == nil {
			c.mode = st.Mode().Perm()
		}
	}
	var root *object
	indent := "  "
	if len(strings.TrimSpace(string(c.before))) == 0 {
		root = newObject()
	} else {
		v, err := parse(c.before)
		if err != nil {
			c.Status, c.Reason = Skip, "it isn't plain JSON (comments or a syntax error?)"
			return
		}
		o, ok := v.(*object)
		if !ok {
			c.Status, c.Reason = Skip, "it isn't a JSON object"
			return
		}
		root, indent = o, indentOf(c.before)
	}
	want, err := snippet(hook, command)
	if err != nil {
		c.Status, c.Reason = Skip, err.Error()
		return
	}
	changed, present, why := merge(root, want, guardMatcher(command))
	switch {
	case why != "":
		c.Status, c.Reason = Skip, why
	case present:
		c.Status = Present
	case changed:
		c.Status, c.after = Add, render(root, indent)
	default:
		c.Status = Present
	}
}

// snippet is the client's documented hook config with "deedglass guard shell" started by command.
func snippet(hook shellguard.ClientHook, command string) (*object, error) {
	v, err := parse([]byte(hook.Config))
	if err != nil {
		return nil, fmt.Errorf("protect: %s's hook config: %w", hook.Name, err)
	}
	v = mapStrings(v, func(s string) string { return strings.Replace(s, "deedglass guard shell", command+" guard shell", 1) })
	return v.(*object), nil
}

// merge adds want's hooks to root. Top-level keys other than "hooks" (Cursor's and Copilot's
// "version") are set only when absent. present: a guard is already under every event want names.
func merge(root, want *object, isGuard func(string) bool) (changed, present bool, why string) {
	wantHooks, _ := want.vals["hooks"].(*object)
	if wantHooks == nil {
		return false, false, "protect: hook config without hooks"
	}
	var hooks *object
	if v, ok := root.get("hooks"); ok {
		h, isObj := v.(*object)
		if !isObj {
			return false, false, `its "hooks" isn't an object`
		}
		hooks = h
	}
	present = true
	for _, ev := range wantHooks.keys {
		if hooks == nil {
			present = false
			break
		}
		v, ok := hooks.get(ev)
		if !ok {
			present = false
			break
		}
		list, isList := v.([]any)
		if !isList {
			return false, false, fmt.Sprintf("its %q hooks aren't a list", ev)
		}
		found := false
		eachString(list, func(s string) { found = found || isGuard(s) })
		if !found {
			present = false
		}
	}
	if present {
		return false, true, ""
	}
	for _, k := range want.keys {
		if k == "hooks" {
			continue
		}
		if _, ok := root.get(k); !ok {
			root.set(k, want.vals[k])
		}
	}
	if hooks == nil {
		hooks = newObject()
		root.set("hooks", hooks)
	}
	for _, ev := range wantHooks.keys {
		add := wantHooks.vals[ev].([]any)
		cur, _ := hooks.get(ev)
		list, _ := cur.([]any)
		found := false
		eachString(list, func(s string) { found = found || isGuard(s) })
		if found {
			continue
		}
		hooks.set(ev, append(list, add...))
	}
	return true, false, ""
}

// Manifest records one protect run, so Undo can put every file back.
type Manifest struct {
	Version int     `json:"version"`
	Created string  `json:"created"`
	Command string  `json:"command"`
	Entries []Entry `json:"entries"`
}

// Entry is one file a run changed.
type Entry struct {
	Client  string   `json:"client"`
	Scope   string   `json:"scope"`
	File    string   `json:"file"`
	Event   string   `json:"event"`
	Existed bool     `json:"existed"`
	Mode    uint32   `json:"mode"`
	Backup  string   `json:"backup,omitempty"` // file name in the run's directory
	After   string   `json:"after_sha256"`
	NewDirs []string `json:"new_dirs,omitempty"` // directories this run created, outermost first
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Apply writes every Add change. Backups and the manifest are written first, so a run that stops
// half way can still be undone. It returns the run's directory ("" when nothing changed).
func Apply(o Options, cs []Change) (string, error) {
	var adds []*Change
	for i := range cs {
		if cs[i].Status == Add {
			adds = append(adds, &cs[i])
		}
	}
	if len(adds) == 0 {
		return "", nil
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	t := now().UTC()
	run := filepath.Join(StateDir(o.Home), t.Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(run, 0o700); err != nil {
		return "", err
	}
	m := Manifest{Version: 1, Created: t.Format(time.RFC3339), Command: o.Command}
	for i, c := range adds {
		e := Entry{Client: c.Client, Scope: c.Scope, File: c.File, Event: c.Event, Existed: c.existed, Mode: uint32(c.mode), After: sum(c.after)}
		if c.existed {
			e.Backup = fmt.Sprintf("%02d-%s-%s.json", i+1, c.Client, c.Scope)
			if err := os.WriteFile(filepath.Join(run, e.Backup), c.before, 0o600); err != nil {
				return run, err
			}
		}
		e.NewDirs = missingDirs(filepath.Dir(c.File))
		m.Entries = append(m.Entries, e)
	}
	if err := writeManifest(filepath.Join(run, "manifest.json"), m); err != nil {
		return run, err
	}
	for i, c := range adds {
		if err := os.MkdirAll(filepath.Dir(c.File), 0o755); err != nil {
			return run, err
		}
		if err := writeAtomic(c.File, c.after, c.mode); err != nil {
			return run, fmt.Errorf("%s: %w (entries before it were written; deedglass protect --undo puts them back)", m.Entries[i].File, err)
		}
	}
	return run, nil
}

// missingDirs lists dir and its parents that don't exist yet, outermost first.
func missingDirs(dir string) []string {
	var out []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil || filepath.Dir(d) == d {
			break
		}
		out = append([]string{d}, out...)
	}
	return out
}

func writeManifest(path string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// writeAtomic replaces name's content through a temporary file in the same directory. A symlink is
// followed, so a dotfiles checkout keeps its link and gets the change.
func writeAtomic(name string, b []byte, mode fs.FileMode) error {
	if r, err := filepath.EvalSymlinks(name); err == nil {
		name = r
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".deedglass-protect-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

// UndoStatus is what Undo did, or would do, to one file.
type UndoStatus string

const (
	Restored UndoStatus = "restored" // the file is back byte for byte (or gone, if protect created it)
	Removed  UndoStatus = "removed"  // changed since: only Deedglass's entries were taken out
	Left     UndoStatus = "left"     // nothing to do (missing, or no Deedglass entry left)
	Failed   UndoStatus = "failed"
)

// Undone is one file Undo handled.
type Undone struct {
	Client string     `json:"client"`
	Scope  string     `json:"scope"`
	File   string     `json:"file"`
	Status UndoStatus `json:"status"`
	Reason string     `json:"reason,omitempty"`
}

// Undo reverses every run not yet undone, newest first. With dry, it writes nothing.
func Undo(o Options, dry bool) ([]Undone, error) {
	dir := StateDir(o.Home)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []string
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "manifest.json")); err == nil {
				runs = append(runs, e.Name())
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(runs)))
	var out []Undone
	for _, r := range runs {
		run := filepath.Join(dir, r)
		b, err := os.ReadFile(filepath.Join(run, "manifest.json"))
		if err != nil {
			return out, err
		}
		var m Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return out, fmt.Errorf("%s: %w", filepath.Join(run, "manifest.json"), err)
		}
		failed := false
		for i := len(m.Entries) - 1; i >= 0; i-- {
			u := undoEntry(run, m.Entries[i], guardMatcher(m.Command), dry)
			failed = failed || u.Status == Failed
			out = append(out, u)
		}
		if !dry && !failed {
			if err := os.Rename(filepath.Join(run, "manifest.json"), filepath.Join(run, "undone.json")); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func undoEntry(run string, e Entry, isGuard func(string) bool, dry bool) Undone {
	u := Undone{Client: e.Client, Scope: e.Scope, File: e.File}
	cur, err := os.ReadFile(e.File)
	if errors.Is(err, fs.ErrNotExist) {
		u.Status, u.Reason = Left, "the file is gone; nothing to put back"
		return u
	}
	if err != nil {
		u.Status, u.Reason = Failed, err.Error()
		return u
	}
	if sum(cur) == e.After {
		u.Status = Restored
		if dry {
			return u
		}
		if !e.Existed {
			if err := os.Remove(e.File); err != nil {
				u.Status, u.Reason = Failed, err.Error()
				return u
			}
			for i := len(e.NewDirs) - 1; i >= 0; i-- {
				_ = os.Remove(e.NewDirs[i]) // only if empty
			}
			return u
		}
		b, err := os.ReadFile(filepath.Join(run, e.Backup))
		if err != nil {
			u.Status, u.Reason = Failed, "the backup is missing: "+err.Error()
			return u
		}
		if err := writeAtomic(e.File, b, fs.FileMode(e.Mode)); err != nil {
			u.Status, u.Reason = Failed, err.Error()
		}
		return u
	}
	// Changed since protect wrote it: keep the person's edits and take out only the guard.
	v, err := parse(cur)
	root, ok := v.(*object)
	if err != nil || !ok {
		u.Status, u.Reason = Failed, "it changed since and isn't plain JSON now; remove the Deedglass hook by hand"
		return u
	}
	if !removeGuard(root, e.Event, isGuard) {
		u.Status, u.Reason = Left, "no Deedglass hook left in it"
		return u
	}
	u.Status, u.Reason = Removed, "it changed since, so only the Deedglass hook was taken out"
	if dry {
		return u
	}
	st, _ := os.Stat(e.File)
	mode := fs.FileMode(0o644)
	if st != nil {
		mode = st.Mode().Perm()
	}
	if err := writeAtomic(e.File, render(root, indentOf(cur)), mode); err != nil {
		u.Status, u.Reason = Failed, err.Error()
	}
	return u
}

// removeGuard takes every guard entry out of root's hooks[event]: a flat entry whose command runs
// the guard, or such a command inside a group's "hooks" (dropping the group when it empties). An
// emptied event list, and an emptied hooks object, go too.
func removeGuard(root *object, event string, isGuard func(string) bool) bool {
	hv, _ := root.get("hooks")
	hooks, _ := hv.(*object)
	if hooks == nil {
		return false
	}
	lv, _ := hooks.get(event)
	list, _ := lv.([]any)
	removed := false
	var keep []any
	for _, item := range list {
		if g, ok := item.(*object); ok {
			if inner, ok := g.vals["hooks"].([]any); ok {
				var k2 []any
				for _, h := range inner {
					guard := false
					eachString(h, func(s string) { guard = guard || isGuard(s) })
					if guard {
						removed = true
						continue
					}
					k2 = append(k2, h)
				}
				if len(k2) == 0 && len(inner) > 0 {
					continue
				}
				if len(k2) != len(inner) {
					g.set("hooks", k2)
				}
				keep = append(keep, g)
				continue
			}
		}
		guard := false
		eachString(item, func(s string) { guard = guard || isGuard(s) })
		if guard {
			removed = true
			continue
		}
		keep = append(keep, item)
	}
	if !removed {
		return false
	}
	if len(keep) == 0 {
		hooks.del(event)
	} else {
		hooks.set(event, keep)
	}
	if len(hooks.keys) == 0 {
		root.del("hooks")
	}
	return true
}

// HookCommand says how a hook should start deedglass, so it still works after this process ends:
//   - the deedglass on PATH, when it is this binary (or the npm wrapper for it): stable across upgrades
//   - after an npx run (the binary sits in npm's _npx cache): npx -y deedglass@<version>
//   - otherwise this binary's absolute path
//
// exe is os.Executable(); onPath is exec.LookPath("deedglass") ("" when absent); version is the
// release version ("" or a -dev version when built from source).
func HookCommand(exe, onPath, version string) string {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	slash := filepath.ToSlash(exe)
	npx := strings.Contains(slash, "/_npx/")
	if onPath != "" && !npx {
		rp := real(onPath)
		if rp == real(exe) || strings.HasSuffix(filepath.ToSlash(rp), "/deedglass/bin/deedglass.js") {
			return shellQuote(onPath)
		}
	}
	release := version != "" && !strings.Contains(version, "-") && !strings.Contains(version, "+")
	if npx || strings.Contains(slash, "/go-build") {
		if release {
			return "npx -y deedglass@" + strings.TrimPrefix(version, "v")
		}
		return "deedglass"
	}
	return shellQuote(exe)
}

// shellQuote quotes p for sh when it holds anything but a plain path.
func shellQuote(p string) string {
	if p != "" && strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+@:") == "" {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

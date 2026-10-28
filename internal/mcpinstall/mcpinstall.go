// Package mcpinstall adds Deedglass to the MCP settings file of an AI tool on this computer, and
// takes it out again. The Connect screen (DL-143) uses it for the one-click Connect button.
//
// Rules: it only writes after a person clicks, and only the one entry (the tool's "mcpServers" or
// "servers" object gets a "deedglass" key). Every other key keeps its place, order and indent. Before
// it changes a file it copies the file to <file>.<time>.bak beside it and writes a manifest under
// ~/.deedglass/mcp, so Undo can put the file back exactly as it was; when the person has changed
// the file since, Undo removes only the Deedglass entry. A file it can't read as plain JSON is left
// alone. No network, no server, no key: the entry runs deedglass, which finds its own key file.
package mcpinstall

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/orderedjson"
)

// Name is the key the entry is written under.
const Name = "deedglass"

// Target ids.
const (
	ClaudeDesktop = "claude-desktop"
	Cursor        = "cursor"
	VSCode        = "vscode"
	Gemini        = "gemini"
)

// Target is one tool whose settings file mcpinstall knows.
type Target struct {
	ID    string
	Title string
	Key   string // the object the entry goes in: mcpServers, or servers for VS Code
	// typed says the entry carries "type": "stdio" (VS Code's format).
	typed bool
	// dir and file, under the person's home (or the Windows roaming folder), per GOOS.
	dir  func(l layout) string
	file func(l layout) string
}

type layout struct {
	home, goos, appData string
}

func (l layout) appSupport(name string) string {
	switch l.goos {
	case "darwin":
		return filepath.Join(l.home, "Library", "Application Support", name)
	case "windows":
		if l.appData != "" {
			return filepath.Join(l.appData, name)
		}
		return filepath.Join(l.home, "AppData", "Roaming", name)
	}
	return filepath.Join(l.home, ".config", name)
}

// Targets are the tools, in the order the Connect screen shows them.
var Targets = []Target{
	{ID: ClaudeDesktop, Title: "Claude Desktop", Key: "mcpServers",
		dir:  func(l layout) string { return l.appSupport("Claude") },
		file: func(l layout) string { return filepath.Join(l.appSupport("Claude"), "claude_desktop_config.json") }},
	{ID: Cursor, Title: "Cursor", Key: "mcpServers",
		dir:  func(l layout) string { return filepath.Join(l.home, ".cursor") },
		file: func(l layout) string { return filepath.Join(l.home, ".cursor", "mcp.json") }},
	{ID: VSCode, Title: "VS Code", Key: "servers", typed: true,
		dir:  func(l layout) string { return filepath.Join(l.appSupport("Code"), "User") },
		file: func(l layout) string { return filepath.Join(l.appSupport("Code"), "User", "mcp.json") }},
	{ID: Gemini, Title: "Gemini CLI", Key: "mcpServers",
		dir:  func(l layout) string { return filepath.Join(l.home, ".gemini") },
		file: func(l layout) string { return filepath.Join(l.home, ".gemini", "settings.json") }},
}

// Lookup finds a target by id.
func Lookup(id string) (Target, bool) {
	for _, t := range Targets {
		if t.ID == id {
			return t, true
		}
	}
	return Target{}, false
}

// Options says where the person's files are and what the entry runs.
type Options struct {
	Home    string // the person's home directory
	GOOS    string // runtime.GOOS when empty
	AppData string // Windows: %APPDATA%; else ignored
	Command string // how the tool starts deedglass: "npx" or the binary's path
	Args    []string
	Now     func() time.Time
}

func (o Options) layout() layout {
	g := o.GOOS
	if g == "" {
		g = runtime.GOOS
	}
	return layout{home: o.Home, goos: g, appData: o.AppData}
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Status is what Connect does, or would do, to a tool's settings file.
type Status string

const (
	Add     Status = "add"     // the entry goes in
	Update  Status = "update"  // a deedglass entry is there but runs something else; it gets replaced
	Present Status = "present" // the entry is already there, as it should be
	Skip    Status = "skip"    // the file can't be read as JSON (or has an unexpected shape); left alone
	Absent  Status = "absent"  // the tool's folder isn't on this computer
)

// Change is one tool's settings file and what Connect does to it.
type Change struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	File      string `json:"file"`
	Installed bool   `json:"installed"` // the tool's folder is here
	Status    Status `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Entry     string `json:"entry"` // the JSON Connect adds, as the person sees it before clicking
	// Backup is the newest backup Connect made for this file, when an Undo is possible.
	Backup  string `json:"backup,omitempty"`
	CanUndo bool   `json:"can_undo"`

	existed bool
	mode    fs.FileMode
	before  []byte
	after   []byte
}

// StateDir is where manifests live.
func StateDir(home string) string { return filepath.Join(home, ".deedglass", "mcp") }

// entry is the server object the tool runs.
func entry(t Target, o Options) *orderedjson.Object {
	e := orderedjson.New()
	if t.typed {
		e.Set("type", "stdio")
	}
	e.Set("command", o.Command)
	args := make([]any, len(o.Args))
	for i, a := range o.Args {
		args[i] = a
	}
	e.Set("args", args)
	return e
}

// Plan works out what Connect would do to one tool's file, writing nothing.
func Plan(o Options, id string) (Change, error) {
	t, ok := Lookup(id)
	if !ok {
		return Change{}, fmt.Errorf("mcpinstall: no tool %q", id)
	}
	if o.Command == "" {
		return Change{}, errors.New("mcpinstall: no command")
	}
	l := o.layout()
	c := Change{ID: t.ID, Title: t.Title, File: t.file(l)}
	want := entry(t, o)
	wrap := orderedjson.New()
	wrap.Set(Name, want)
	c.Entry = string(bytes.TrimRight(orderedjson.Render(wrap, "  "), "\n"))
	c.Backup, c.CanUndo = latestBackup(o, t.ID)
	if st, err := os.Stat(t.dir(l)); err != nil || !st.IsDir() {
		c.Status = Absent
		return c, nil
	}
	c.Installed = true
	plan(&c, t, want)
	return c, nil
}

func plan(c *Change, t Target, want *orderedjson.Object) {
	b, err := os.ReadFile(c.File)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.mode = 0o600
	case err != nil:
		c.Status, c.Reason = Skip, "can't read it: "+err.Error()
		return
	default:
		c.existed, c.before = true, b
		if st, err := os.Stat(c.File); err == nil {
			c.mode = st.Mode().Perm()
		}
	}
	var root *orderedjson.Object
	indent := "  "
	if len(strings.TrimSpace(string(c.before))) == 0 {
		root = orderedjson.New()
	} else {
		v, err := orderedjson.Parse(c.before)
		if err != nil {
			c.Status, c.Reason = Skip, "it isn't plain JSON (comments or a syntax error?)"
			return
		}
		o, ok := v.(*orderedjson.Object)
		if !ok {
			c.Status, c.Reason = Skip, "it isn't a JSON object"
			return
		}
		root, indent = o, orderedjson.IndentOf(c.before)
	}
	var servers *orderedjson.Object
	if v, ok := root.Get(t.Key); ok {
		s, isObj := v.(*orderedjson.Object)
		if !isObj {
			c.Status, c.Reason = Skip, fmt.Sprintf("its %q isn't an object", t.Key)
			return
		}
		servers = s
	} else {
		servers = orderedjson.New()
		root.Set(t.Key, servers)
	}
	c.Status = Add
	if cur, ok := servers.Get(Name); ok {
		if bytes.Equal(orderedjson.Render(cur, " "), orderedjson.Render(want, " ")) {
			c.Status = Present
			return
		}
		c.Status = Update
	}
	servers.Set(Name, want)
	c.after = orderedjson.Render(root, indent)
}

// Manifest records one write, so Undo can put the file back.
type Manifest struct {
	Version int      `json:"version"`
	Created string   `json:"created"`
	ID      string   `json:"id"`
	File    string   `json:"file"`
	Existed bool     `json:"existed"`
	Mode    uint32   `json:"mode"`
	Backup  string   `json:"backup,omitempty"` // the .bak beside the file
	Before  string   `json:"before_sha256,omitempty"`
	After   string   `json:"after_sha256"`
	NewDirs []string `json:"new_dirs,omitempty"`
	Undone  bool     `json:"undone"`
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Result is what Apply wrote.
type Result struct {
	File   string `json:"file"`
	Backup string `json:"backup,omitempty"`
	Status Status `json:"status"`
}

// Apply writes the change: the backup and manifest first, then the file. Present, Skip and Absent
// write nothing.
func Apply(o Options, c Change) (Result, error) {
	r := Result{File: c.File, Status: c.Status}
	if c.Status != Add && c.Status != Update {
		return r, nil
	}
	if o.Home == "" {
		return r, errors.New("mcpinstall: no home directory")
	}
	t := o.now()
	stamp := t.Format("20060102T150405.000000000Z")
	m := Manifest{Version: 1, Created: t.Format(time.RFC3339Nano), ID: c.ID, File: c.File, Existed: c.existed, Mode: uint32(c.mode), After: sum(c.after)}
	if c.existed {
		m.Before = sum(c.before)
		m.Backup = c.File + "." + stamp + ".bak"
		if err := os.WriteFile(m.Backup, c.before, 0o600); err != nil {
			return r, fmt.Errorf("couldn't write the backup: %w", err)
		}
		r.Backup = m.Backup
	}
	m.NewDirs = missingDirs(filepath.Dir(c.File))
	run := filepath.Join(StateDir(o.Home), c.ID, stamp)
	if err := os.MkdirAll(run, 0o700); err != nil {
		return r, err
	}
	if err := writeManifest(filepath.Join(run, "manifest.json"), m); err != nil {
		return r, err
	}
	if err := os.MkdirAll(filepath.Dir(c.File), 0o755); err != nil {
		return r, err
	}
	if err := writeAtomic(c.File, c.after, c.mode); err != nil {
		return r, err
	}
	return r, nil
}

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
	f, err := os.CreateTemp(filepath.Dir(name), ".deedglass-mcp-*")
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

// runs lists a target's manifest directories, newest first.
func runs(o Options, id string) []string {
	dir := filepath.Join(StateDir(o.Home), id)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "manifest.json")); err == nil {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

func readManifest(run string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(run, "manifest.json"))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// latestBackup is the newest run not yet undone: its backup path and whether Undo has work.
func latestBackup(o Options, id string) (string, bool) {
	if o.Home == "" {
		return "", false
	}
	for _, run := range runs(o, id) {
		m, err := readManifest(run)
		if err != nil || m.Undone {
			continue
		}
		return m.Backup, true
	}
	return "", false
}

// UndoStatus is what Undo did to the file.
type UndoStatus string

const (
	Restored UndoStatus = "restored" // the file is back byte for byte (or gone, if Connect created it)
	Removed  UndoStatus = "removed"  // changed since: only the Deedglass entry was taken out
	Left     UndoStatus = "left"     // nothing to undo
	Failed   UndoStatus = "failed"
)

// Undone is what Undo did.
type Undone struct {
	ID     string     `json:"id"`
	File   string     `json:"file"`
	Status UndoStatus `json:"status"`
	Reason string     `json:"reason,omitempty"`
}

// Undo reverses the newest write to a tool's file that isn't undone yet.
func Undo(o Options, id string) (Undone, error) {
	t, ok := Lookup(id)
	if !ok {
		return Undone{}, fmt.Errorf("mcpinstall: no tool %q", id)
	}
	u := Undone{ID: id, File: t.file(o.layout()), Status: Left}
	for _, run := range runs(o, id) {
		m, err := readManifest(run)
		if err != nil || m.Undone {
			continue
		}
		u.File = m.File
		undo(&u, t, m)
		if u.Status == Failed {
			return u, nil
		}
		m.Undone = true
		if err := writeManifest(filepath.Join(run, "manifest.json"), m); err != nil {
			return u, err
		}
		return u, nil
	}
	return u, nil
}

func undo(u *Undone, t Target, m Manifest) {
	cur, err := os.ReadFile(m.File)
	if errors.Is(err, fs.ErrNotExist) {
		u.Status = Left
		return
	}
	if err != nil {
		u.Status, u.Reason = Failed, err.Error()
		return
	}
	mode := fs.FileMode(m.Mode)
	if mode == 0 {
		mode = 0o600
	}
	if sum(cur) == m.After {
		if !m.Existed {
			if err := os.Remove(m.File); err != nil {
				u.Status, u.Reason = Failed, err.Error()
				return
			}
			for i := len(m.NewDirs) - 1; i >= 0; i-- {
				_ = os.Remove(m.NewDirs[i]) // only when empty
			}
			u.Status = Restored
			return
		}
		before, err := os.ReadFile(m.Backup)
		if err == nil && sum(before) == m.Before {
			if err := writeAtomic(m.File, before, mode); err != nil {
				u.Status, u.Reason = Failed, err.Error()
				return
			}
			u.Status = Restored
			return
		}
		// The backup is gone or changed: fall through and take the entry out.
	}
	v, err := orderedjson.Parse(cur)
	if err != nil {
		u.Status, u.Reason = Failed, "the file isn't plain JSON any more, so the entry was left in place"
		return
	}
	root, ok := v.(*orderedjson.Object)
	if !ok {
		u.Status, u.Reason = Failed, "the file isn't a JSON object any more"
		return
	}
	sv, _ := root.Get(t.Key)
	servers, _ := sv.(*orderedjson.Object)
	if servers == nil {
		u.Status = Left
		return
	}
	if _, ok := servers.Get(Name); !ok {
		u.Status = Left
		return
	}
	servers.Del(Name)
	if servers.Len() == 0 {
		root.Del(t.Key)
	}
	if err := writeAtomic(m.File, orderedjson.Render(root, orderedjson.IndentOf(cur)), mode); err != nil {
		u.Status, u.Reason = Failed, err.Error()
		return
	}
	u.Status = Removed
}

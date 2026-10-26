// The two functions behind deedglass.com/check, kept free of syscall/js so the parity test runs
// them on the host.

package main

import (
	"context"
	"encoding/json"
	"testing/fstest"

	"github.com/deedglass/deedglass/internal/scan"
	"github.com/deedglass/deedglass/internal/shellguard"
)

// version is set by the Makefile (-ldflags "-X main.version=…").
var version = "dev"

// files are the names the page offers; each is where the CLI's scan looks for that client's config.
var files = map[string]bool{".cursor/mcp.json": true, ".vscode/mcp.json": true, ".mcp.json": true,
	"claude_desktop_config.json": true, "mcp_config.json": true, ".claude/settings.json": true, "package.json": true}

// ScanConfig scans a directory that holds only the pasted file.
func ScanConfig(text, name string) string {
	if !files[name] {
		return errJSON("unknown file name: " + name)
	}
	if len(text) > 1<<20 {
		return errJSON("the file is over 1 MB")
	}
	fsys := fstest.MapFS{name: &fstest.MapFile{Data: []byte(text), Mode: 0o644}}
	r, err := scan.Scan(context.Background(), scan.Options{FS: fsys, Dir: "/check", DirLabel: ".", MaxFiles: 10, GOOS: "js", Version: version})
	if err != nil {
		return errJSON(err.Error())
	}
	return toJSON(r)
}

// CheckCommand is guard shell --no-files --command … --json.
func CheckCommand(command string) string {
	fs := shellguard.Check(command, shellguard.Options{})
	if fs == nil {
		fs = []shellguard.Finding{}
	}
	decision := "none"
	if len(fs) > 0 {
		decision = "ask"
	}
	return toJSON(map[string]any{"decision": decision, "reason": shellguard.Reason(fs, false), "findings": fs})
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return errJSON(err.Error())
	}
	return string(b)
}

func errJSON(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

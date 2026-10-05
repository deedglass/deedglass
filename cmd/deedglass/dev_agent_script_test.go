package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// S37 built the last unbuilt command, so the not-built path keeps its own test.
func TestNotBuiltMessage(t *testing.T) {
	var out, errOut bytes.Buffer
	e := &Env{Stdout: &out, Stderr: &errOut, Getenv: func(string) string { return "" }}
	code := report(e, notBuilt(&Command{Path: []string{"dev", "future"}}))
	if code != ExitUsage || !strings.Contains(errOut.String(), "deedglass dev future isn't in this build yet.") ||
		!strings.Contains(errOut.String(), "Run deedglass help to see what this build can do.") {
		t.Fatalf("exit %d\n%s", code, errOut.String())
	}
}

func TestAgentScriptRefusesAtLoad(t *testing.T) {
	var out, errOut bytes.Buffer
	dir := t.TempDir()
	f := dir + "/bad.yaml"
	writeTestFile(t, f, "deedglass: agent-script/v1\norg: acme-demo\nagent: claude-code\nscenes:\n  - id: x\n    title: X\n    steps:\n      - make: demo-down\n")
	e := &Env{Stdout: &out, Stderr: &errOut, Getenv: func(string) string { return "" }}
	code := run(t.Context(), e, []string{"--no-color", "dev", "agent-script", f})
	if code != ExitUsage || !strings.Contains(errOut.String(), `make "demo-down" isn't allowed`) {
		t.Fatalf("exit %d\n%s%s", code, out.String(), errOut.String())
	}
}

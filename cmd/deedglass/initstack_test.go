package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/setup"
)

// TestWithInitStack: in a directory deedglass init set up, the DB-direct commands find the runtime
// role's database URL, the seal key and the listen address; anything the environment sets wins.
func TestWithInitStack(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".deedglass"), 0o700); err != nil {
		t.Fatal(err)
	}
	st := setup.Stack{Object: "local_stack", Version: 1, PGPort: 55999, Listen: "127.0.0.1:18999", AppPassword: "pw", SealKeyFile: "/k/seal.key"}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, ".deedglass", "stack.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	opts, got, err := withInitStack(config.LoadOptions{Dir: dir, Env: map[string]string{"DEEDGLASS_SEAL_KEY_FILE": "/mine.key"}})
	if err != nil || got == nil {
		t.Fatalf("stack %v, err %v", got, err)
	}
	if opts.Env["DEEDGLASS_DATABASE_URL"] != st.AppURL() || opts.Env["DEEDGLASS_ENV"] != "dev" || opts.Env["DEEDGLASS_LISTEN"] != "127.0.0.1:18999" {
		t.Fatalf("env %v", opts.Env)
	}
	if opts.Env["DEEDGLASS_SEAL_KEY_FILE"] != "/mine.key" {
		t.Fatal("the environment's own setting must win")
	}
	opts, got, err = withInitStack(config.LoadOptions{Dir: t.TempDir()})
	if err != nil || got != nil || len(opts.Env) != 0 {
		t.Fatalf("no stack: %v %v %v", opts.Env, got, err)
	}
}

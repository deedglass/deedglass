//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/localapi"
	"github.com/deedglass/deedglass/internal/localstore"
)

// TestAppLive is the local app end to end (DL-143): a fresh data folder and no Docker,
// deedglass app starts the store and serve, the API answers with the app's admin key, Connect
// makes a key file, app --stop stops everything, and a second start keeps the data. It uses an
// installed PostgreSQL 16+ (or DEEDGLASS_PG_BIN); with none it is skipped rather than downloading.
func TestAppLive(t *testing.T) {
	if _, err := localstore.New(t.TempDir(), os.Getenv).Binaries(context.Background()); errors.Is(err, localstore.NeedDownload) {
		t.Skip("no PostgreSQL 16+ on this computer (set DEEDGLASS_PG_BIN)")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "deedglass")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := filepath.Join(tmp, "app data")
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "DEEDGLASS_APP_DIR="+dir, "HOME="+home, "DEEDGLASS_NO_NETWORK=1", "NO_COLOR=1")
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command(bin, args...)
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("deedglass %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	start := func() (*exec.Cmd, string) {
		t.Helper()
		c := exec.Command(bin, "app", "--no-open")
		c.Env = env
		out, _ := c.StdoutPipe()
		c.Stderr = c.Stdout
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cs := exec.Command(bin, "app", "--stop")
			cs.Env = env
			_ = cs.Run()
			_ = c.Wait()
		})
		lines := make(chan string, 64)
		go func() {
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
		}()
		deadline := time.After(3 * time.Minute)
		var seen []string
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("deedglass app stopped:\n%s", strings.Join(seen, "\n"))
				}
				seen = append(seen, l)
				if strings.Contains(l, "deedglass help") { // the third of the three lines
					go func() {
						for range lines {
						}
					}()
					st := readAppState(dir)
					if st == nil {
						t.Fatal("no app.json")
					}
					return c, st.Server
				}
			case <-deadline:
				t.Fatalf("deedglass app didn't start in 3 minutes:\n%s", strings.Join(seen, "\n"))
			}
		}
	}
	key := func() string {
		b, err := os.ReadFile(filepath.Join(dir, appAdminKey))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	call := func(server, method, path, body string) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, server+path, rd)
		req.Header.Set("Authorization", "Bearer "+key())
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}

	_, server := start()
	if code, m := call(server, "GET", "/v1/local", ""); code != 200 || m["first_run"] != true || m["local"] != true {
		t.Fatalf("GET /v1/local: %d %v", code, m)
	}
	if code, m := call(server, "PATCH", "/v1/local", `{"path":"build","first_run":false}`); code != 200 || m["path"] != "build" {
		t.Fatalf("PATCH /v1/local: %d %v", code, m)
	}
	code, m := call(server, "GET", "/v1/connect", "")
	if code != 200 || len(m["data"].([]any)) != len(localapi.Clients) {
		t.Fatalf("GET /v1/connect: %d %v", code, m)
	}
	code, m = call(server, "POST", "/v1/connect/cursor/start", "")
	if code != 200 || m["status"] != "waiting" || m["agent"] == nil {
		t.Fatalf("POST /v1/connect/cursor/start: %d %v", code, m)
	}
	if st, err := os.Stat(localapi.AgentKeyPath(dir, "cursor")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("cursor's key file: %v %v", st, err)
	}
	if code, m2 := call(server, "POST", "/v1/connect/cursor/start", ""); code != 200 || m2["agent"] != m["agent"] {
		t.Fatalf("start again must change nothing: %d %v", code, m2)
	}
	// The CLI reaches the running app with no flags.
	if out := run("--json", "agent", "list"); !strings.Contains(out, `"cursor"`) {
		t.Fatalf("agent list against the app:\n%s", out)
	}

	run("app", "--stop")
	if out := run("--json", "app", "--status"); !strings.Contains(out, `"running":false`) || !strings.Contains(out, `"database_running":false`) {
		t.Fatalf("after --stop:\n%s", out)
	}

	_, server = start()
	if code, m := call(server, "GET", "/v1/local", ""); code != 200 || m["first_run"] != false || m["path"] != "build" {
		t.Fatalf("after a restart, local state: %d %v", code, m)
	}
	if out := run("--json", "agent", "list"); !strings.Contains(out, `"cursor"`) {
		t.Fatalf("after a restart, the agent is gone:\n%s", out)
	}
	run("app", "--stop")
}

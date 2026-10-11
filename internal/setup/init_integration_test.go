//go:build integration

package setup

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInitLive is S32 §6's init table on real Docker (DEEDGLASS_IT_INIT=1; make quickstart-it runs
// it): a clean directory reaches the smoke actions (a read, DROP TABLE denied, an UPDATE witnessed
// at L2); a second run changes nothing; a run killed after the stack started resumes without a
// second org, agent or key; the key never appears in --json; files are 0600 under a 0700 directory.
func TestInitLive(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_INIT") != "1" {
		t.Skip("set DEEDGLASS_IT_INIT=1 to run deedglass init against Docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker isn't on PATH")
	}
	bin := filepath.Join(t.TempDir(), "deedglass")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/deedglass")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%s", out)
	}
	dir := t.TempDir()
	r := &ProcRunner{Dir: dir, Exe: bin}
	o := Options{Dir: dir, Runner: r, Listen: "127.0.0.1:18391", PGPort: 55491, Project: "it-init", NoOpen: true, Yes: true}
	cleanup := func() {
		if st, _ := ReadStack(dir); st != nil {
			if pid := readPID(Path(dir, ServePID)); pid > 0 {
				_ = r.Stop(pid)
			}
			_, _ = r.Docker(context.Background(), nil, "rm", "-f", st.Container, st.DemoContainer)
			_, _ = r.Docker(context.Background(), nil, "volume", "rm", st.Volume, st.DemoVolume)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	ctx := context.Background()

	// A first run stopped after the server step (the kill-and-resume case), then the full run.
	partial := o
	partial.NoSmoke = true
	partial.Progress = func(s StepResult) {}
	t0 := time.Now()
	res, err := Init(ctx, partial)
	if err != nil {
		t.Fatalf("%v %+v", err, res)
	}
	t.Logf("first run (no smoke): %s", time.Since(t0))
	adminBefore, _ := ReadSecretFile(dir, AdminKeyFile)
	keyBefore, _ := ReadSecretFile(dir, AgentKeyFile)
	o.Smoke = true
	res, err = Init(ctx, o)
	if err != nil {
		t.Fatalf("%v %+v", err, res.Steps)
	}
	var detail []string
	for _, s := range res.Steps {
		detail = append(detail, s.ID+": "+s.Detail)
	}
	all := strings.Join(detail, "\n")
	for _, want := range []string{"smoke_read: Claimed · Matches", "smoke_drop: Blocked by policy \"block-destructive-sql\"", "smoke_update: Proven · Matches"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q:\n%s", want, all)
		}
	}
	if a, _ := ReadSecretFile(dir, AdminKeyFile); a != adminBefore {
		t.Fatal("a second run made a new admin key")
	}
	if k, _ := ReadSecretFile(dir, AgentKeyFile); k != keyBefore || !strings.HasPrefix(k, "dg_test_") {
		t.Fatal("a second run made a new agent key, or the key isn't dg_test_")
	}
	// Third run: nothing to change, fast.
	t1 := time.Now()
	res, err = Init(ctx, Options{Dir: dir, Runner: r, NoOpen: true, Yes: true})
	if err != nil || res.Changed {
		t.Fatalf("%v changed=%v %+v", err, res.Changed, res.Steps)
	}
	if d := time.Since(t1); d > 10*time.Second {
		t.Fatalf("a rerun took %s (budget 10 s)", d)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), keyBefore) || strings.Contains(string(b), adminBefore) {
		t.Fatal("a key reached init_result")
	}
	fi, _ := os.Stat(filepath.Join(dir, DirName))
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf(".deedglass mode %o", fi.Mode().Perm())
	}
	_ = filepath.Walk(filepath.Join(dir, DirName), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %o", p, fi.Mode().Perm())
		}
		return nil
	})
	st, _ := ReadState(dir)
	if st.Org == "" || st.Agent == "" || len(st.Policies) != 1 {
		t.Fatalf("%+v", st)
	}
	t.Logf("rerun: %s", time.Since(t1))
}

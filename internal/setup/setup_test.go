package setup

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner fails every Docker call unless told otherwise; Self writes a key file for keys gen.
type fakeRunner struct {
	dockerUp bool
	calls    []string
	free     uint64
}

func (f *fakeRunner) Docker(_ context.Context, _ []byte, args ...string) (string, error) {
	f.calls = append(f.calls, "docker "+strings.Join(args, " "))
	if !f.dockerUp {
		return "", errors.New("Cannot connect to the Docker daemon")
	}
	if args[0] == "info" {
		return "29.8.1\n", nil
	}
	return "", errors.New("no such container")
}

func (f *fakeRunner) Self(_ context.Context, _ map[string]string, args ...string) (string, string, error) {
	f.calls = append(f.calls, "self "+strings.Join(args, " "))
	if len(args) >= 4 && args[0] == "keys" && args[1] == "gen" {
		_ = os.MkdirAll(args[3], 0o700)
		_ = os.WriteFile(filepath.Join(args[3], "sk_01k6h7kp6msmzrhdzwtcr3jnkp.json"), []byte("{}"), 0o600)
	}
	return "", "", nil
}
func (f *fakeRunner) StartServe(context.Context, map[string]string, string) (int, error) {
	return 0, errors.New("no")
}
func (f *fakeRunner) Alive(int) bool                       { return false }
func (f *fakeRunner) Stop(int) error                       { return nil }
func (f *fakeRunner) Healthy(context.Context, string) bool { return false }
func (f *fakeRunner) Open(string) error                    { return nil }
func (f *fakeRunner) FreeDisk(string) (uint64, error)      { return f.free, nil }

func TestDockerNotRunning(t *testing.T) {
	res, err := Init(context.Background(), Options{Dir: t.TempDir(), Runner: &fakeRunner{free: 50 << 30}, PGPort: 55497, Listen: "127.0.0.1:18397"})
	if !errors.Is(err, ErrStep) {
		t.Fatal(err)
	}
	s := res.Steps[len(res.Steps)-1]
	if s.Detail != "Docker isn't running." || s.Fix != "Start Docker Desktop, then run deedglass init again." {
		t.Fatalf("%+v", s)
	}
}

func TestPortInUse(t *testing.T) {
	l, err := listen()
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	_, port, _ := strings.Cut(l.Addr().String(), ":")
	res, _ := Init(context.Background(), Options{Dir: t.TempDir(), Runner: &fakeRunner{dockerUp: true, free: 50 << 30}, Listen: l.Addr().String(), PGPort: 55497})
	s := res.Steps[len(res.Steps)-1]
	if s.Detail != "Port "+port+" is in use by another program." || s.Fix != "Stop it, or run deedglass init --listen 127.0.0.1:8090." {
		t.Fatalf("%+v", s)
	}
}

func TestLowDisk(t *testing.T) {
	res, _ := Init(context.Background(), Options{Dir: t.TempDir(), Runner: &fakeRunner{dockerUp: true, free: 1 << 30}, Listen: "127.0.0.1:18397", PGPort: 55497})
	if s := res.Steps[len(res.Steps)-1]; !strings.Contains(s.Detail, "Deedglass needs 2 GiB") {
		t.Fatalf("%+v", s)
	}
}

func TestStateDirRules(t *testing.T) {
	dir := t.TempDir()
	x := &run{o: Options{Dir: dir}}
	if s := x.stateDir(context.Background()); s.Status != Done {
		t.Fatalf("%+v", s)
	}
	_ = os.Chmod(filepath.Join(dir, DirName), 0o755)
	if s := x.stateDir(context.Background()); s.Status != Changed || !strings.Contains(s.Detail, "was 0755 · fixed to 0700") {
		t.Fatalf("%+v", s)
	}
	if fi, _ := os.Stat(filepath.Join(dir, DirName)); fi.Mode().Perm() != 0o700 {
		t.Fatal(fi.Mode())
	}
	if s := x.stateDir(context.Background()); s.Status != Skipped {
		t.Fatalf("%+v", s)
	}
	link := t.TempDir()
	_ = os.Symlink(t.TempDir(), filepath.Join(link, DirName))
	x = &run{o: Options{Dir: link}}
	if s := x.stateDir(context.Background()); s.Status != Failed || s.Detail != ".deedglass is a symlink. Deedglass keeps keys there, so it must be a real directory." {
		t.Fatalf("%+v", s)
	}
}

func TestGitignore(t *testing.T) {
	dir := t.TempDir()
	if gitignore(dir) {
		t.Fatal("not a git checkout")
	}
	_ = os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/"), 0o644)
	if !gitignore(dir) || gitignore(dir) {
		t.Fatal("adds once")
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(b) != "node_modules/\n# Deedglass local keys and secrets (deedglass init); the badge file may be committed\n.deedglass/*\n!.deedglass/score.json\n" {
		t.Fatalf("%q", b)
	}
	// git agrees: every key file and licence.json stays ignored, score.json doesn't.
	if _, err := exec.LookPath("git"); err == nil {
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("%s", out)
		}
		for f, ignored := range map[string]bool{".deedglass/score.json": false, ".deedglass/secrets.json": true, ".deedglass/licence.json": true, ".deedglass/seal.key": true, ".deedglass/quickstart.key": true} {
			err := exec.Command("git", "-C", dir, "check-ignore", "-q", f).Run()
			if (err == nil) != ignored {
				t.Errorf("git check-ignore %s: ignored=%v, want %v", f, err == nil, ignored)
			}
		}
	}
}

func TestSecretsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	r := &fakeRunner{dockerUp: true, free: 50 << 30}
	x := &run{o: Options{Dir: dir, Runner: r}, r: r}
	x.o.defaults()
	_ = os.Mkdir(filepath.Join(dir, DirName), 0o700)
	if s := x.secrets(context.Background()); s.Status != Done {
		t.Fatalf("%+v", s)
	}
	before := map[string][]byte{}
	for _, f := range []string{SecretsFile, StackFile} {
		b, _ := os.ReadFile(Path(dir, f))
		before[f] = b
		if fi, _ := os.Stat(Path(dir, f)); fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %o", f, fi.Mode().Perm())
		}
	}
	if s := x.secrets(context.Background()); s.Status != Skipped {
		t.Fatalf("%+v", s)
	}
	for f, b := range before {
		after, _ := os.ReadFile(Path(dir, f))
		if string(after) != string(b) {
			t.Fatalf("%s changed on a second run", f)
		}
	}
	st, _ := ReadStack(dir)
	if st.AppPassword == "" || st.DemoPort != 55442 || !strings.HasPrefix(st.Container, "deedglass-local-") {
		t.Fatalf("%+v", st)
	}
	for _, c := range r.calls {
		if strings.Contains(c, st.AppPassword) {
			t.Fatal("a password reached a command line")
		}
	}
}

func TestPlanAndClaudeLine(t *testing.T) {
	create, have := Plan(Options{Dir: t.TempDir()})
	if len(have) != 0 || !strings.Contains(strings.Join(create, ","), "Postgres 18.6 in Docker (127.0.0.1:55432)") {
		t.Fatalf("%v %v", create, have)
	}
	if ClaudeCodeLine("http://127.0.0.1:8080") != `claude mcp add --transport http deedglass http://127.0.0.1:8080/mcp/native --header "Authorization: Bearer $(cat .deedglass/quickstart.key)"` {
		t.Fatal(ClaudeCodeLine("http://127.0.0.1:8080"))
	}
}

func listen() (interface {
	Close() error
	Addr() net.Addr
}, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

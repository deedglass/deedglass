package setup

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
)

// --- the process Runner ---

// ProcRunner is the real Runner: the docker CLI, this binary (Exe, default os.Executable) and
// detached serve processes. Dir is the directory being set up.
type ProcRunner struct {
	Dir string
	Exe string
}

func (p *ProcRunner) exe() (string, error) {
	if p.Exe != "" {
		return p.Exe, nil
	}
	return os.Executable()
}

func (p *ProcRunner) Docker(ctx context.Context, stdin []byte, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != nil {
		cmd.Stdin = strings.NewReader(string(stdin))
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s", firstNonEmpty(strings.TrimSpace(errb.String()), err.Error()))
	}
	return out.String(), nil
}

// baseEnv is what every child gets besides the variables init sets: never the user's DEEDGLASS_*.
func baseEnv(extra map[string]string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR", "USER", "LANG", "TZ"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "NO_COLOR=1", "DEEDGLASS_NO_BANNER=1")
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func (p *ProcRunner) Self(ctx context.Context, extra map[string]string, args ...string) (string, string, error) {
	exe, err := p.exe()
	if err != nil {
		return "", "", err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = p.Dir
	cmd.Env = baseEnv(extra)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if err != nil {
		return out.String(), errb.String(), fmt.Errorf("%s", firstNonEmpty(lastLineOf(errb.String()), err.Error()))
	}
	return out.String(), errb.String(), nil
}

func lastLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func (p *ProcRunner) StartServe(_ context.Context, extra map[string]string, logPath string) (int, error) {
	exe, err := p.exe()
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	cmd := exec.Command(exe, "serve")
	cmd.Dir = p.Dir
	cmd.Env = baseEnv(extra)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func (p *ProcRunner) Alive(pid int) bool {
	proc, err := os.FindProcess(pid)
	return err == nil && proc.Signal(syscall.Signal(0)) == nil
}

func (p *ProcRunner) Stop(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 250 && p.Alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (p *ProcRunner) Healthy(ctx context.Context, server string) bool {
	c := egress.FirstParty(egress.FirstPartyOptions{Timeout: 2 * time.Second})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *ProcRunner) Open(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "linux":
		return exec.Command("xdg-open", url).Start()
	}
	return nil
}

func (p *ProcRunner) FreeDisk(path string) (uint64, error) { return statfsFree(path) }

//go:build unix

package wrap

//deedglass:allow-exec the supervisor's process-group handling for the wrapped MCP server

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group, so shutdown signals reach everything it
// spawned (npx starts node; signalling only npx would leave node running).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, kill bool) {
	if cmd.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

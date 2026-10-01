//go:build !unix

package wrap

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, _ bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

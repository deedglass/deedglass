//go:build !unix

package wrap

//deedglass:allow-exec the supervisor's process handling for the wrapped MCP server

import "os/exec"

func setProcessGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, _ bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

//go:build windows

package cmdrunner

import "os/exec"

func applyProcessGroup(cmd *exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

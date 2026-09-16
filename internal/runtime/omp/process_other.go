//go:build !unix

package omp

import (
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) {}

func terminateProcessGroup(cmd *exec.Cmd, force bool) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if force {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(os.Interrupt)
}

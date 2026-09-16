//go:build unix

package claude

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	if process == nil {
		return nil
	}

	sig, ok := signal.(syscall.Signal)
	if !ok {
		return process.Signal(signal)
	}

	err := syscall.Kill(-process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func gracefulStopSignal() os.Signal {
	return syscall.SIGTERM
}

func osKillSignal() os.Signal {
	return os.Kill
}

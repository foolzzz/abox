//go:build !unix

package claude

import (
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) {}

func signalProcessGroup(process *os.Process, signal os.Signal) error {
	if process == nil {
		return nil
	}
	if signal == os.Kill {
		return process.Kill()
	}
	return process.Signal(signal)
}

func gracefulStopSignal() os.Signal {
	return os.Interrupt
}

func osKillSignal() os.Signal {
	return os.Kill
}

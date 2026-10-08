//go:build !windows

package publisher

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func setupProcess(cmd *exec.Cmd) func() {
	done := make(chan struct{})
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pid := cmd.Process.Pid
		err := syscall.Kill(-pid, syscall.SIGINT)
		// A blocked browser/upload must not keep the publisher busy indefinitely.
		go func() {
			select {
			case <-done:
				return
			case <-time.After(4 * time.Second):
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		}()
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 6 * time.Second
	return func() { close(done) }
}

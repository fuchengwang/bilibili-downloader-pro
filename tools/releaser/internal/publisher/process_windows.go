//go:build windows

package publisher

import (
	"os/exec"
	"strconv"
	"time"
)

func setupProcess(cmd *exec.Cmd) func() {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	}
	cmd.WaitDelay = 6 * time.Second
	return func() {}
}

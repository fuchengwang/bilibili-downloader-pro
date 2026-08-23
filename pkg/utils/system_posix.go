//go:build !windows

package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func openDirectoryOS(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
			cmd = exec.Command("open", "-R", target)
		} else {
			cmd = exec.Command("open", target)
		}
	default:
		if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
			cmd = exec.Command("xdg-open", filepath.Dir(target))
		} else {
			cmd = exec.Command("xdg-open", target)
		}
	}
	return cmd.Start()
}

func openFileOS(path string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", path)
	} else {
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

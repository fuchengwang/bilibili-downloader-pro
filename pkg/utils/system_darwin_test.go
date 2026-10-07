//go:build darwin

package utils

import (
	"errors"
	"os/exec"
	"testing"
)

func TestRunOpenCommandWaitsAndReportsExitFailure(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		code         int
	}{
		{"成功", "exit 0", 0},
		{"已启动但执行失败", "exit 7", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 使用无界面的短命令验证回收与错误反馈，不打开 Finder。
			cmd := exec.Command("/bin/sh", "-c", tc.script)
			err := runOpenCommand(cmd)
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.code {
					t.Fatalf("应返回退出码 %d，实际: %v", tc.code, err)
				}
			}
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != tc.code {
				t.Fatalf("命令应已等待并回收: %v", cmd.ProcessState)
			}
		})
	}
}

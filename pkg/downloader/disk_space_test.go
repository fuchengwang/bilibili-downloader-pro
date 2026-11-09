package downloader

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestDiskSpaceMessages(t *testing.T) {
	for _, err := range []error{
		&os.PathError{Op: "write", Path: "episode403.mp4.downloading", Err: syscall.ENOSPC},
		fmt.Errorf("保存断点: %w", syscall.EDQUOT),
		errors.New("write: There is not enough space on the disk."),
		errors.New("write: 磁盘的空间不足。"),
	} {
		if got := formatFriendlyError(err); got != diskSpaceMessage {
			t.Errorf("download error %v displayed as %q", err, got)
		}
		if got := fileFailureMessage("音视频合成失败", err); got != diskSpaceMessage {
			t.Errorf("merge error %v displayed as %q", err, got)
		}
	}
}

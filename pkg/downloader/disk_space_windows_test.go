package downloader

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsDiskSpaceErrors(t *testing.T) {
	for _, code := range []error{windows.ERROR_DISK_FULL, windows.ERROR_HANDLE_DISK_FULL, windows.ERROR_DISK_QUOTA_EXCEEDED} {
		if got := formatFriendlyError(&os.PathError{Op: "write", Err: code}); got != diskSpaceMessage {
			t.Errorf("native Windows error %v displayed as %q", code, got)
		}
	}
}

//go:build !windows

package instance

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var errAlreadyRunning = errors.New("another instance is already running")

type posixInstanceLock struct {
	file *os.File
}

func (l *posixInstanceLock) Release() error {
	if l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
		l.file = nil
	}
	return nil
}

func tryAcquireSystemLock(configDir string) (*posixInstanceLock, error) {
	lockFilePath := filepath.Join(configDir, "single_instance.flock")
	f, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	// 尝试非阻塞排他锁 LOCK_EX | LOCK_NB
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errAlreadyRunning
	}

	return &posixInstanceLock{file: f}, nil
}

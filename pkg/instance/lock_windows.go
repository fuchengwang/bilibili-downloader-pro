//go:build windows

package instance

import (
	"errors"

	"golang.org/x/sys/windows"
)

var errAlreadyRunning = errors.New("another instance is already running")

type windowsInstanceLock struct {
	handle windows.Handle
}

func (l *windowsInstanceLock) Release() error {
	if l.handle != 0 {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
	}
	return nil
}

func tryAcquireSystemLock(configDir string) (*windowsInstanceLock, error) {
	mutexName, err := windows.UTF16PtrFromString("Local\\BBDownProSingleInstanceMutex_Default")
	if err != nil {
		return nil, err
	}

	h, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
		// 如果返回系统错误且已存在互斥量，CreateMutex 自身已封装 ERROR_ALREADY_EXISTS
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if h != 0 {
				_ = windows.CloseHandle(h)
			}
			return nil, errAlreadyRunning
		}
		return nil, err
	}

	return &windowsInstanceLock{handle: h}, nil
}

package update

import (
	"errors"
	"syscall"
)

func lockResume(path string) (func(), error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, syscall.Errno(32)) {
			return nil, ErrDownloadInProgress
		} // ERROR_SHARING_VIOLATION
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(handle) }, nil
}

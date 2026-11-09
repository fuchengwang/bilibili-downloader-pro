//go:build !windows

package downloader

func isNativeDiskSpaceError(error) bool { return false }

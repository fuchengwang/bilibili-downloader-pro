//go:build windows

package main

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

const (
	dwmwaUseImmersiveDarkMode = 20
	dwmwaBorderColor          = 34
	dwmwaCaptionColor         = 35
	dwmwaColorDefault         = uint32(0xFFFFFFFF)
)

const (
	rdwInvalidate  = 0x0001
	rdwAllChildren = 0x0080
	rdwFrame       = 0x0400
)

const (
	winThemeSystem   uintptr = 0
	winThemeLight    uintptr = 1
	winThemeDark     uintptr = 2
	winThemeMidnight uintptr = 3
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	dwmapi                 = syscall.NewLazyDLL("dwmapi.dll")
	procEnumWindows        = user32.NewProc("EnumWindows")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procRedrawWindow       = user32.NewProc("RedrawWindow")
	procDwmSetWindowAttr   = dwmapi.NewProc("DwmSetWindowAttribute")
	currentProcessID       = uint32(os.Getpid())
	enumCallbackOnce       sync.Once
	enumWindowCallbackPtr  uintptr
)

func colorRef(r, g, b uint8) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

func setDwmAttrUint32(hwnd uintptr, attr uintptr, value uint32) {
	_, _, _ = procDwmSetWindowAttr.Call(
		hwnd,
		attr,
		uintptr(unsafe.Pointer(&value)),
		unsafe.Sizeof(value),
	)
}

func applyWindowThemeAttributes(hwnd, mode uintptr) {
	switch mode {
	case winThemeLight:
		setDwmAttrUint32(hwnd, dwmwaUseImmersiveDarkMode, 0)
		setDwmAttrUint32(hwnd, dwmwaCaptionColor, colorRef(232, 232, 237))
		setDwmAttrUint32(hwnd, dwmwaBorderColor, colorRef(214, 214, 222))
	case winThemeMidnight:
		setDwmAttrUint32(hwnd, dwmwaUseImmersiveDarkMode, 1)
		setDwmAttrUint32(hwnd, dwmwaCaptionColor, colorRef(24, 29, 41))
		setDwmAttrUint32(hwnd, dwmwaBorderColor, colorRef(42, 51, 68))
	case winThemeDark:
		setDwmAttrUint32(hwnd, dwmwaUseImmersiveDarkMode, 1)
		setDwmAttrUint32(hwnd, dwmwaCaptionColor, colorRef(24, 24, 29))
		setDwmAttrUint32(hwnd, dwmwaBorderColor, colorRef(44, 44, 52))
	default:
		setDwmAttrUint32(hwnd, dwmwaCaptionColor, dwmwaColorDefault)
		setDwmAttrUint32(hwnd, dwmwaBorderColor, dwmwaColorDefault)
	}
	// Invalidate the caption and WebView together after changing their frame
	// colors. Let Windows repaint in its normal order without a background erase.
	_, _, _ = procRedrawWindow.Call(hwnd, 0, 0, rdwInvalidate|rdwAllChildren|rdwFrame)
}

func getEnumWindowCallback() uintptr {
	enumCallbackOnce.Do(func() {
		enumWindowCallbackPtr = syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
			visible, _, _ := procIsWindowVisible.Call(hwnd)
			if visible == 0 {
				return 1
			}
			var pid uint32
			_, _, _ = procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
			if pid == currentProcessID {
				applyWindowThemeAttributes(hwnd, lparam)
			}
			return 1
		})
	})
	return enumWindowCallbackPtr
}

func applyNativeWindowAppearance(theme string) {
	mode := winThemeSystem
	switch theme {
	case "light":
		mode = winThemeLight
	case "dark":
		mode = winThemeDark
	case "midnight":
		mode = winThemeMidnight
	}
	_, _, _ = procEnumWindows.Call(getEnumWindowCallback(), mode)
}

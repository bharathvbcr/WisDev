//go:build windows

package cli

import (
	"syscall"
	"unsafe"
)

var (
	kernel32TitleDLL     = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleTitleW = kernel32TitleDLL.NewProc("SetConsoleTitleW")
	procGetConsoleTitleW = kernel32TitleDLL.NewProc("GetConsoleTitleW")
)

// setConsoleTitleNative sets the console window title via Win32 in addition
// to the OSC escape, for hosts where the escape is not honored. ConPTY
// forwards Win32 title changes to the attached terminal.
func setConsoleTitleNative(title string) {
	p, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	_, _, _ = procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
}

// getConsoleTitleNative returns the current console window title so it can
// be restored when the TUI exits.
func getConsoleTitleNative() string {
	buf := make([]uint16, 1024)
	n, _, _ := procGetConsoleTitleW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

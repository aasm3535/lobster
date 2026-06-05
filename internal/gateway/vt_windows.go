//go:build windows

package gateway

import (
	"os"
	"syscall"
	"unsafe"
)

// enableANSIConsole turns on ANSI escape handling for the Windows console so the terminal
// chat's colours render instead of printing raw escape codes. Harmless if it fails.
func enableANSIConsole() {
	const enableVirtualTerminalProcessing = 0x0004
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	get := kernel32.NewProc("GetConsoleMode")
	set := kernel32.NewProc("SetConsoleMode")
	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := get.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return
	}
	set.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
}

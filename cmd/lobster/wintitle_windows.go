//go:build windows
// +build windows

// setConsoleTitle sets the Windows console window title. On non-Windows this file
// is not compiled (build tag below), so cross-compiling still works. Used so the
// terminal window says "LOBSTER" instead of the default "cmd.exe" / "powershell.exe".

package main

import (
	"syscall"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleTitleW = kernel32.NewProc("SetConsoleTitleW")
)

func setConsoleTitle(title string) {
	p, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	_, _, _ = procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
}

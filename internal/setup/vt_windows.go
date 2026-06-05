//go:build windows

package setup

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode = kernel32.NewProc("SetConsoleMode")
)

const enableVirtualTerminalProcessing = 0x0004

// enableANSI turns on ANSI escape handling for the Windows console so the coloured
// banner renders instead of printing raw escape codes. Modern terminals support it; we
// just have to flip the bit. A failure is harmless — colours simply stay off.
func enableANSI() {
	h := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := procGetConsoleMode.Call(uintptr(h), uintptr(unsafe.Pointer(&mode))); r == 0 {
		colorOK = false
		return
	}
	procSetConsoleMode.Call(uintptr(h), uintptr(mode|enableVirtualTerminalProcessing))
}

// detach makes the spawned bot a fully independent process (own group, no console, no
// window) so it keeps running after the wizard returns and the terminal closes.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		HideWindow:    true,
	}
}

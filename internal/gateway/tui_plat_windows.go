//go:build windows

package gateway

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// Console input modes (wincon.h). We clear line+echo so we get raw keystrokes, set
// virtual-terminal input so the arrow/nav keys arrive as ANSI escape sequences, and keep
// processed-input on so Ctrl-C still raises the interrupt the signal handler watches for.
const (
	enableProcessedInput       = 0x0001
	enableLineInput            = 0x0002
	enableEchoInput            = 0x0004
	enableVirtualTerminalInput = 0x0200
	cpUTF8                     = 65001
)

type coord struct{ X, Y int16 }
type smallRect struct{ Left, Top, Right, Bottom int16 }

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

// enableRawInput puts the console into raw keystroke mode and switches the input/output code
// pages to UTF-8 (so typed Cyrillic comes through correctly). It returns a restore func and
// whether it succeeded — false when stdin isn't a real console, so the caller falls back to
// the line-based REPL.
func enableRawInput() (func(), bool) {
	k := syscall.NewLazyDLL("kernel32.dll")
	getMode := k.NewProc("GetConsoleMode")
	setMode := k.NewProc("SetConsoleMode")
	getCP := k.NewProc("GetConsoleCP")
	setCP := k.NewProc("SetConsoleCP")
	getOutCP := k.NewProc("GetConsoleOutputCP")
	setOutCP := k.NewProc("SetConsoleOutputCP")

	hIn := syscall.Handle(os.Stdin.Fd())
	var mode uint32
	if r, _, _ := getMode.Call(uintptr(hIn), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return nil, false // not a console (piped) — caller falls back
	}
	inCP, _, _ := getCP.Call()
	outCP, _, _ := getOutCP.Call()

	raw := (mode &^ (enableLineInput | enableEchoInput)) | enableVirtualTerminalInput | enableProcessedInput
	setMode.Call(uintptr(hIn), uintptr(raw))
	setCP.Call(cpUTF8)
	setOutCP.Call(cpUTF8)

	restore := func() {
		setMode.Call(uintptr(hIn), uintptr(mode))
		setCP.Call(inCP)
		setOutCP.Call(outCP)
	}
	return restore, true
}

// terminalSize returns the console's visible rows and columns, falling back to 24x80.
func terminalSize() (int, int) {
	k := syscall.NewLazyDLL("kernel32.dll")
	getInfo := k.NewProc("GetConsoleScreenBufferInfo")
	h := syscall.Handle(os.Stdout.Fd())
	var info consoleScreenBufferInfo
	if r, _, _ := getInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 24, 80
	}
	cols := int(info.Window.Right-info.Window.Left) + 1
	rows := int(info.Window.Bottom-info.Window.Top) + 1
	if rows < 8 {
		rows = 24
	}
	if cols < 20 {
		cols = 80
	}
	return rows, cols
}

// watchResize reports the size now and then whenever it changes. Windows has no SIGWINCH, so
// we poll the (cheap) console syscall a few times a second. Returns a stop func.
func watchResize(cb func(rows, cols int)) func() {
	stop := make(chan struct{})
	go func() {
		lastR, lastC := terminalSize()
		cb(lastR, lastC)
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if r, c := terminalSize(); r != lastR || c != lastC {
					lastR, lastC = r, c
					cb(r, c)
				}
			}
		}
	}()
	return func() { close(stop) }
}

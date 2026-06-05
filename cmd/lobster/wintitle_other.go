//go:build !windows
// +build !windows

// No-op stub for non-Windows builds. The real implementation is in wintitle_windows.go.

package main

func setConsoleTitle(title string) {}

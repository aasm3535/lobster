//go:build !windows

package gateway

// On Unix terminals ANSI works out of the box, so there's nothing to enable.
func enableANSIConsole() {}

//go:build !windows

package setup

import "os/exec"

// On Unix the terminal already understands ANSI, and a child started with Start() and
// Release() keeps running on its own — so both hooks are no-ops.
func enableANSI() {}

func detach(_ *exec.Cmd) {}

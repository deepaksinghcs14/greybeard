//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach puts a background child in its own session so it survives the
// parent's terminal closing (SIGHUP) and hook runners that kill the hook's
// process group — the reindex/update children must outlive `check`.
func detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

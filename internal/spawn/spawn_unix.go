//go:build !windows

// Package spawn detaches background children so they survive the parent's
// terminal closing (SIGHUP) and hook runners that kill the process group.
package spawn

import (
	"os/exec"
	"syscall"
)

func Detach(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

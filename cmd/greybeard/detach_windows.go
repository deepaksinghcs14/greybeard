//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the child in a new process group, detached from the console.
func detach(c *exec.Cmd) {
	const createNewProcessGroup = 0x00000200
	const detachedProcess = 0x00000008
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

//go:build windows

package spawn

import (
	"os/exec"
	"syscall"
)

// Detach starts the child in a new process group, detached from the console.
func Detach(c *exec.Cmd) {
	const createNewProcessGroup = 0x00000200
	const detachedProcess = 0x00000008
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

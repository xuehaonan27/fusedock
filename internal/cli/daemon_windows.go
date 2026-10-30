package cli

import (
	"os/exec"
	"syscall"
)

func detachDaemon(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200}
}

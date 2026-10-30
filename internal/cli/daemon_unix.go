//go:build darwin || linux

package cli

import (
	"os/exec"
	"syscall"
)

func detachDaemon(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

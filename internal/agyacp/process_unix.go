//go:build !windows

package agyacp

import (
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd)   { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func attachCommand(p *Process) error { return nil }
func stopCommand(p *Process) {
	if p.cmd != nil && p.cmd.Process != nil {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

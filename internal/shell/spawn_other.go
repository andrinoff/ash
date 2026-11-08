//go:build !darwin

package shell

import "syscall"

func startProcess(path string, argv []string, attr *syscall.ProcAttr) (int, error) {
	return syscall.ForkExec(path, argv, attr)
}

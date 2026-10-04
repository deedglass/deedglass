//go:build unix

package setup

import "syscall"

func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func statfsFree(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil
}

//go:build unix

package main

import "syscall"

// statfsFree is the free space for unprivileged users on the file system holding path.
func statfsFree(path string) (uint64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil
}

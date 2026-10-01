//go:build darwin || linux

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// ioctlWidth asks the terminal for its size with the stdlib syscall package (no cgo, no x/sys).
func ioctlWidth(f *os.File) int {
	if f == nil {
		return 0
	}
	var ws struct{ row, col, x, y uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0
	}
	return int(ws.col)
}

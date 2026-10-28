//go:build !unix

package localstore

import "syscall"

func detachAttr() *syscall.SysProcAttr { return nil }

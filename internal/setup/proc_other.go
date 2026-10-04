//go:build !unix

package setup

import (
	"errors"
	"syscall"
)

func detachAttr() *syscall.SysProcAttr { return nil }

func statfsFree(string) (uint64, error) { return 0, errors.New("not supported on this system") }

//go:build !unix

package main

import "errors"

func statfsFree(string) (uint64, error) { return 0, errors.New("not supported on this system") }

//go:build !darwin && !linux

package main

import "os"

func ioctlWidth(*os.File) int { return 0 }

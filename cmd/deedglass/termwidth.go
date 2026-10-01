package main

import (
	"os"
	"strconv"
)

// termWidth is the terminal's column count: the TIOCGWINSZ ioctl where the platform has it
// (termwidth_unix.go), else $COLUMNS, else 0 (unknown; the mark treats it as 80).
func termWidth(f *os.File, getenv func(string) string) int {
	if n := ioctlWidth(f); n > 0 {
		return n
	}
	if n, err := strconv.Atoi(getenv("COLUMNS")); err == nil && n > 0 && n < 10000 {
		return n
	}
	return 0
}

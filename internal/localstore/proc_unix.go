//go:build unix

package localstore

import "syscall"

// detachAttr puts pg_ctl (and the postmaster it starts) in its own session, so a Ctrl+C in the
// terminal reaches deedglass app, which then stops PostgreSQL cleanly itself.
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

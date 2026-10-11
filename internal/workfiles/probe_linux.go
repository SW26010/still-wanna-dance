package workfiles

import "golang.org/x/sys/unix"

// This is only an early permissions check. Storage initialization performs the
// authoritative write under its lease; do not leave probe files in shared roots.
func Probe(dir string) error { return unix.Faccessat(unix.AT_FDCWD, dir, unix.W_OK, unix.AT_EACCESS) }

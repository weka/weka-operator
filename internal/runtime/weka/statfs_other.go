//go:build !linux

package weka

import "syscall"

// statfsFragmentSize returns the block size from a Statfs_t for non-Linux platforms.
// On Darwin/other, Statfs_t does not have Frsize; fall back to Bsize.
// This path is compile-only; the runtime binary only runs on Linux.
func statfsFragmentSize(stat *syscall.Statfs_t) int64 {
	return int64(stat.Bsize)
}

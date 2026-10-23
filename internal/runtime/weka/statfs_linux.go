//go:build linux

package weka

import "syscall"

// statfsFragmentSize returns the fragment size (f_frsize) from a Statfs_t.
// On Linux, Statfs_t.Frsize corresponds to statvfs f_frsize, which Python uses
// in weka_runtime.py:3360 as `stat.f_frsize`.  This differs from Bsize (f_bsize)
// on some NFS/btrfs mounts.
func statfsFragmentSize(stat *syscall.Statfs_t) int64 {
	return int64(stat.Frsize)
}

//go:build darwin

package driveignore

import (
	"fmt"
	"os"
	"syscall"
)

// dirChangeStamp returns the directory's ctime in unix nanoseconds: unlike
// mtime it moves for remote-origin structural changes too. A zero return
// means unknown, which forces the scan to walk the subtree.
func dirChangeStamp(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected stat type for %s", path)
	}
	return stat.Ctimespec.Nano(), nil
}

// fileInode returns the file's inode, the journal's anchor for deletion
// proofs. Zero means unknown: callers must route to creation, never deletion.
func fileInode(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected stat type for %s", path)
	}
	return stat.Ino, nil
}

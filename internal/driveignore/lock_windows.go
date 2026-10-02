//go:build windows

package driveignore

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive non-blocking byte-range lock on the file's first
// byte: LockFileEx needs a range, and one byte covers the whole lock file.
func tryLock(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

// unlockFile releases the byte-range lock.
func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{})
}

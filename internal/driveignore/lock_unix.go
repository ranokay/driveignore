//go:build unix

package driveignore

import (
	"os"
	"syscall"
)

// tryLock takes an exclusive non-blocking flock: a second holder gets
// EWOULDBLOCK instead of waiting.
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile releases the flock.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

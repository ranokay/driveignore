//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package driveignore

import "os"

// tryLock fails closed on platforms without a pair-lock primitive: the caller
// must error out rather than run unlocked.
func tryLock(*os.File) error { return errLockUnsupported }

// unlockFile is unreachable: tryLock never succeeds here.
func unlockFile(*os.File) error { return errLockUnsupported }

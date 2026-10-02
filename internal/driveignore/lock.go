package driveignore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errLockUnsupported marks platforms with no pair-lock primitive. Locking
// fails closed there: the caller must error out rather than run unlocked.
var errLockUnsupported = errors.New("pair locking is unsupported on this platform")

// AcquireLock takes an exclusive, non-blocking lock on path, the per-pair
// artifact WatchLockPath names. While another process holds it the error
// names the lock path, so the caller can tell which pair is busy. The
// returned release unlocks and closes the file; process death releases the
// lock automatically.
func AcquireLock(path string) (release func() error, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open pair lock %s: %w", path, err)
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockUnsupported) {
			return nil, fmt.Errorf("cannot lock pair %s: %w", path, err)
		}
		return nil, fmt.Errorf("another driveignore run holds the pair lock %s: %w", path, err)
	}
	return func() error {
		unlockErr := unlockFile(f)
		closeErr := f.Close()
		if unlockErr != nil {
			return fmt.Errorf("release pair lock %s: %w", path, unlockErr)
		}
		return closeErr
	}, nil
}

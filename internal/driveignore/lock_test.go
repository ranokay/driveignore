package driveignore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A pair lock is exclusive: while one holder has it, a second AcquireLock on
// the same path must fail naming the lock, and releasing must free it for the
// next acquirer.
func TestAcquireLockExcludesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pair.lock")

	release, err := AcquireLock(path)
	require.NoError(t, err)
	require.NotNil(t, release)

	_, err = AcquireLock(path)
	require.Error(t, err, "a held lock must refuse a second holder")
	require.ErrorContains(t, err, path)

	require.NoError(t, release())

	second, err := AcquireLock(path)
	require.NoError(t, err, "a released lock must be acquirable again")
	require.NoError(t, second())
}

//go:build darwin

package driveignore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestIgnoreAttributeSetReadRemove proves the real attribute helpers round-trip
// on a directory and a file, the two kinds the guard stamps.
func TestIgnoreAttributeSetReadRemove(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	write(t, file, "x")

	for _, path := range []string{dir, file} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			sealed, err := hasIgnoreAttr(path)
			require.NoError(t, err)
			require.False(t, sealed, "nothing starts sealed")

			require.NoError(t, setIgnoreAttr(path))
			sealed, err = hasIgnoreAttr(path)
			require.NoError(t, err)
			require.True(t, sealed)

			require.NoError(t, removeIgnoreAttr(path))
			sealed, err = hasIgnoreAttr(path)
			require.NoError(t, err)
			require.False(t, sealed)
		})
	}
}

package cmd

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ranokay/driveignore/internal/driveignore"
)

// The mutating commands share the watch pair lock: while it is held they must
// exit 1 naming the busy lock instead of touching the pair.
func TestMutatingCommandsRefuseWhilePairLocked(t *testing.T) {
	// Keep the pair's lock artifact inside the test's temp tree instead of the
	// user's real config directory.
	configDir := t.TempDir()
	t.Setenv("HOME", configDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("AppData", configDir)

	src, out := t.TempDir(), t.TempDir()

	lockPath, err := driveignore.WatchLockPath(src, out)
	require.NoError(t, err)
	release, err := driveignore.AcquireLock(lockPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, release()) })

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"upload", []string{"upload", out, "-i", src}},
		{"unify", []string{"unify", out, "-i", src}},
		{"clean", []string{"clean", out, "-i", src}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			rootCmd.SetArgs(tc.args)
			rootCmd.SetOut(io.Discard)
			rootCmd.SetErr(&stderr)
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
			})

			require.Equal(t, 1, execute(), "a locked pair is a runtime failure, not a usage error")
			require.Contains(t, stderr.String(), lockPath, "the error must name the busy lock")
		})
	}
}

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ranokay/driveignore/internal/driveignore"
)

// watchTestPair builds a pair with a backdated local-only file, so the churn
// guard does not defer it and a pass can act immediately.
func watchTestPair(t *testing.T) (src, out string) {
	t.Helper()
	src, out = t.TempDir(), t.TempDir()
	paths := []string{filepath.Join(src, ".driveignore"), filepath.Join(src, "new.txt")}
	require.NoError(t, os.WriteFile(paths[0], nil, 0o644))
	require.NoError(t, os.WriteFile(paths[1], []byte("new"), 0o644))
	past := time.Now().Add(-time.Hour)
	for _, path := range paths {
		require.NoError(t, os.Chtimes(path, past, past))
	}
	return src, out
}

// isolateConfigHome keeps the per-pair journal and lock inside the test's temp
// tree instead of the user's real config directory.
func isolateConfigHome(t *testing.T) {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("HOME", configDir)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("AppData", configDir)
}

// runWatch invokes the root command through the execute() seam.
func runWatch(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		resetWatchFlags(t)
	})
	code = execute()
	return code, out.String(), errBuf.String()
}

// resetWatchFlags restores the watch command's flags to their defaults: flag
// values live on the shared root command, so without this a run would leak
// them into the next test.
func resetWatchFlags(t *testing.T) {
	t.Helper()
	watch, _, err := rootCmd.Find([]string{"watch"})
	require.NoError(t, err)
	for _, name := range []string{"once", "dry-run", "one-way", "install", "uninstall", "interval", "input"} {
		flag := watch.Flags().Lookup(name)
		require.NotNil(t, flag)
		require.NoError(t, flag.Value.Set(flag.DefValue))
		flag.Changed = false
	}
}

// dirNames lists the entries of dir, for asserting a tree stayed untouched.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func assertHardlinked(t *testing.T, first, second string) {
	t.Helper()
	firstInfo, err := os.Stat(first)
	require.NoError(t, err)
	secondInfo, err := os.Stat(second)
	require.NoError(t, err)
	require.True(t, os.SameFile(firstInfo, secondInfo), "%s and %s are not hardlinked", first, second)
}

func TestNextWatchInterval(t *testing.T) {
	tests := []struct {
		name       string
		prev       time.Duration
		base       time.Duration
		hadActions bool
		failed     bool
		want       time.Duration
	}{
		{"idle doubles from the base", watchBaseInterval, watchBaseInterval, false, false, 2 * watchBaseInterval},
		{"idle stops at the maximum", 40 * time.Second, watchBaseInterval, false, false, watchMaxInterval},
		{"idle stays at the maximum", watchMaxInterval, watchBaseInterval, false, false, watchMaxInterval},
		{"actions reset to the base", watchMaxInterval, watchBaseInterval, true, false, watchBaseInterval},
		{"actions reset to a custom base", watchMaxInterval, 30 * time.Second, true, false, 30 * time.Second},
		{"a custom base lowers the floor", 0, time.Second, false, false, time.Second},
		{"idle doubles a custom base", 5 * time.Second, 5 * time.Second, false, false, 10 * time.Second},
		{"idle caps a custom base at the maximum", 40 * time.Second, 30 * time.Second, false, false, watchMaxInterval},
		{"a failure doubles", watchBaseInterval, watchBaseInterval, false, true, 2 * watchBaseInterval},
		{"a failure stops at the maximum", 45 * time.Second, watchBaseInterval, false, true, watchMaxInterval},
		{"a failure backs off even after progress", watchBaseInterval, watchBaseInterval, true, true, 2 * watchBaseInterval},
		{"a zero interval floors at the base", 0, watchBaseInterval, false, false, watchBaseInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, nextWatchInterval(tt.prev, tt.base, tt.hadActions, tt.failed))
		})
	}
}

func TestWatchOnceDryRunReportsWithoutChanges(t *testing.T) {
	isolateConfigHome(t)
	src, out := watchTestPair(t)
	statePath, err := driveignore.WatchStatePath(src, out)
	require.NoError(t, err)

	code, stdout, stderr := runWatch(t, "watch", out, "-i", src, "--once", "--dry-run")

	require.Equal(t, 0, code, stderr)
	require.Empty(t, stderr)
	require.Equal(t, "would linked: .driveignore\nwould linked: new.txt\nwould apply 2 action(s)\n", stdout)
	require.ElementsMatch(t, []string{".driveignore", "new.txt"}, dirNames(t, src), "a dry run must leave the source tree alone")
	require.Empty(t, dirNames(t, out), "a dry run must leave the drive tree alone")
	require.NoFileExists(t, statePath, "a dry run must not write state")
}

func TestWatchOnceAppliesAndIsIdempotent(t *testing.T) {
	isolateConfigHome(t)
	src, out := watchTestPair(t)

	code, stdout, stderr := runWatch(t, "watch", out, "-i", src, "--once")

	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "pass: 2 action(s), 0 deferred in ")
	require.NotContains(t, stdout, "linked:", "routine creations stay silent by default")
	assertHardlinked(t, filepath.Join(src, ".driveignore"), filepath.Join(out, ".driveignore"))
	assertHardlinked(t, filepath.Join(src, "new.txt"), filepath.Join(out, "new.txt"))

	code, stdout, stderr = runWatch(t, "watch", out, "-i", src, "--once")

	require.Equal(t, 0, code, stderr)
	require.Empty(t, stdout, "a converged pass must print nothing")
}

func TestWatchRefusesWhenPairLocked(t *testing.T) {
	isolateConfigHome(t)
	src, out := watchTestPair(t)

	lockPath, err := driveignore.WatchLockPath(src, out)
	require.NoError(t, err)
	release, err := driveignore.AcquireLock(lockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	code, _, stderr := runWatch(t, "watch", out, "-i", src, "--once")

	require.Equal(t, 1, code, "a locked pair is a runtime failure, not a usage error")
	require.Contains(t, stderr, lockPath, "the error must name the busy lock")
}

func TestWatchRejectsPairWithoutHardlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	isolateConfigHome(t)
	src, out := watchTestPair(t)

	require.NoError(t, os.Chmod(out, 0o500))
	t.Cleanup(func() { _ = os.Chmod(out, 0o755) })

	code, _, stderr := runWatch(t, "watch", out, "-i", src, "--once")

	require.Equal(t, 1, code, "a pair without hardlink support must fail, not degrade")
	require.Contains(t, stderr, "filesystem", "the error must name the filesystem requirement")
	require.Contains(t, stderr, "--copy", "the error must say copy mode is unsupported")
}

func TestWatchIntervalMustBePositive(t *testing.T) {
	isolateConfigHome(t)
	src, out := watchTestPair(t)

	tests := []struct {
		name string
		args []string
	}{
		{"zero", []string{"--interval", "0"}},
		{"negative", []string{"--interval=-1s"}},
		{"above the maximum", []string{"--interval=61s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"watch", out, "-i", src, "--once"}, tt.args...)
			code, stdout, stderr := runWatch(t, args...)

			require.Equal(t, 2, code, "--interval outside its bounds is a usage error")
			require.Empty(t, stdout)
			require.Contains(t, stderr, "--interval")
		})
	}
}

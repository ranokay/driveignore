package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ranokay/driveignore/internal/driveignore"
)

// guardTestFolder builds a guarded folder with rules and one matching
// directory, ready for a pass.
func guardTestFolder(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".driveignore"), []byte("node_modules/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("x"), 0o644))
	return root
}

// runGuard invokes the root command through the execute() seam.
func runGuard(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	rootCmd.SetArgs(args)
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		resetGuardFlags(t)
	})
	code = execute()
	return code, out.String(), errBuf.String()
}

// resetGuardFlags restores the guard command's flags to their defaults: flag
// values live on the shared root command, so without this a run would leak
// them into the next test.
func resetGuardFlags(t *testing.T) {
	t.Helper()
	guard, _, err := rootCmd.Find([]string{"guard"})
	require.NoError(t, err)
	for _, name := range []string{"once", "dry-run", "install", "uninstall", "interval"} {
		flag := guard.Flags().Lookup(name)
		require.NotNil(t, flag)
		require.NoError(t, flag.Value.Set(flag.DefValue))
		flag.Changed = false
	}
}

// TestGuardRejectsInstallAndUninstallTogether pins the flag wiring; the usage
// error is raised before any launchd call, so the test is safe on every OS.
func TestGuardRejectsInstallAndUninstallTogether(t *testing.T) {
	isolateConfigHome(t)

	code, stdout, stderr := runGuard(t, "guard", t.TempDir(), "--install", "--uninstall")

	require.Equal(t, 2, code, "combining the two agent modes is a usage error")
	require.Empty(t, stdout)
	require.Contains(t, stderr, "--install and --uninstall")
}

func TestGuardIntervalMustBePositive(t *testing.T) {
	isolateConfigHome(t)
	root := t.TempDir()

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
			args := append([]string{"guard", root, "--once"}, tt.args...)
			code, stdout, stderr := runGuard(t, args...)

			require.Equal(t, 2, code, "--interval outside its bounds is a usage error")
			require.Empty(t, stdout)
			require.Contains(t, stderr, "--interval")
		})
	}
}

func TestGuardRejectsNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("guard runs on darwin")
	}
	code, _, stderr := runGuard(t, "guard", t.TempDir(), "--once")
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "macOS")
}

func TestGuardRefusesWhenLocked(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("guard is macOS only")
	}
	isolateConfigHome(t)
	root := guardTestFolder(t)

	lockPath, err := driveignore.GuardLockPath(root)
	require.NoError(t, err)
	release, err := driveignore.AcquireLock(lockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	code, _, stderr := runGuard(t, "guard", root, "--once")

	require.Equal(t, 1, code, "a locked folder is a runtime failure, not a usage error")
	require.Contains(t, stderr, lockPath, "the error must name the busy lock")
}

//go:build darwin

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// hasIgnoreAttrForTest reads the attribute directly; the cmd package cannot
// see the core's unexported helper.
func hasIgnoreAttrForTest(t *testing.T, path string) bool {
	t.Helper()
	_, err := unix.Getxattr(path, "com.apple.fileprovider.ignore#P", make([]byte, 1))
	return err == nil
}

func TestGuardOnceDryRunReportsWithoutChanges(t *testing.T) {
	isolateConfigHome(t)
	root := guardTestFolder(t)

	code, stdout, stderr := runGuard(t, "guard", root, "--once", "--dry-run")

	require.Equal(t, 0, code, stderr)
	require.Equal(t, "would seal: node_modules\nwould apply 1 action(s)\n", stdout)
	require.False(t, hasIgnoreAttrForTest(t, filepath.Join(root, "node_modules")),
		"a dry run must not stamp anything")
}

func TestGuardOnceSealsAndUnseals(t *testing.T) {
	isolateConfigHome(t)
	root := guardTestFolder(t)
	target := filepath.Join(root, "node_modules")

	code, stdout, stderr := runGuard(t, "guard", root, "--once")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "sealed: node_modules")
	require.Contains(t, stdout, "pass: 1 action(s)")
	require.True(t, hasIgnoreAttrForTest(t, target))

	code, stdout, stderr = runGuard(t, "guard", root, "--once")
	require.Equal(t, 0, code, stderr)
	require.Empty(t, stdout, "a converged pass must print nothing")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".driveignore"), []byte("# none\n"), 0o644))
	code, stdout, stderr = runGuard(t, "guard", root, "--once")
	require.Equal(t, 0, code, stderr)
	require.Contains(t, stdout, "unsealed: node_modules")
	require.False(t, hasIgnoreAttrForTest(t, target))
}

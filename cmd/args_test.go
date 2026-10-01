package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSingleDirArg(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o644))

	require.Error(t, singleDirArg()(nil, nil))
	require.Error(t, singleDirArg()(nil, []string{"one", "two"}))
	require.Error(t, singleDirArg()(nil, []string{filepath.Join(dir, "missing")}))
	require.Error(t, singleDirArg()(nil, []string{file}))
	require.NoError(t, singleDirArg()(nil, []string{dir}))
}

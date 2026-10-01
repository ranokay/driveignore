package cmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecuteExitCodes(t *testing.T) {
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, ".driveignore"), []byte("ignored.txt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644))
	out := t.TempDir()

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"version", []string{"--version"}, 0},
		{"missing positional argument", []string{"clean"}, 2},
		{"unknown flag", []string{"diff", "--nope", out}, 2},
		{"missing input directory", []string{"diff", out, "-i", filepath.Join(src, "missing")}, 2},
		{"global with extra arguments", []string{"global", "extra"}, 2},
		{"differences without exit-code still succeed", []string{"diff", out, "-i", src}, 0},
		{"differences with exit-code", []string{"diff", out, "-i", src, "--exit-code"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rootCmd.SetArgs(tc.args)
			rootCmd.SetOut(io.Discard)
			rootCmd.SetErr(io.Discard)
			t.Cleanup(func() {
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
			})
			require.Equal(t, tc.want, execute())
		})
	}
}

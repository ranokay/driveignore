package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSharedFlagsUseOneUsageText(t *testing.T) {
	upload, diff, clean, unify := newUploadCmd(), newDiffCmd(), newCleanCmd(), newUnifyCmd()

	for _, cmd := range []*cobra.Command{upload, diff, clean, unify} {
		flag := cmd.Flags().Lookup("input")
		require.NotNil(t, flag)
		require.Equal(t, "Input directory of the source files", flag.Usage, cmd.Name())
	}

	for _, cmd := range []*cobra.Command{upload, diff, unify} {
		flag := cmd.Flags().Lookup("merge-ignores")
		require.NotNil(t, flag)
		require.Equal(t, "Merges the global and the input directory's .driveignore", flag.Usage, cmd.Name())
	}

	for _, cmd := range []*cobra.Command{upload, unify} {
		flag := cmd.Flags().Lookup("copy")
		require.NotNil(t, flag)
		require.Equal(t, "Copy files instead of hardlinking them (for filesystems without hardlink support)", flag.Usage, cmd.Name())
	}

	for _, cmd := range []*cobra.Command{clean, unify} {
		flag := cmd.Flags().Lookup("prune-ignored")
		require.NotNil(t, flag)
		require.Equal(t, "Remove files excluded by .driveignore even when they exist in the source", flag.Usage, cmd.Name())
	}
}

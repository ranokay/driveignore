package cmd

import "github.com/spf13/cobra"

// The shared flags are registered here so every command that carries one
// describes it identically.

func addInputFlag(cmd *cobra.Command, input *string) {
	cmd.Flags().StringVarP(input, "input", "i", ".", "Input directory of the source files")
}

func addMergeIgnoresFlag(cmd *cobra.Command, mergeIgnores *bool) {
	cmd.Flags().BoolVarP(mergeIgnores, "merge-ignores", "M", false, "Merges the global and the input directory's .driveignore")
}

func addCopyFlag(cmd *cobra.Command, copyFiles *bool) {
	cmd.Flags().BoolVar(copyFiles, "copy", false, "Copy files instead of hardlinking them (for filesystems without hardlink support)")
}

func addPruneIgnoredFlag(cmd *cobra.Command, pruneIgnored *bool) {
	cmd.Flags().BoolVar(pruneIgnored, "prune-ignored", false, "Remove files excluded by .driveignore even when they exist in the source")
}

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newOptions(cmd *cobra.Command, input, output string) (driveignore.Options, error) {
	globalPath, err := driveignore.GlobalIgnorePath()
	if err != nil {
		return driveignore.Options{}, err
	}
	opts := driveignore.Options{
		Input:            input,
		Output:           output,
		GlobalIgnorePath: globalPath,
		Out:              cmd.OutOrStdout(),
	}
	if verbose {
		opts.Log = func(format string, args ...any) {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", args...)
		}
	}
	return opts, nil
}

// singleDirArg validates that exactly one argument is given and that it names
// an existing directory.
func singleDirArg() cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		info, err := os.Stat(args[0])
		if err != nil {
			return fmt.Errorf("cannot use %q: %w", args[0], err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", args[0])
		}
		return nil
	}
}

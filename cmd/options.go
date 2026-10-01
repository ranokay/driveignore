package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newOptions(cmd *cobra.Command, input, output string) (driveignore.Options, error) {
	if err := requireDir(input); err != nil {
		return driveignore.Options{}, err
	}
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
			return usageError{err}
		}
		return requireDir(args[0])
	}
}

// requireDir returns a usage error when path does not name a directory.
func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return usageError{fmt.Errorf("cannot use %q: %w", path, err)}
	}
	if !info.IsDir() {
		return usageError{fmt.Errorf("%q is not a directory", path)}
	}
	return nil
}

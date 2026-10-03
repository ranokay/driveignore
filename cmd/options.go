package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newOptions(cmd *cobra.Command, input, output string) (driveignore.Config, *progress, error) {
	if err := requireDir(input); err != nil {
		return driveignore.Config{}, nil, err
	}
	errWriter := cmd.ErrOrStderr()
	cfg := driveignore.Config{
		Input:  input,
		Output: output,
	}
	if verbose {
		cfg.Log = func(format string, args ...any) {
			_, _ = fmt.Fprintf(errWriter, format+"\n", args...)
		}
	}
	prog := newProgress(errWriter, !verbose && isTerminal(errWriter))
	cfg.Progress = prog.Tick
	return cfg, prog, nil
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

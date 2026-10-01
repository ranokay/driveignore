// Copyright © 2019 Marcin Wojnarowski xmarcinmarcin@gmail.com
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/version"
)

// usageError marks command-line misuse. It exits with status 2 and prints the
// usage text, while runtime failures exit with status 1.
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// errDiffExit makes `diff --exit-code` exit 1 without printing an error.
var errDiffExit = errors.New("differences found")

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "driveignore",
	Short: ".driveignore support for Google Drive",
	Long: `This simple cli allows you to have .driveignore(s)
It looks for a .driveignore, ignores the matching files,
and hardlinks the rest into your drive folder
meaning no files duplicates, and no repetitive cli calls.`,
	Version:       version.String(),
	SilenceErrors: true,
	SilenceUsage:  true,
}

var verbose bool

// Execute runs the CLI and exits with the resulting status code.
func Execute() {
	os.Exit(execute())
}

// execute runs the root command and returns the process exit code: 0 on
// success, 1 for runtime failures or `diff --exit-code` differences, and 2
// for usage errors.
func execute() int {
	cmd, err := rootCmd.ExecuteC()
	if err == nil {
		return 0
	}
	var usage usageError
	switch {
	case errors.As(err, &usage):
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), cmd.UsageString())
		return 2
	case errors.Is(err, errDiffExit):
		return 1
	default:
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Error:", err)
		return 1
	}
}

func init() {
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err}
	})
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "print what is happening")
}

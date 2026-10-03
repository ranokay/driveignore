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
	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newDiffCmd() *cobra.Command {
	var (
		input        string
		mergeIgnores bool
		exitCode     bool
	)
	cmd := &cobra.Command{
		Use:   "diff [drive sync folder path]",
		Short: "Compares your directory with the drive one",
		Long: `Prints out the difference in files between your source (input) and
drive sync folder ([drive sync folder path])

Red    - your drive sync folder is missing a file
Yellow - your drive sync folder has a file that doesn't exist in input

With --exit-code, diff exits 1 when differences exist and 0 otherwise,
without printing an error.
`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, prog, err := newOptions(cmd, input, args[0])
			if err != nil {
				return err
			}
			defer prog.Done()
			cfg.MergeIgnores = mergeIgnores
			res, err := driveignore.Diff(cfg)
			if err != nil {
				return err
			}
			redPrint := color.New(color.FgRed).FprintlnFunc()
			yellowPrint := color.New(color.FgHiYellow).FprintlnFunc()
			for _, missing := range res.Missing {
				redPrint(cmd.OutOrStdout(), missing)
			}
			for _, old := range res.Old {
				yellowPrint(cmd.OutOrStdout(), old)
			}
			if exitCode && (len(res.Missing) > 0 || len(res.Old) > 0) {
				return errDiffExit
			}
			return nil
		},
	}
	addInputFlag(cmd, &input)
	addMergeIgnoresFlag(cmd, &mergeIgnores)
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "Exit with status 1 when differences exist")
	return cmd
}

func init() {
	rootCmd.AddCommand(newDiffCmd())
}

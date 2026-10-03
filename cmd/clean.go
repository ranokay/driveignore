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
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newCleanCmd() *cobra.Command {
	var (
		input        string
		dryRun       bool
		pruneIgnored bool
	)
	cmd := &cobra.Command{
		Use:   "clean [path to clean]",
		Short: "Cleans your drive sync folder from old files",
		Long: `Will look through the drive sync folder and
remove files that do not exist in your source files.

With --dry-run, the files are listed on stdout but nothing is removed.
With --prune-ignored, files that your .driveignore excludes are removed
even when the source still contains them.
`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, prog, err := newOptions(cmd, input, args[0])
			if err != nil {
				return err
			}
			defer prog.Done()
			lockPath, err := driveignore.WatchLockPath(input, args[0])
			if err != nil {
				return err
			}
			release, err := driveignore.AcquireLock(lockPath)
			if err != nil {
				return err
			}
			defer func() { _ = release() }()
			removed, err := driveignore.Clean(cfg, driveignore.CleanOptions{DryRun: dryRun, PruneIgnored: pruneIgnored})
			if err != nil {
				return err
			}
			if dryRun {
				for _, rel := range removed {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), rel)
				}
			}
			return nil
		},
	}
	addInputFlag(cmd, &input)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List files that would be removed without removing them")
	addPruneIgnoredFlag(cmd, &pruneIgnored)
	return cmd
}

func init() {
	rootCmd.AddCommand(newCleanCmd())
}

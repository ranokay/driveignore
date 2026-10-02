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
	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newUnifyCmd() *cobra.Command {
	var (
		input        string
		mergeIgnores bool
		pruneIgnored bool
		copyFiles    bool
	)
	cmd := &cobra.Command{
		Use:   "unify [output path]",
		Short: "Unifies 2 directories where input is the source",
		Long: `Uploads all files (with respect to .driveignores)
as well as removes legacy files from the drive sync folder.

It is an alias for: 'driveignore upload [args] [flags] --force' + 'driveignore clean [args] [flags]'
With --prune-ignored, it also removes files excluded by .driveignore,
even when they exist in the source.`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := newOptions(cmd, input, args[0])
			if err != nil {
				return err
			}
			cfg.MergeIgnores = mergeIgnores
			res, err := driveignore.Unify(cfg, driveignore.UnifyOptions{PruneIgnored: pruneIgnored, Copy: copyFiles})
			printConflicts(cmd.OutOrStdout(), res.Conflicts)
			return err
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", ".", "Input directory of the files to be uploaded")
	cmd.Flags().BoolVarP(&mergeIgnores, "merge-ignores", "M", false, "Merges the global and the input directory's .driveignore")
	cmd.Flags().BoolVar(&pruneIgnored, "prune-ignored", false, "Remove files excluded by .driveignore even when they exist in the source")
	cmd.Flags().BoolVar(&copyFiles, "copy", false, "Copy files instead of hardlinking them (for filesystems without hardlink support)")
	return cmd
}

func init() {
	rootCmd.AddCommand(newUnifyCmd())
}

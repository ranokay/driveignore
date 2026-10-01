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

func newUploadCmd() *cobra.Command {
	var (
		input        string
		mergeIgnores bool
		force        bool
	)
	cmd := &cobra.Command{
		Use:   "upload [output path]",
		Short: "Upload a directory to your drive folder",
		Long: `Uploads files from the input directory (--input flag) into a drive folder.
Files that satisfy the .driveignore are skipped.
The order of importance of a .driveignore file:
current folder > global config
`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := newOptions(cmd, input, args[0])
			if err != nil {
				return err
			}
			opts.MergeIgnores = mergeIgnores
			opts.Force = force
			return driveignore.Upload(opts)
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", ".", "Input directory of the files to be uploaded")
	cmd.Flags().BoolVarP(&mergeIgnores, "merge-ignores", "M", false, "Merges global and input dir .driveignore")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files with the same name")
	return cmd
}

func init() {
	rootCmd.AddCommand(newUploadCmd())
}

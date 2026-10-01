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

func newCleanCmd() *cobra.Command {
	var input string
	cmd := &cobra.Command{
		Use:   "clean [path to clean]",
		Short: "Cleans your drive sync folder from old files",
		Long: `Will look through the drive sync folder and
remove files that do not exist in your source files.
`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := newOptions(cmd, input, args[0])
			if err != nil {
				return err
			}
			_, err = driveignore.Clean(opts)
			return err
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", ".", "Input directory of source files")
	return cmd
}

func init() {
	rootCmd.AddCommand(newCleanCmd())
}

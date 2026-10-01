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
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newGlobalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "global",
		Short: "Get the path to your global .driveignore",
		Long: `If you wish to have a global .driveignore you can set the content of to it here.
You can later decide if you want to use global, local or merged .driveignore.`,
		Example: "vim $(driveignore global)",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := driveignore.GlobalIgnorePath()
			if err != nil {
				return err
			}
			if verbose {
				if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
					_, _ = fmt.Fprintln(cmd.ErrOrStderr(), ".global_driveignore didnt exist, created a new one")
				}
			}
			if err := driveignore.EnsureFile(path); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
}

func init() {
	rootCmd.AddCommand(newGlobalCmd())
}

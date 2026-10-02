package cmd

import (
	"fmt"
	"io"
)

// printConflicts reports entries Upload left untouched, one per line. Upload
// returns conflicts as data; printing them is the command's job.
func printConflicts(w io.Writer, conflicts []string) {
	for _, rel := range conflicts {
		_, _ = fmt.Fprintf(w, "cannot upload '%s'. A file with the same name already exists.\n", rel)
	}
}

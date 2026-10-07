package cmd

import (
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newGuardCmd() *cobra.Command {
	var (
		once      bool
		dryRun    bool
		install   bool
		uninstall bool
		interval  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "guard [folder]",
		Short: "Keep ignored paths in a My Drive folder sealed from sync",
		Long: `Keeps a folder inside My Drive working the way its ignore rules say: paths
that match the .driveignore rules are stamped with the macOS File Provider
ignore attribute, so Google Drive for desktop keeps them on disk but out of the
cloud, and paths whose rules were removed are unstamped again.

The rules come from the .driveignore in the guarded folder itself, falling back
to the global .driveignore when the folder has none. Run --once for a single
pass, or --dry-run to print what a pass would do without changing anything.`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if install || uninstall {
				return runGuardAgent(cmd, args[0], install, uninstall)
			}
			if interval <= 0 || interval > pollMaxInterval {
				return usageError{fmt.Errorf("--interval must be greater than 0s and at most %s, got %s", pollMaxInterval, interval)}
			}
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("guard uses a macOS File Provider attribute and is only available on macOS; this is %s", runtime.GOOS)
			}
			cfg, _, err := newOptions(cmd, args[0], "")
			if err != nil {
				return err
			}
			lockPath, err := driveignore.GuardLockPath(args[0])
			if err != nil {
				return err
			}
			release, err := driveignore.AcquireLock(lockPath)
			if err != nil {
				return err
			}
			defer func() { _ = release() }()
			guard, err := driveignore.NewGuard(cfg)
			if err != nil {
				return err
			}
			opts := driveignore.GuardOptions{DryRun: dryRun}
			if once {
				_, err := runGuardPass(guard, opts, cmd.OutOrStdout())
				return err
			}
			return guardLoop(cmd, guard, opts, interval)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "Run a single pass and exit")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what a pass would seal or unseal without changing anything")
	cmd.Flags().BoolVar(&install, "install", false, "Install the folder as a launchd agent (macOS only)")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Uninstall the folder's launchd agent (macOS only)")
	cmd.Flags().DurationVar(&interval, "interval", pollBaseInterval, "Base time between passes while changes flow (idle backs off to 60s)")
	return cmd
}

// runGuardPass runs one pass and prints its report. The report lets the loop
// adapt its interval; the error is returned for --once to exit non-zero and
// for the loop to back off.
func runGuardPass(guard *driveignore.Guard, opts driveignore.GuardOptions, out io.Writer) (driveignore.Report, error) {
	start := time.Now()
	report, err := guard.Pass(opts)
	if err != nil {
		return report, err
	}
	printGuardReport(out, report, opts.DryRun, time.Since(start))
	return report, nil
}

// guardLoop polls until the process is interrupted. Like watch, a failed pass
// is reported and backed off, never stopping the loop.
func guardLoop(cmd *cobra.Command, guard *driveignore.Guard, opts driveignore.GuardOptions, base time.Duration) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	interval := base
	for {
		report, err := runGuardPass(guard, opts, out)
		if err != nil {
			_, _ = fmt.Fprintln(errOut, "guard:", err)
		}
		interval = nextPollInterval(interval, base, len(report.Actions) > 0, err != nil)
		time.Sleep(interval)
	}
}

// printGuardReport renders one pass. Every seal and unseal is worth a line
// because it explains why a path is or is not syncing; a dry run phrases the
// same decisions as intentions. A pass with no actions prints nothing.
func printGuardReport(out io.Writer, report driveignore.Report, dryRun bool, elapsed time.Duration) {
	for _, action := range report.Actions {
		sealing := action.Kind != driveignore.ActionUnsealed
		if dryRun {
			verb := "seal"
			if !sealing {
				verb = "unseal"
			}
			_, _ = fmt.Fprintf(out, "would %s: %s\n", verb, action.Path)
			continue
		}
		verb := "sealed"
		if !sealing {
			verb = "unsealed"
		}
		_, _ = fmt.Fprintf(out, "%s: %s\n", verb, action.Path)
	}
	if dryRun {
		_, _ = fmt.Fprintf(out, "would apply %d action(s)\n", len(report.Actions))
		return
	}
	if len(report.Actions) > 0 {
		_, _ = fmt.Fprintf(out, "pass: %d action(s) in %s\n", len(report.Actions), elapsed.Round(10*time.Millisecond))
	}
}

func init() {
	rootCmd.AddCommand(newGuardCmd())
}

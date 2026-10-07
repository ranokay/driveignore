package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func newWatchCmd() *cobra.Command {
	var (
		input     string
		once      bool
		dryRun    bool
		oneWay    bool
		install   bool
		uninstall bool
		interval  time.Duration
		trashDir  string
	)
	cmd := &cobra.Command{
		Use:   "watch [drive folder]",
		Short: "Keep the source and the drive folder reconciled continuously",
		Long: `Watches the source directory and its drive folder and converges structural
changes in both directions until interrupted.

Both trees must be on one filesystem: watch hardlinks content and refuses to
run when it cannot, because copies could silently diverge. A per-pair journal
tells created paths from deleted ones, so a local deletion moves to the Trash
instead of being guessed away. Run --once for a single pass, or --dry-run to
print what a pass would do without changing anything.`,
		Args: singleDirArg(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if install || uninstall {
				return runWatchAgent(cmd, input, args[0], install, uninstall)
			}
			if interval <= 0 || interval > pollMaxInterval {
				return usageError{fmt.Errorf("--interval must be greater than 0s and at most %s, got %s", pollMaxInterval, interval)}
			}
			// The journal, the lock and the reconciliation must all key the
			// pair the same way; WatchPairHash canonicalizes both roots, so
			// any spelling finds the same state.
			cfg, _, err := newOptions(cmd, input, args[0])
			if err != nil {
				return err
			}
			statePath, err := driveignore.WatchStatePath(input, args[0])
			if err != nil {
				return err
			}
			lockPath, err := driveignore.WatchLockPath(input, args[0])
			if err != nil {
				return err
			}
			release, err := driveignore.AcquireLock(lockPath)
			if err != nil {
				return err
			}
			defer func() { _ = release() }()
			if err := requireHardlinks(input, args[0]); err != nil {
				return err
			}
			opts := driveignore.WatchOptions{DryRun: dryRun, OneWay: oneWay, TrashDir: trashDir}
			if once {
				_, err := runWatchPass(cfg, statePath, opts, cmd.OutOrStdout(), verbose)
				return err
			}
			return watchLoop(cmd, cfg, statePath, opts, interval)
		},
	}
	addInputFlag(cmd, &input)
	cmd.Flags().BoolVar(&once, "once", false, "Run a single reconcile pass and exit")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the actions a pass would take without changing anything")
	cmd.Flags().BoolVar(&oneWay, "one-way", false, "Only propagate local changes; the source tree is authoritative")
	cmd.Flags().BoolVar(&install, "install", false, "Install the pair as a launchd agent (macOS only)")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Uninstall the pair's launchd agent (macOS only)")
	cmd.Flags().DurationVar(&interval, "interval", pollBaseInterval, "Base time between passes while changes flow (idle backs off to 60s)")
	cmd.Flags().StringVar(&trashDir, "trash-dir", "", "Directory local deletions move to (default: ~/.Trash)")
	return cmd
}

// requireHardlinks proves the pair can share a hardlink before the first pass.
// watch's safety model assumes one filesystem, so a pair that cannot link
// fails here instead of silently degrading to copies.
func requireHardlinks(input, output string) error {
	probe, err := os.CreateTemp(input, ".driveignore-watch-probe-*")
	if err != nil {
		return fmt.Errorf("watch cannot create a probe in %s: %w", input, err)
	}
	probePath := probe.Name()
	_ = probe.Close()
	linkedPath := filepath.Join(output, filepath.Base(probePath))
	defer func() {
		_ = os.Remove(probePath)
		_ = os.Remove(linkedPath)
	}()
	if err := os.Link(probePath, linkedPath); err != nil {
		return fmt.Errorf("watch requires hardlinks between %s and %s; keep both trees on one filesystem (--copy is unsupported): %w", input, output, err)
	}
	return nil
}

// runWatchPass runs one reconcile pass and prints its report. The report lets
// the loop adapt its interval; the error is returned for --once to exit
// non-zero and for the loop to back off.
func runWatchPass(cfg driveignore.Config, statePath string, opts driveignore.WatchOptions, out io.Writer, verbose bool) (driveignore.Report, error) {
	start := time.Now()
	report, err := driveignore.Reconcile(cfg, statePath, opts)
	if err != nil {
		return report, err
	}
	printWatchReport(out, report, opts.DryRun, verbose, time.Since(start))
	return report, nil
}

// watchLoop polls until the process is interrupted. base is the cadence while
// changes flow and the floor for the backoff; a failed pass is reported and
// backed off, never stopping the loop.
func watchLoop(cmd *cobra.Command, cfg driveignore.Config, statePath string, opts driveignore.WatchOptions, base time.Duration) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	interval := base
	for {
		report, err := runWatchPass(cfg, statePath, opts, out, verbose)
		if err != nil {
			_, _ = fmt.Fprintln(errOut, "watch:", err)
		}
		interval = nextPollInterval(interval, base, len(report.Actions) > 0, err != nil)
		time.Sleep(interval)
	}
}

// printWatchReport renders one pass. A dry run names every action and ends
// with the would-apply count; a real pass shows only removals, conflicts,
// deferrals and type mismatches unless verbose, and summarizes itself only
// when it had work.
func printWatchReport(out io.Writer, report driveignore.Report, dryRun, verbose bool, elapsed time.Duration) {
	deferred := 0
	for _, action := range report.Actions {
		if action.Kind == driveignore.ActionDeferred {
			deferred++
		}
		if !dryRun && !verbose && !watchAlwaysReports(action.Kind) {
			continue
		}
		line := fmt.Sprintf("%s: %s", action.Kind, action.Path)
		if action.Detail != "" {
			line = fmt.Sprintf("%s (%s)", line, action.Detail)
		}
		if dryRun {
			line = "would " + line
		}
		_, _ = fmt.Fprintln(out, line)
	}
	if dryRun {
		_, _ = fmt.Fprintf(out, "would apply %d action(s)\n", len(report.Actions))
		return
	}
	if len(report.Actions) > 0 {
		_, _ = fmt.Fprintf(out, "pass: %d action(s), %d deferred in %s\n", len(report.Actions), deferred, elapsed.Round(10*time.Millisecond))
	}
}

// watchAlwaysReports decides which kinds a real non-verbose pass prints:
// anything that removed, conflicted, deferred or could not be resolved is
// worth a line; routine creations and repairs stay silent unless verbose.
func watchAlwaysReports(kind driveignore.ActionKind) bool {
	switch kind {
	case driveignore.ActionTrashedLocal,
		driveignore.ActionRemovedDrive,
		driveignore.ActionConflict,
		driveignore.ActionTypeConflict,
		driveignore.ActionDeferred:
		return true
	default:
		return false
	}
}

func init() {
	rootCmd.AddCommand(newWatchCmd())
}

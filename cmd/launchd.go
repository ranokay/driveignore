package cmd

import (
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ranokay/driveignore/internal/driveignore"
)

// agentLabelPrefix is the reverse-DNS namespace of the launchd agents. The
// per-pair hash follows it, so journal, lock and agent share one name.
const agentLabelPrefix = "dev.ranokay.driveignore.watch."

// agentConfig is everything the launchd plist for one watch pair needs.
type agentConfig struct {
	Label   string
	Binary  string
	Input   string
	Output  string
	LogPath string
}

// agentLabel names the launchd agent for a pair. WatchPairHash is the single
// key for per-pair artifacts, so the label matches the journal and lock paths
// the watcher uses for the same raw input/output strings.
func agentLabel(input, output string) string {
	hash, err := driveignore.WatchPairHash(input, output)
	if err != nil {
		// WatchPairHash only fails when a relative pair cannot be resolved
		// against a missing working directory; the command resolves the pair
		// before calling this and reports that error, so this is unreachable.
		return agentLabelPrefix
	}
	return agentLabelPrefix + hash
}

// launchdPlist renders the agent definition. The builder is pure so the
// pinned content can be unit-tested; installAgent writes exactly this text.
// ProgramArguments is an array, so launchd execs the binary directly — paths
// with spaces survive — and every value is XML-escaped.
func launchdPlist(c agentConfig) string {
	var b strings.Builder
	write := func(s string) { _, _ = b.WriteString(s) }
	writeString := func(s string) {
		write("\t<string>")
		_ = xml.EscapeText(&b, []byte(s))
		write("</string>\n")
	}
	writeKey := func(key string) { write("\t<key>" + key + "</key>\n") }

	write("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	write("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	write("<plist version=\"1.0\">\n<dict>\n")

	writeKey("Label")
	writeString(c.Label)

	writeKey("ProgramArguments")
	write("\t<array>\n")
	// watch takes the drive folder as its positional argument and the source
	// through -i, the same shape a foreground run uses.
	for _, arg := range []string{c.Binary, "watch", c.Output, "-i", c.Input} {
		write("\t\t<string>")
		_ = xml.EscapeText(&b, []byte(arg))
		write("</string>\n")
	}
	write("\t</array>\n")

	writeKey("RunAtLoad")
	write("\t<true/>\n")
	// KeepAlive restarts the watcher after a crash, so a transient failure
	// cannot leave the pair silently unsynced until the next login.
	writeKey("KeepAlive")
	write("\t<true/>\n")

	writeKey("StandardOutPath")
	writeString(c.LogPath)
	writeKey("StandardErrorPath")
	writeString(c.LogPath)

	write("</dict>\n</plist>\n")
	return b.String()
}

// launchctlRunner executes launchctl for the agent lifecycle. The wrappers
// take one so the failure-recovery paths are testable without a real launchd.
type launchctlRunner func(args ...string) ([]byte, error)

// installAgent writes the pair's plist into ~/Library/LaunchAgents and loads
// it. The log directory is created first because launchd opens the standard
// streams itself and fails the job when their parent does not exist.
func installAgent(c agentConfig) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("watch agents use launchd and are only available on macOS; this is %s", runtime.GOOS)
	}
	return installAgentWith(c, launchctl)
}

// installAgentWith implements installAgent over an injectable launchctl. A
// service left loaded by an earlier install is replaced, and a failed
// bootstrap removes the plist this call wrote, so neither a re-install nor a
// retry is blocked by state from an earlier attempt and nothing half-installed
// can be loaded at the next login.
func installAgentWith(c agentConfig, run launchctlRunner) error {
	plistPath, err := agentPlistPath(c.Label)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(plistPath), err)
	}
	if err := os.MkdirAll(filepath.Dir(c.LogPath), 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", filepath.Dir(c.LogPath), err)
	}
	if err := os.WriteFile(plistPath, []byte(launchdPlist(c)), 0o644); err != nil {
		return fmt.Errorf("cannot write %s: %w", plistPath, err)
	}

	domain := fmt.Sprintf("gui/%d", os.Getuid())
	// Replace an instance from an earlier install; an unloaded or missing
	// service fails here harmlessly.
	_, _ = run("bootout", domain+"/"+c.Label)
	if _, err := run("bootstrap", domain, plistPath); err != nil {
		if removeErr := os.Remove(plistPath); removeErr != nil {
			return fmt.Errorf("cannot load launchd agent %s: %w (the failed plist at %s could not be removed: %w)", c.Label, err, plistPath, removeErr)
		}
		return fmt.Errorf("cannot load launchd agent %s: %w (removed the failed plist)", c.Label, err)
	}
	return nil
}

// uninstallAgent unloads the pair's agent and removes its plist.
func uninstallAgent(label string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("watch agents use launchd and are only available on macOS; this is %s", runtime.GOOS)
	}
	return uninstallAgentWith(label, launchctl)
}

// uninstallAgentWith implements uninstallAgent over an injectable launchctl. A
// bootout failure only blocks the removal while the service is still loaded: a
// failed install or an earlier uninstall leaves nothing to stop, and that
// state must not keep the plist around to resurrect the agent at login.
func uninstallAgentWith(label string, run launchctlRunner) error {
	plistPath, err := agentPlistPath(label)
	if err != nil {
		return err
	}
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	if _, err := run("bootout", target); err != nil {
		if agentLoaded(run, target) {
			return fmt.Errorf("cannot unload launchd agent %s: %w", label, err)
		}
	}
	if err := os.Remove(plistPath); err != nil {
		return fmt.Errorf("cannot remove %s: %w", plistPath, err)
	}
	return nil
}

// agentLoaded reports whether launchd still has the service bootstrapped; it
// takes the same gui/<uid>/<label> target bootout does.
func agentLoaded(run launchctlRunner, target string) bool {
	_, err := run("print", target)
	return err == nil
}

// agentPlistPath returns where launchd discovers the agent.
func agentPlistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// runWatchAgent handles watch --install/--uninstall. Both actions key off the
// pair hash the watcher itself uses, so the agent and a foreground run share
// one journal and lock.
func runWatchAgent(cmd *cobra.Command, input, output string, install, uninstall bool) error {
	if install && uninstall {
		return usageError{errors.New("--install and --uninstall cannot be combined")}
	}
	hash, err := driveignore.WatchPairHash(input, output)
	if err != nil {
		return err
	}
	label := agentLabel(input, output)

	if uninstall {
		if err := uninstallAgent(label); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "watch agent removed: %s\n", label)
		return nil
	}

	if err := requireDir(input); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate the driveignore binary: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	// launchd starts agents with / as the working directory, so the plist
	// records resolved paths; the hash above still names the pair the user
	// asked for.
	absInput, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	c := agentConfig{
		Label:   label,
		Binary:  binary,
		Input:   absInput,
		Output:  absOutput,
		LogPath: filepath.Join(home, "Library", "Logs", "driveignore", "watch-"+hash+".log"),
	}
	if err := installAgent(c); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "watch agent installed: %s\n", label)
	return nil
}

// launchctl runs the real launchctl and folds its output into the error, so a
// failed bootstrap tells the user what launchd complained about.
func launchctl(args ...string) ([]byte, error) {
	out, err := exec.Command("launchctl", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("launchctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

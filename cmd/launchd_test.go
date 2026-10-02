package cmd

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func TestLaunchdPlistPinsArgumentsAndLogs(t *testing.T) {
	cfg := agentConfig{
		Label:   "dev.ranokay.driveignore.watch.0123456789ab",
		Binary:  "/usr/local/bin/driveignore",
		Input:   "/Users/x/source",
		Output:  "/Users/x/Drive/my drive",
		LogPath: "/Users/x/Library/Logs/driveignore/watch-0123456789ab.log",
	}

	plist := launchdPlist(cfg)

	require.Contains(t, plist, "<key>Label</key>\n\t<string>dev.ranokay.driveignore.watch.0123456789ab</string>")
	require.Contains(t, plist, "<key>ProgramArguments</key>\n\t<array>\n"+
		"\t\t<string>/usr/local/bin/driveignore</string>\n"+
		"\t\t<string>watch</string>\n"+
		"\t\t<string>/Users/x/Drive/my drive</string>\n"+
		"\t\t<string>-i</string>\n"+
		"\t\t<string>/Users/x/source</string>\n"+
		"\t</array>")
	require.Contains(t, plist, "<key>RunAtLoad</key>\n\t<true/>")
	require.Contains(t, plist, "<key>KeepAlive</key>\n\t<true/>")
	require.Contains(t, plist, "<key>StandardOutPath</key>\n\t<string>/Users/x/Library/Logs/driveignore/watch-0123456789ab.log</string>")
	require.Contains(t, plist, "<key>StandardErrorPath</key>\n\t<string>/Users/x/Library/Logs/driveignore/watch-0123456789ab.log</string>")
	require.Equal(t, plist, launchdPlist(cfg), "the builder must be deterministic")
}

func TestLaunchdPlistEscapesXML(t *testing.T) {
	plist := launchdPlist(agentConfig{
		Label:   "label",
		Binary:  "binary",
		Input:   "/source/a&b",
		Output:  "/drive/<out>",
		LogPath: "/log",
	})

	require.Contains(t, plist, "<string>/source/a&amp;b</string>")
	require.Contains(t, plist, "<string>/drive/&lt;out&gt;</string>")
	require.NotContains(t, plist, "/source/a&b")
}

func TestAgentLabelIsStablePerPair(t *testing.T) {
	input, output := "/Users/x/source", "/Users/x/Drive/out"
	hash, err := driveignore.WatchPairHash(input, output)
	require.NoError(t, err)

	label := agentLabel(input, output)

	require.Equal(t, "dev.ranokay.driveignore.watch."+hash, label)
	require.Equal(t, label, agentLabel(input, output), "the same pair must map to the same agent")
	require.NotEqual(t, label, agentLabel(output, input), "the direction of the pair is part of its identity")
	require.NotEqual(t, label, agentLabel(input, output+"/other"))
}

// TestWatchRejectsInstallAndUninstallTogether pins the flag wiring; the usage
// error is raised before any launchd call, so the test is safe on every OS.
func TestWatchRejectsInstallAndUninstallTogether(t *testing.T) {
	isolateConfigHome(t)
	src, out := watchTestPair(t)

	code, stdout, stderr := runWatch(t, "watch", out, "-i", src, "--install", "--uninstall")

	require.Equal(t, 2, code, "combining the two agent modes is a usage error")
	require.Empty(t, stdout)
	require.Contains(t, stderr, "--install and --uninstall")
}

func TestInstallAgentRejectsNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("launchd is available on darwin; the real install is verified on a machine in Task 11")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	label := "dev.ranokay.driveignore.watch.test"

	err := installAgent(agentConfig{Label: label})
	require.Error(t, err)
	require.Contains(t, err.Error(), "macOS")
	require.NoDirExists(t, filepath.Join(home, "Library", "LaunchAgents"), "a rejected install must not write anything")

	err = uninstallAgent(label)
	require.Error(t, err)
	require.Contains(t, err.Error(), "macOS")
}

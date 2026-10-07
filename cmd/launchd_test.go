package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ranokay/driveignore/internal/driveignore"
)

func TestLaunchdPlistPinsArgumentsAndLogs(t *testing.T) {
	cfg := agentConfig{
		Label:   "dev.ranokay.driveignore.watch.0123456789ab",
		Args:    []string{"/usr/local/bin/driveignore", "watch", "/Users/x/Drive/my drive", "-i", "/Users/x/source"},
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
		Args:    []string{"binary", "watch", "/drive/<out>", "-i", "/source/a&b"},
		LogPath: "/log",
	})

	require.Contains(t, plist, "<string>/source/a&amp;b</string>")
	require.Contains(t, plist, "<string>/drive/&lt;out&gt;</string>")
	require.NotContains(t, plist, "/source/a&b")
}

func TestAgentLabelPinsTheArtifactHash(t *testing.T) {
	input, output := "/Users/x/source", "/Users/x/Drive/out"
	hash, err := driveignore.WatchPairHash(input, output)
	require.NoError(t, err)

	label := agentLabel(agentLabelPrefix, hash)

	require.Equal(t, "dev.ranokay.driveignore.watch."+hash, label)
	require.Equal(t, label, agentLabel(agentLabelPrefix, hash), "the same key must map to the same agent")
	require.NotEqual(t, label, agentLabel(guardLabelPrefix, hash), "watch and guard agents are distinct")
	otherHash, err := driveignore.WatchPairHash(output, input)
	require.NoError(t, err)
	require.NotEqual(t, label, agentLabel(agentLabelPrefix, otherHash), "the direction of the pair is part of its identity")
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

// agentTestHome points the agent paths at a scratch directory so the recovery
// tests can write and remove plists without a real launchd.
func agentTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// agentTestConfig builds a config whose log path proves nested directories are
// created by install, not by the test's own temp tree.
func agentTestConfig(t *testing.T, label string) agentConfig {
	t.Helper()
	return agentConfig{
		Label:   label,
		Args:    []string{"/bin/driveignore", "watch", "/out", "-i", "/in"},
		LogPath: filepath.Join(t.TempDir(), "Library", "Logs", "driveignore", "watch-test.log"),
	}
}

// plantStalePlist leaves a plist behind as a failed install would, returning
// its path.
func plantStalePlist(t *testing.T, label string) string {
	t.Helper()
	plistPath, err := agentPlistPath(label)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(plistPath), 0o755))
	require.NoError(t, os.WriteFile(plistPath, []byte("stale"), 0o644))
	return plistPath
}

func TestInstallAgentWritesLoadsAndReplacesExisting(t *testing.T) {
	agentTestHome(t)
	label := "dev.ranokay.driveignore.watch.test"
	cfg := agentTestConfig(t, label)
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, nil
	}

	require.NoError(t, installAgentWith(cfg, run))

	plistPath, err := agentPlistPath(label)
	require.NoError(t, err)
	content, err := os.ReadFile(plistPath)
	require.NoError(t, err)
	require.Equal(t, launchdPlist(cfg), string(content))
	require.DirExists(t, filepath.Dir(cfg.LogPath), "install must create the log directory")
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	require.Equal(t, [][]string{
		{"bootout", target},
		{"bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath},
	}, calls, "install must replace an already-loaded service before bootstrapping")
}

func TestInstallAgentRemovesPlistWhenBootstrapFails(t *testing.T) {
	agentTestHome(t)
	label := "dev.ranokay.driveignore.watch.test"
	cfg := agentTestConfig(t, label)
	run := func(args ...string) ([]byte, error) {
		if args[0] == "bootstrap" {
			return nil, errors.New("bootstrap rejected the plist")
		}
		return nil, errors.New("no such service")
	}

	err := installAgentWith(cfg, run)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot load launchd agent")
	plistPath, pathErr := agentPlistPath(label)
	require.NoError(t, pathErr)
	require.NoFileExists(t, plistPath, "a failed bootstrap must not leave a plist to load at login")
}

func TestUninstallAgentRemovesPlistWhenNotLoaded(t *testing.T) {
	agentTestHome(t)
	label := "dev.ranokay.driveignore.watch.test"
	plistPath := plantStalePlist(t, label)
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, errors.New("no such service")
	}

	require.NoError(t, uninstallAgentWith(label, run))

	require.NoFileExists(t, plistPath)
	require.Equal(t, [][]string{
		{"bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)},
		{"print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)},
	}, calls)
}

func TestUninstallAgentKeepsPlistWhenStillLoaded(t *testing.T) {
	agentTestHome(t)
	label := "dev.ranokay.driveignore.watch.test"
	plistPath := plantStalePlist(t, label)
	run := func(args ...string) ([]byte, error) {
		switch args[0] {
		case "bootout":
			return nil, errors.New("boot-out rejected")
		case "print":
			return []byte("service is loaded"), nil
		}
		return nil, errors.New("unexpected " + args[0])
	}

	err := uninstallAgentWith(label, run)

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot unload")
	require.FileExists(t, plistPath, "a still-loaded agent must keep its plist")
}

func TestUninstallAgentRemovesPlistAfterBootout(t *testing.T) {
	agentTestHome(t)
	label := "dev.ranokay.driveignore.watch.test"
	plistPath := plantStalePlist(t, label)
	var calls [][]string
	run := func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, nil
	}

	require.NoError(t, uninstallAgentWith(label, run))

	require.NoFileExists(t, plistPath)
	require.Equal(t, [][]string{{"bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)}}, calls,
		"a successful bootout needs no liveness check")
}

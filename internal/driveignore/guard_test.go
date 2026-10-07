package driveignore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// sealFake is an in-memory stand-in for the File Provider ignore attribute, so
// the guard pass is testable on every platform. It keys by path; the one place
// a real filesystem would distinguish inodes (a deleted and recreated
// directory) is simulated by deleting the fake entry alongside the directory.
type sealFake struct {
	attrs   map[string]bool
	seals   int
	unseals int
}

func newSealFake() *sealFake {
	return &sealFake{attrs: map[string]bool{}}
}

func (f *sealFake) set(path string) error {
	f.seals++
	f.attrs[path] = true
	return nil
}

func (f *sealFake) clear(path string) error {
	f.unseals++
	delete(f.attrs, path)
	return nil
}

func (f *sealFake) has(path string) (bool, error) {
	return f.attrs[path], nil
}

func (f *sealFake) sealed(path string) bool {
	return f.attrs[path]
}

// guardConfig builds an invocation whose global rules point at a path that
// never exists, so no test reads or writes the user's real global ignore file.
func guardConfig(root string) (Config, *sealFake) {
	fake := newSealFake()
	return Config{
		Input: root,
		globalPathFn: func() (string, error) {
			return filepath.Join(os.TempDir(), "driveignore-test-missing", ".global_driveignore"), nil
		},
		setSealFn:   fake.set,
		clearSealFn: fake.clear,
		hasSealFn:   fake.has,
	}, fake
}

func newGuardFor(t *testing.T, root string) (*Guard, *sealFake) {
	t.Helper()
	cfg, fake := guardConfig(root)
	g, err := NewGuard(cfg)
	require.NoError(t, err)
	return g, fake
}

func TestGuardSealsMatchingDirectoryAndSkipsSubtree(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "node_modules/\n")
	write(t, filepath.Join(root, "node_modules", "pkg", "index.js"), "x")
	write(t, filepath.Join(root, "keep.txt"), "y")

	g, fake := newGuardFor(t, root)
	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "node_modules"}}, report.Actions)
	require.True(t, fake.sealed(filepath.Join(root, "node_modules")))
	require.False(t, fake.sealed(filepath.Join(root, "node_modules", "pkg")),
		"the sealed directory covers its subtree; children are not stamped individually")
	require.False(t, fake.sealed(filepath.Join(root, "keep.txt")))

	report, err = g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Empty(t, report.Actions, "a sealed, unchanged directory is a no-op")

	write(t, filepath.Join(root, "node_modules", "pkg2", "x.js"), "z")
	report, err = g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Empty(t, report.Actions, "changes inside a sealed directory stay untouched")
}

func TestGuardSealsMatchingFilesAndLeavesOthers(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "*.log\n")
	write(t, filepath.Join(root, "a.log"), "x")
	write(t, filepath.Join(root, "keep.txt"), "y")

	g, fake := newGuardFor(t, root)
	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "a.log"}}, report.Actions)
	require.True(t, fake.sealed(filepath.Join(root, "a.log")))
	require.False(t, fake.sealed(filepath.Join(root, "keep.txt")))
}

func TestGuardUnsealsWhenTheRuleIsRemoved(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, ".driveignore")
	write(t, rules, "node_modules/\n")
	write(t, filepath.Join(root, "node_modules", "x.js"), "x")

	g, fake := newGuardFor(t, root)
	_, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.True(t, fake.sealed(filepath.Join(root, "node_modules")))

	write(t, rules, "# nothing is ignored now\n")
	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionUnsealed, Path: "node_modules"}}, report.Actions)
	require.False(t, fake.sealed(filepath.Join(root, "node_modules")))
}

func TestGuardUnsealsFilesWhenRulesStopMatching(t *testing.T) {
	root := t.TempDir()
	rules := filepath.Join(root, ".driveignore")
	write(t, rules, "*.log\n")
	write(t, filepath.Join(root, "a.log"), "x")

	g, fake := newGuardFor(t, root)
	_, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.True(t, fake.sealed(filepath.Join(root, "a.log")))

	write(t, rules, "# nothing is ignored now\n")
	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionUnsealed, Path: "a.log"}}, report.Actions)
	require.False(t, fake.sealed(filepath.Join(root, "a.log")))
}

func TestGuardRestoresManuallyRemovedStamps(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "node_modules/\n")
	write(t, filepath.Join(root, "node_modules", "x.js"), "x")

	g, fake := newGuardFor(t, root)
	_, err := g.Pass(GuardOptions{})
	require.NoError(t, err)

	delete(fake.attrs, filepath.Join(root, "node_modules"))

	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "node_modules"}}, report.Actions)
	require.True(t, fake.sealed(filepath.Join(root, "node_modules")),
		"a stamp removed outside the guard comes back without waiting for a change")
}

func TestGuardResealsRecreatedDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "dist/\n")
	write(t, filepath.Join(root, "dist", "out.js"), "x")

	g, fake := newGuardFor(t, root)
	_, err := g.Pass(GuardOptions{})
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(root, "dist")))
	delete(fake.attrs, filepath.Join(root, "dist")) // the stamp lived on the removed inode
	write(t, filepath.Join(root, "dist", "out.js"), "y")

	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "dist"}}, report.Actions)
	require.True(t, fake.sealed(filepath.Join(root, "dist")))
}

func TestGuardDryRunNeverMutates(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "node_modules/\n")
	write(t, filepath.Join(root, "node_modules", "x.js"), "x")

	cfg, fake := guardConfig(root)
	g, err := NewGuard(cfg)
	require.NoError(t, err)

	report, err := g.Pass(GuardOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "node_modules"}}, report.Actions)
	require.Empty(t, fake.attrs, "a dry run must not stamp anything")
	require.Zero(t, fake.seals+fake.unseals)
}

func TestGuardPrunesUnchangedDirectories(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "node_modules/\n")
	write(t, filepath.Join(root, "src", "a.go"), "x")

	cfg, _ := guardConfig(root)
	var ticks []string
	cfg.Progress = func(rel string) { ticks = append(ticks, rel) }
	g, err := NewGuard(cfg)
	require.NoError(t, err)
	_, err = g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Contains(t, ticks, "src/a.go")

	ticks = nil
	_, err = g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Contains(t, ticks, "src", "unchanged directories are still compared")
	require.NotContains(t, ticks, "src/a.go", "children of an unchanged directory are not revisited")
}

func TestGuardNeverSealsRuleFilesOrGoogleStubs(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "*\n")
	write(t, filepath.Join(root, "doc.gdoc"), "{}")
	write(t, filepath.Join(root, "regular.txt"), "x")

	g, fake := newGuardFor(t, root)
	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "regular.txt"}}, report.Actions)
	require.False(t, fake.sealed(filepath.Join(root, ".driveignore")))
	require.False(t, fake.sealed(filepath.Join(root, "doc.gdoc")),
		"stamping a Google stub could affect the underlying cloud document")
	require.True(t, fake.sealed(filepath.Join(root, "regular.txt")))
}

func TestGuardSkipsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs elevated privileges on windows")
	}
	root := t.TempDir()
	write(t, filepath.Join(root, ".driveignore"), "*\n")
	write(t, filepath.Join(root, "target.txt"), "x")
	require.NoError(t, os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")))

	g, fake := newGuardFor(t, root)
	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "target.txt"}}, report.Actions)
	require.False(t, fake.sealed(filepath.Join(root, "link.txt")))
}

func TestGuardRequiresRules(t *testing.T) {
	cfg, _ := guardConfig(t.TempDir())

	_, err := NewGuard(cfg)
	require.ErrorContains(t, err, "no .driveignore found")
}

func TestGuardFallsBackToTheGlobalRules(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(t.TempDir(), ".global_driveignore")
	write(t, global, "node_modules/\n")
	write(t, filepath.Join(root, "node_modules", "x"), "x")

	fake := newSealFake()
	cfg := Config{
		Input:        root,
		globalPathFn: func() (string, error) { return global, nil },
		setSealFn:    fake.set,
		clearSealFn:  fake.clear,
		hasSealFn:    fake.has,
	}
	g, err := NewGuard(cfg)
	require.NoError(t, err)

	report, err := g.Pass(GuardOptions{})
	require.NoError(t, err)
	require.Equal(t, []Action{{Kind: ActionSealed, Path: "node_modules"}}, report.Actions)
	require.True(t, fake.sealed(filepath.Join(root, "node_modules")))
}

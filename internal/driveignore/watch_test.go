package driveignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncedPair builds a converged pair — a .driveignore, a file, a nested file
// and an empty directory, hardlinked on both sides — plus a journal proving
// every path synced, saved under its own temporary directory.
func syncedPair(t *testing.T) (src, out, state string) {
	t.Helper()
	src, out = t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(src, "sub", "nested.txt"), "nested")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "empty"), 0o755))
	state = watchStatePathIn(t, t.TempDir(), src, out)
	saveSyncedState(t, src, out, state)
	return src, out, state
}

// saveSyncedState mirrors src into out with hardlinks and writes a journal
// proving the pair synced: file entries carry the shared inode, size and
// mtime, directory entries the two change stamps. Tests call it again after
// adding synced paths, the way a completed pass would commit them.
func saveSyncedState(t *testing.T, src, out, state string) {
	t.Helper()
	j := &journal{Version: journalVersion, Input: src, Output: out, Entries: map[string]journalEntry{}}
	require.NoError(t, Walk(src, func(path string, entry fs.DirEntry, rel string) error {
		goal := filepath.Join(out, rel)
		if entry.IsDir() {
			return os.MkdirAll(goal, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		inode, err := fileInode(path)
		if err != nil {
			return err
		}
		if err := os.Remove(goal); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Link(path, goal); err != nil {
			return err
		}
		j.Entries[rel] = journalEntry{Type: "file", Inode: inode, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		return nil
	}))
	// Directory stamps are computed once every file exists: creating a file
	// moves its directory's stamp.
	require.NoError(t, Walk(src, func(_ string, entry fs.DirEntry, rel string) error {
		if !entry.IsDir() {
			return nil
		}
		localStamp, err := dirChangeStamp(filepath.Join(src, rel))
		if err != nil {
			return err
		}
		outStamp, err := dirChangeStamp(filepath.Join(out, rel))
		if err != nil {
			return err
		}
		j.Entries[rel] = journalEntry{Type: "dir", LocalStamp: localStamp, OutStamp: outStamp}
		return nil
	}))
	require.NoError(t, saveJournal(state, j))
}

func watchOpts(t *testing.T) WatchOptions {
	t.Helper()
	return WatchOptions{
		DryRun:   true,
		MinAge:   time.Nanosecond,
		TrashDir: filepath.Join(t.TempDir(), "Trash"),
	}
}

// applyOpts is watchOpts with the dry run switched off, for passes that must
// mutate the trees and commit the journal.
func applyOpts(t *testing.T) WatchOptions {
	t.Helper()
	opts := watchOpts(t)
	opts.DryRun = false
	return opts
}

func tickCollector(cfg *Config) *[]string {
	ticks := &[]string{}
	cfg.Progress = func(rel string) { *ticks = append(*ticks, rel) }
	return ticks
}

// replaceFile rewrites path through a temp sibling and a rename, so the file
// gets a new inode, the way an editor's atomic save or the Drive client's
// replace does.
func replaceFile(t *testing.T, path, content string) {
	t.Helper()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".watch-test-*")
	require.NoError(t, err)
	_, err = tmp.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	require.NoError(t, os.Rename(tmp.Name(), path))
}

// backdate pushes a file's modification time into the past, the way a
// restore-from-backup or a skewed clock would.
func backdate(t *testing.T, path string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, past, past))
}

// setMtime pins a file's modification time so a conflict's winner and its copy
// name are deterministic instead of racing the filesystem clock.
func setMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, at, at))
}

// treeSnapshot captures every path below root with the data a pass must not
// change: inode, size and kind.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	require.NoError(t, Walk(root, func(path string, entry fs.DirEntry, rel string) error {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		inode, err := fileInode(path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(rel)] = fmt.Sprintf("%d/%d/%v", inode, info.Size(), entry.IsDir())
		return nil
	}))
	return snapshot
}

func TestReconcileDryRunClassifiesEveryDecisionRow(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, src, out, state string)
		want  []Action
	}{
		{
			name: "both sides same inode",
		},
		{
			name: "local anchor, drive replaced wins",
			setup: func(t *testing.T, src, out, state string) {
				replaceFile(t, filepath.Join(out, "keep.txt"), "drive")
			},
			want: []Action{{ActionRelinked, "keep.txt", "drive wins"}},
		},
		{
			name: "drive anchor, local replaced wins",
			setup: func(t *testing.T, src, out, state string) {
				replaceFile(t, filepath.Join(src, "keep.txt"), "local")
			},
			want: []Action{{ActionRelinked, "keep.txt", "local wins"}},
		},
		{
			name: "neither anchor conflicts",
			setup: func(t *testing.T, src, out, state string) {
				replaceFile(t, filepath.Join(src, "keep.txt"), "local")
				replaceFile(t, filepath.Join(out, "keep.txt"), "drive")
				backdate(t, filepath.Join(src, "keep.txt"))
			},
			want: []Action{{ActionConflict, "keep.txt", "drive wins"}},
		},
		{
			name: "local-only new links",
			setup: func(t *testing.T, src, out, state string) {
				write(t, filepath.Join(src, "added.txt"), "added")
			},
			want: []Action{{ActionLinked, "added.txt", ""}},
		},
		{
			name: "local-only journaled trashes local",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
			},
			want: []Action{{ActionTrashedLocal, "keep.txt", ""}},
		},
		{
			name: "local-only journaled anchor mismatch links",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
				replaceFile(t, filepath.Join(src, "keep.txt"), "recreated")
			},
			want: []Action{{ActionLinked, "keep.txt", ""}},
		},
		{
			name: "local-only journaled backdated survivor links",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
				backdate(t, filepath.Join(src, "keep.txt"))
			},
			want: []Action{{ActionLinked, "keep.txt", ""}},
		},
		{
			name: "drive-only new imports",
			setup: func(t *testing.T, src, out, state string) {
				write(t, filepath.Join(out, "added.txt"), "added")
			},
			want: []Action{{ActionImported, "added.txt", ""}},
		},
		{
			name: "drive-only journaled removes drive",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
			},
			want: []Action{{ActionRemovedDrive, "keep.txt", ""}},
		},
		{
			name: "drive-only journaled anchor mismatch imports",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
				replaceFile(t, filepath.Join(out, "keep.txt"), "replaced")
			},
			want: []Action{{ActionImported, "keep.txt", ""}},
		},
		{
			name: "drive-only journaled backdated survivor imports",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
				backdate(t, filepath.Join(out, "keep.txt"))
			},
			want: []Action{{ActionImported, "keep.txt", ""}},
		},
		{
			name: "both missing drops silently",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
				require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
			},
		},
		{
			name: "type mismatch reports",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
				require.NoError(t, os.MkdirAll(filepath.Join(out, "keep.txt"), 0o755))
			},
			want: []Action{{ActionTypeConflict, "keep.txt", ""}},
		},
		{
			name: "local-only empty journaled dir trashes",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(out, "empty")))
			},
			want: []Action{{ActionTrashedLocal, "empty", ""}},
		},
		{
			name: "drive-only empty journaled dir removes",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.Remove(filepath.Join(src, "empty")))
			},
			want: []Action{{ActionRemovedDrive, "empty", ""}},
		},
		{
			name: "local-only new dir creates",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.MkdirAll(filepath.Join(src, "newdir"), 0o755))
			},
			want: []Action{{ActionCreatedDir, "newdir", ""}},
		},
		{
			name: "drive-only new dir creates",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.MkdirAll(filepath.Join(out, "newdir"), 0o755))
			},
			want: []Action{{ActionCreatedDir, "newdir", ""}},
		},
		{
			name: "journaled non-empty dir missing on drive touches only children",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.RemoveAll(filepath.Join(out, "sub")))
			},
			want: []Action{{ActionTrashedLocal, "sub/nested.txt", ""}},
		},
		{
			name: "deletions order deepest first",
			setup: func(t *testing.T, src, out, state string) {
				require.NoError(t, os.RemoveAll(filepath.Join(out, "sub")))
				require.NoError(t, os.Remove(filepath.Join(out, "empty")))
			},
			want: []Action{
				{ActionTrashedLocal, "sub/nested.txt", ""},
				{ActionTrashedLocal, "empty", ""},
			},
		},
		{
			name: "creations order ascending",
			setup: func(t *testing.T, src, out, state string) {
				write(t, filepath.Join(src, "newdir", "a.txt"), "a")
				write(t, filepath.Join(src, "zz.txt"), "z")
			},
			want: []Action{
				{ActionCreatedDir, "newdir", ""},
				{ActionLinked, "newdir/a.txt", ""},
				{ActionLinked, "zz.txt", ""},
			},
		},
		{
			name: "unicode local-only file links",
			setup: func(t *testing.T, src, out, state string) {
				write(t, filepath.Join(src, "ünï code", "深.txt"), "x")
			},
			want: []Action{
				{ActionCreatedDir, "ünï code", ""},
				{ActionLinked, "ünï code/深.txt", ""},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, out, state := syncedPair(t)
			if tt.setup != nil {
				tt.setup(t, src, out, state)
			}
			report, err := Reconcile(baseConfig(t, src, out), state, watchOpts(t))
			require.NoError(t, err)
			if tt.want == nil {
				require.Empty(t, report.Actions)
				return
			}
			require.Equal(t, tt.want, report.Actions)
		})
	}
}

func TestReconcileDryRunNeverWrites(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "added.txt"), "added")
	require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))

	stateBefore := read(t, state)
	srcBefore := treeSnapshot(t, src)
	outBefore := treeSnapshot(t, out)

	report, err := Reconcile(baseConfig(t, src, out), state, watchOpts(t))
	require.NoError(t, err)
	require.NotEmpty(t, report.Actions, "the dry run must report the pending work")

	require.Equal(t, stateBefore, read(t, state), "a dry run must not touch the journal")
	require.Equal(t, srcBefore, treeSnapshot(t, src), "a dry run must not touch the source tree")
	require.Equal(t, outBefore, treeSnapshot(t, out), "a dry run must not touch the drive tree")
}

func TestReconcileOneWayIsLocalAuthoritative(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "pair.txt"), "pair")
	saveSyncedState(t, src, out, state)

	// A drive-only file is not imported: the local side is the authority, so
	// the drive entry goes away instead.
	write(t, filepath.Join(out, "incoming.txt"), "from drive")
	require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
	replaceFile(t, filepath.Join(src, "sub", "nested.txt"), "local edited")
	require.NoError(t, os.Remove(filepath.Join(src, "pair.txt")))

	opts := watchOpts(t)
	opts.OneWay = true
	report, err := Reconcile(baseConfig(t, src, out), state, opts)
	require.NoError(t, err)
	require.Equal(t, []Action{
		{ActionLinked, "keep.txt", ""},
		{ActionRelinked, "sub/nested.txt", "local wins"},
		{ActionRemovedDrive, "incoming.txt", ""},
		{ActionRemovedDrive, "pair.txt", ""},
	}, report.Actions)
}

func TestReconcilePrunesUntouchedSubtrees(t *testing.T) {
	probe, err := dirChangeStamp(t.TempDir())
	require.NoError(t, err)
	if probe == 0 {
		t.Skip("change stamps are unavailable on this platform")
	}

	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "touched", "old.txt"), "old")
	write(t, filepath.Join(src, "untouched", "old.txt"), "old")
	write(t, filepath.Join(src, "raw", "zero.txt"), "zero")
	saveSyncedState(t, src, out, state)

	// A directory the journal only knows with zero stamps can never be
	// pruned: unknown means always walk.
	j, ok := loadJournal(state)
	require.True(t, ok)
	j.Entries["raw"] = journalEntry{Type: "dir"}
	require.NoError(t, saveJournal(state, j))

	write(t, filepath.Join(src, "touched", "new.txt"), "new")

	cfg := baseConfig(t, src, out)
	ticks := tickCollector(&cfg)
	report, err := Reconcile(cfg, state, watchOpts(t))
	require.NoError(t, err)

	require.Equal(t, []Action{{ActionLinked, "touched/new.txt", ""}}, report.Actions)
	require.Contains(t, *ticks, "touched")
	require.Contains(t, *ticks, "touched/new.txt")
	require.NotContains(t, *ticks, "untouched", "an unchanged subtree must not be walked")
	require.NotContains(t, *ticks, "untouched/old.txt", "an unchanged subtree must not be walked")
	require.Contains(t, *ticks, "raw", "a zero stamp must force a walk")
	require.Contains(t, *ticks, "raw/zero.txt", "a zero stamp must force a walk")
}

func TestReconcileSkipsIgnoredAndSymlinks(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, ".driveignore"), "ignored.txt\n")
	write(t, filepath.Join(src, "ignored.txt"), "local junk")
	write(t, filepath.Join(out, "ignored.txt"), "drive junk")
	write(t, filepath.Join(src, "notes.gdoc"), "stub")
	write(t, filepath.Join(out, "book.gsheet"), "stub")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink("keep.txt", filepath.Join(src, "local-link")))
		require.NoError(t, os.Symlink("keep.txt", filepath.Join(out, "drive-link")))
	}

	stateBefore := read(t, state)
	report, err := Reconcile(baseConfig(t, src, out), state, watchOpts(t))
	require.NoError(t, err)
	require.Empty(t, report.Actions, "excluded paths must produce no actions")
	require.Equal(t, stateBefore, read(t, state), "excluded paths must never be journaled")
}

func TestReconcileUnusableStateLogsAndTreatsAsCreations(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, state, "{not json")
	write(t, filepath.Join(src, "new.txt"), "new")
	require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))

	var logs []string
	cfg := baseConfig(t, src, out)
	cfg.Log = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

	report, err := Reconcile(cfg, state, watchOpts(t))
	require.NoError(t, err)
	// Without a usable journal there is no deletion proof: keep.txt must be
	// linked back, never trashed.
	require.Equal(t, []Action{
		{ActionLinked, "keep.txt", ""},
		{ActionLinked, "new.txt", ""},
	}, report.Actions)
	require.Contains(t, strings.Join(logs, "\n"), "unusable")
}

func TestReconcileDefersFreshlyModifiedFiles(t *testing.T) {
	t.Run("new file", func(t *testing.T) {
		src, out, state := syncedPair(t)
		write(t, filepath.Join(src, "fresh.txt"), "still being written")

		opts := watchOpts(t)
		opts.MinAge = time.Hour
		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionDeferred, "fresh.txt", ""}}, report.Actions)

		opts.MinAge = time.Nanosecond
		report, err = Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionLinked, "fresh.txt", ""}}, report.Actions)
	})

	t.Run("local deletion candidate", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
		write(t, filepath.Join(src, "keep.txt"), "edited while the drive copy was gone")

		opts := watchOpts(t)
		opts.MinAge = time.Hour
		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionDeferred, "keep.txt", ""}}, report.Actions)
	})

	t.Run("drive deletion candidate", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
		write(t, filepath.Join(out, "keep.txt"), "edited while the local copy was gone")

		opts := watchOpts(t)
		opts.MinAge = time.Hour
		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionDeferred, "keep.txt", ""}}, report.Actions)
	})

	t.Run("conflict", func(t *testing.T) {
		src, out, state := syncedPair(t)
		replaceFile(t, filepath.Join(src, "keep.txt"), "local")
		replaceFile(t, filepath.Join(out, "keep.txt"), "drive")

		opts := watchOpts(t)
		opts.MinAge = time.Hour
		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionDeferred, "keep.txt", ""}}, report.Actions)
	})
}

func TestReconcileLinksAndCommitsState(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "local-new.txt"), "from local")
	write(t, filepath.Join(out, "drive-new.txt"), "from drive")

	report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{
		{ActionImported, "drive-new.txt", ""},
		{ActionLinked, "local-new.txt", ""},
	}, report.Actions)

	assertLinked(t, filepath.Join(src, "local-new.txt"), filepath.Join(out, "local-new.txt"))
	assertLinked(t, filepath.Join(out, "drive-new.txt"), filepath.Join(src, "drive-new.txt"))
	assertNoTempEntries(t, src)
	assertNoTempEntries(t, out)

	j, ok := loadJournal(state)
	require.True(t, ok, "a committed journal must load back")
	require.Equal(t, "file", j.Entries["local-new.txt"].Type)
	require.Equal(t, "file", j.Entries["drive-new.txt"].Type)
	localInode, err := fileInode(filepath.Join(src, "local-new.txt"))
	require.NoError(t, err)
	driveInode, err := fileInode(filepath.Join(src, "drive-new.txt"))
	require.NoError(t, err)
	require.Equal(t, localInode, j.Entries["local-new.txt"].Inode)
	require.Equal(t, driveInode, j.Entries["drive-new.txt"].Inode)

	second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Empty(t, second.Actions, "a committed pass must leave nothing to do")
}

func TestReconcileRepairsBrokenLinksInBothDirections(t *testing.T) {
	t.Run("local wins", func(t *testing.T) {
		src, out, state := syncedPair(t)
		replaceFile(t, filepath.Join(src, "keep.txt"), "local edit")

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionRelinked, "keep.txt", "local wins"}}, report.Actions)

		require.Equal(t, "local edit", read(t, filepath.Join(src, "keep.txt")))
		require.Equal(t, "local edit", read(t, filepath.Join(out, "keep.txt")))
		assertLinked(t, filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt"))

		j, ok := loadJournal(state)
		require.True(t, ok)
		inode, err := fileInode(filepath.Join(src, "keep.txt"))
		require.NoError(t, err)
		require.Equal(t, inode, j.Entries["keep.txt"].Inode, "the anchor must follow the repair")

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})

	t.Run("drive wins", func(t *testing.T) {
		src, out, state := syncedPair(t)
		replaceFile(t, filepath.Join(out, "keep.txt"), "drive edit")

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionRelinked, "keep.txt", "drive wins"}}, report.Actions)

		require.Equal(t, "drive edit", read(t, filepath.Join(src, "keep.txt")))
		require.Equal(t, "drive edit", read(t, filepath.Join(out, "keep.txt")))
		assertLinked(t, filepath.Join(out, "keep.txt"), filepath.Join(src, "keep.txt"))

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})
}

func TestReconcileCreatesEmptyDirsAndHandlesUnicodeDeepPaths(t *testing.T) {
	const (
		unicodeDir = "ünï code"
		emptyDir   = unicodeDir + "/empty 空"
		deepDir    = unicodeDir + "/深/very/deep"
		deepFile   = deepDir + "/path file.txt"
	)

	t.Run("local to drive", func(t *testing.T) {
		src, out, state := syncedPair(t)
		write(t, filepath.Join(src, filepath.FromSlash(deepFile)), "unicode")
		require.NoError(t, os.MkdirAll(filepath.Join(src, filepath.FromSlash(emptyDir)), 0o755))

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{
			{ActionCreatedDir, unicodeDir, ""},
			{ActionCreatedDir, emptyDir, ""},
			{ActionCreatedDir, unicodeDir + "/深", ""},
			{ActionCreatedDir, unicodeDir + "/深/very", ""},
			{ActionCreatedDir, deepDir, ""},
			{ActionLinked, deepFile, ""},
		}, report.Actions)

		require.DirExists(t, filepath.Join(out, filepath.FromSlash(emptyDir)))
		require.Equal(t, "unicode", read(t, filepath.Join(out, filepath.FromSlash(deepFile))))
		assertLinked(t, filepath.Join(src, filepath.FromSlash(deepFile)), filepath.Join(out, filepath.FromSlash(deepFile)))
		assertNoTempEntries(t, out)

		j, ok := loadJournal(state)
		require.True(t, ok)
		require.Equal(t, "dir", j.Entries[filepath.Join("ünï code", "深", "very", "deep")].Type)
		require.NotZero(t, j.Entries[filepath.Join("ünï code", "深", "very", "deep")].LocalStamp)

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})

	t.Run("drive to local", func(t *testing.T) {
		src, out, state := syncedPair(t)
		write(t, filepath.Join(out, filepath.FromSlash(deepFile)), "unicode")
		require.NoError(t, os.MkdirAll(filepath.Join(out, filepath.FromSlash(emptyDir)), 0o755))

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{
			{ActionCreatedDir, unicodeDir, ""},
			{ActionCreatedDir, emptyDir, ""},
			{ActionCreatedDir, unicodeDir + "/深", ""},
			{ActionCreatedDir, unicodeDir + "/深/very", ""},
			{ActionCreatedDir, deepDir, ""},
			{ActionImported, deepFile, ""},
		}, report.Actions)

		require.DirExists(t, filepath.Join(src, filepath.FromSlash(emptyDir)))
		require.Equal(t, "unicode", read(t, filepath.Join(src, filepath.FromSlash(deepFile))))
		assertLinked(t, filepath.Join(out, filepath.FromSlash(deepFile)), filepath.Join(src, filepath.FromSlash(deepFile)))
		assertNoTempEntries(t, src)

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})
}

func TestReconcileAbortsBeforeCommitOnFailure(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "a-first.txt"), "first")
	write(t, filepath.Join(src, "b-second.txt"), "second")
	stateBefore := read(t, state)

	boom := errors.New("link boom")
	cfg := baseConfig(t, src, out)
	links := 0
	cfg.linkFn = func(oldname, newname string) error {
		links++
		if links == 2 {
			return boom
		}
		return os.Link(oldname, newname)
	}

	_, err := Reconcile(cfg, state, applyOpts(t))
	require.ErrorIs(t, err, boom)
	require.Equal(t, 2, links, "the pass must stop at the failing install")
	require.Equal(t, stateBefore, read(t, state), "a failed pass must not commit the journal")
	assertNoTempEntries(t, out)

	// The next clean pass finishes the interrupted work and converges.
	report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionLinked, "b-second.txt", ""}}, report.Actions)
	assertLinked(t, filepath.Join(src, "a-first.txt"), filepath.Join(out, "a-first.txt"))
	assertLinked(t, filepath.Join(src, "b-second.txt"), filepath.Join(out, "b-second.txt"))

	j, ok := loadJournal(state)
	require.True(t, ok)
	require.Equal(t, "file", j.Entries["a-first.txt"].Type)
	require.Equal(t, "file", j.Entries["b-second.txt"].Type)

	third, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Empty(t, third.Actions)
}

func TestReconcileDeletesBothDirectionsFromJournalProof(t *testing.T) {
	t.Run("local delete removes the drive entry", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(src, "sub", "nested.txt")))

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionRemovedDrive, "sub/nested.txt", ""}}, report.Actions)

		require.NoFileExists(t, filepath.Join(src, "sub", "nested.txt"))
		require.NoFileExists(t, filepath.Join(out, "sub", "nested.txt"))

		j, ok := loadJournal(state)
		require.True(t, ok)
		require.NotContains(t, j.Entries, filepath.Join("sub", "nested.txt"))
		// An applied deletion is settled: its parent refreshes its stamps so
		// the next scan can prune the subtree instead of walking it forever.
		localStamp, err := dirChangeStamp(filepath.Join(src, "sub"))
		require.NoError(t, err)
		require.Equal(t, localStamp, j.Entries["sub"].LocalStamp)

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})

	t.Run("drive delete trashes the local file", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))

		opts := applyOpts(t)
		require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))

		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionTrashedLocal, "keep.txt", ""}}, report.Actions)

		require.NoFileExists(t, filepath.Join(src, "keep.txt"))
		require.Equal(t, "keep", read(t, filepath.Join(opts.TrashDir, "keep.txt")))

		j, ok := loadJournal(state)
		require.True(t, ok)
		require.NotContains(t, j.Entries, "keep.txt")

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})

	t.Run("drive removal failure keeps the journal and the entry", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
		stateBefore := read(t, state)

		boom := errors.New("remove boom")
		cfg := baseConfig(t, src, out)
		cfg.removeFn = func(string) error { return boom }

		_, err := Reconcile(cfg, state, applyOpts(t))
		require.ErrorIs(t, err, boom)
		require.Equal(t, stateBefore, read(t, state), "a failed removal must not commit the journal")
		require.Equal(t, "keep", read(t, filepath.Join(out, "keep.txt")), "a failed removal must leave the drive entry alone")
	})

	t.Run("local delete removes the empty drive directory", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(src, "empty")))

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionRemovedDrive, "empty", ""}}, report.Actions)

		require.NoDirExists(t, filepath.Join(src, "empty"))
		require.NoDirExists(t, filepath.Join(out, "empty"))

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})

	t.Run("drive delete trashes the empty local directory", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(out, "empty")))

		opts := applyOpts(t)
		require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))

		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionTrashedLocal, "empty", ""}}, report.Actions)

		require.NoDirExists(t, filepath.Join(src, "empty"))
		require.DirExists(t, filepath.Join(opts.TrashDir, "empty"))

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, second.Actions)
	})
}

// A journaled directory holding content is not a deletion candidate when the
// pass scans it: only its children are. Its anchor must survive that pass so
// the next one sees an empty survivor and finishes the deletion, instead of
// routing the directory to creation and resurrecting it.
func TestReconcilePropagatesDirectoryDeletionAcrossPasses(t *testing.T) {
	t.Run("drive deleted the directory", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.RemoveAll(filepath.Join(out, "sub")))

		opts := applyOpts(t)
		require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))

		first, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionTrashedLocal, "sub/nested.txt", ""}}, first.Actions)
		require.NoFileExists(t, filepath.Join(src, "sub", "nested.txt"))
		j, ok := loadJournal(state)
		require.True(t, ok)
		require.Contains(t, j.Entries, "sub", "the directory anchor must survive until its deletion is taken")

		second, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionTrashedLocal, "sub", ""}}, second.Actions)
		require.NoDirExists(t, filepath.Join(src, "sub"))
		require.DirExists(t, filepath.Join(opts.TrashDir, "sub"))

		third, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Empty(t, third.Actions)
	})

	t.Run("local deleted the directory", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.RemoveAll(filepath.Join(src, "sub")))

		first, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionRemovedDrive, "sub/nested.txt", ""}}, first.Actions)
		require.NoFileExists(t, filepath.Join(out, "sub", "nested.txt"))
		j, ok := loadJournal(state)
		require.True(t, ok)
		require.Contains(t, j.Entries, "sub", "the directory anchor must survive until its deletion is taken")

		second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionRemovedDrive, "sub", ""}}, second.Actions)
		require.NoDirExists(t, filepath.Join(out, "sub"))

		third, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Empty(t, third.Actions)
	})
}

func TestReconcileNeverDeletesWithoutMatchingAnchor(t *testing.T) {
	t.Run("drive-only with a different inode is imported", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
		replaceFile(t, filepath.Join(out, "keep.txt"), "recreated")

		report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionImported, "keep.txt", ""}}, report.Actions)

		require.Equal(t, "recreated", read(t, filepath.Join(src, "keep.txt")), "the drive survivor must be imported, not deleted")
		assertLinked(t, filepath.Join(out, "keep.txt"), filepath.Join(src, "keep.txt"))
	})

	t.Run("backdated survivor is linked, not trashed", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
		backdate(t, filepath.Join(src, "keep.txt"))

		opts := applyOpts(t)
		require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))

		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionLinked, "keep.txt", ""}}, report.Actions)

		require.Equal(t, "keep", read(t, filepath.Join(src, "keep.txt")), "the survivor must stay put")
		assertLinked(t, filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt"))
		require.NoFileExists(t, filepath.Join(opts.TrashDir, "keep.txt"))
	})
}

func TestReconcileTrashCollisionDoesNotOverwrite(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "same.txt"), "second")
	saveSyncedState(t, src, out, state)
	require.NoError(t, os.Remove(filepath.Join(out, "same.txt")))

	opts := applyOpts(t)
	require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))
	write(t, filepath.Join(opts.TrashDir, "same.txt"), "first")

	report, err := Reconcile(baseConfig(t, src, out), state, opts)
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionTrashedLocal, "same.txt", ""}}, report.Actions)

	require.NoFileExists(t, filepath.Join(src, "same.txt"))
	require.Equal(t, "first", read(t, filepath.Join(opts.TrashDir, "same.txt")), "an existing trash entry must never be overwritten")
	require.Equal(t, "second", read(t, filepath.Join(opts.TrashDir, "same-1.txt")))

	second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Empty(t, second.Actions)
}

func TestReconcileDeletionFailsClosedWhenTrashIsImpossible(t *testing.T) {
	t.Run("missing trash directory", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
		stateBefore := read(t, state)

		opts := applyOpts(t) // its TrashDir does not exist yet
		_, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.Error(t, err, "a deletion that cannot be trashed must fail the pass")
		require.Equal(t, stateBefore, read(t, state), "a failed pass must not commit the journal")
		require.Equal(t, "keep", read(t, filepath.Join(src, "keep.txt")), "the survivor must stay put")

		// Once the Trash directory exists the same pass takes the deletion.
		require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))
		report, err := Reconcile(baseConfig(t, src, out), state, opts)
		require.NoError(t, err)
		require.Equal(t, []Action{{ActionTrashedLocal, "keep.txt", ""}}, report.Actions)
		require.NoFileExists(t, filepath.Join(src, "keep.txt"))
		require.Equal(t, "keep", read(t, filepath.Join(opts.TrashDir, "keep.txt")))
	})

	t.Run("rename failure", func(t *testing.T) {
		src, out, state := syncedPair(t)
		require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
		stateBefore := read(t, state)

		boom := errors.New("rename boom")
		cfg := baseConfig(t, src, out)
		cfg.renameFn = func(oldname, newname string) error { return boom }

		opts := applyOpts(t)
		require.NoError(t, os.MkdirAll(opts.TrashDir, 0o755))

		_, err := Reconcile(cfg, state, opts)
		require.ErrorIs(t, err, boom)
		require.Equal(t, stateBefore, read(t, state), "a failed move must not commit the journal")
		require.Equal(t, "keep", read(t, filepath.Join(src, "keep.txt")), "the survivor must stay put")
	})
}

// TestTrashDirResolvesTheDefault pins the point-of-use resolution: an empty
// TrashDir means the user's Trash, a set one is used as given.
func TestTrashDirResolvesTheDefault(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name string
		set  string
		want string
	}{
		{"empty means the user's Trash", "", filepath.Join(home, ".Trash")},
		{"a set directory is used as given", filepath.Join(home, "custom-trash"), filepath.Join(home, "custom-trash")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &watchRun{opts: WatchOptions{TrashDir: tt.set}}
			dir, err := run.trashDir()
			require.NoError(t, err)
			require.Equal(t, tt.want, dir)
		})
	}
}

func TestReconcileOneWayPropagatesLocalDeletesOnly(t *testing.T) {
	src, out, state := syncedPair(t)
	require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
	require.NoError(t, os.Remove(filepath.Join(out, "sub", "nested.txt")))

	opts := applyOpts(t)
	opts.OneWay = true
	report, err := Reconcile(baseConfig(t, src, out), state, opts)
	require.NoError(t, err)
	require.Equal(t, []Action{
		{ActionLinked, "sub/nested.txt", ""},
		{ActionRemovedDrive, "keep.txt", ""},
	}, report.Actions)

	require.NoFileExists(t, filepath.Join(src, "keep.txt"))
	require.NoFileExists(t, filepath.Join(out, "keep.txt"))
	assertLinked(t, filepath.Join(src, "sub", "nested.txt"), filepath.Join(out, "sub", "nested.txt"))
	require.NoFileExists(t, filepath.Join(opts.TrashDir, "keep.txt"), "one-way must remove the drive entry, not trash the local one")
	require.NoFileExists(t, filepath.Join(opts.TrashDir, "sub", "nested.txt"), "one-way must re-link the survivor, not trash it")

	second, err := Reconcile(baseConfig(t, src, out), state, opts)
	require.NoError(t, err)
	require.Empty(t, second.Actions)
}

func TestReconcileDropsEntriesGoneOnBothSides(t *testing.T) {
	src, out, state := syncedPair(t)
	require.NoError(t, os.Remove(filepath.Join(src, "keep.txt")))
	require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
	write(t, filepath.Join(src, "new.txt"), "new")

	report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionLinked, "new.txt", ""}}, report.Actions)

	j, ok := loadJournal(state)
	require.True(t, ok)
	require.NotContains(t, j.Entries, "keep.txt", "an entry gone on both sides must not linger")
	require.Contains(t, j.Entries, "new.txt")
	require.Contains(t, j.Entries, filepath.Join("sub", "nested.txt"), "entries under pruned subtrees carry over")
}

func TestReconcileDeferredPassCommitsNothing(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "fresh.txt"), "still being written")
	stateBefore := read(t, state)

	opts := applyOpts(t)
	opts.MinAge = time.Hour
	report, err := Reconcile(baseConfig(t, src, out), state, opts)
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionDeferred, "fresh.txt", ""}}, report.Actions)
	require.Equal(t, stateBefore, read(t, state), "a deferred pass must leave the journal byte-identical")
	require.NoFileExists(t, filepath.Join(out, "fresh.txt"))

	// Once the write settles, the next pass links the file and commits it.
	backdate(t, filepath.Join(src, "fresh.txt"))
	report, err = Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionLinked, "fresh.txt", ""}}, report.Actions)
	assertLinked(t, filepath.Join(src, "fresh.txt"), filepath.Join(out, "fresh.txt"))

	third, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Empty(t, third.Actions)
}

// A path replaced on both sides has no anchor, so neither side may be assumed
// current. The newest mtime wins at the original path and the losing content
// survives as a conflict copy on both sides, hardlinked across.
func TestReconcileKeepsBothSidesWhenNeitherAnchorMatches(t *testing.T) {
	tests := []struct {
		name       string
		winner     string // the side with the newest mtime
		loser      string // the side preserved in the conflict copy
		winnerText string
		loserText  string
	}{
		{"drive newest wins", "drive", "local", "drive edit", "local edit"},
		{"local newest wins", "local", "drive", "local edit", "drive edit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, out, state := syncedPair(t)
			localPath, outPath := filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt")
			replaceFile(t, localPath, "local edit")
			replaceFile(t, outPath, "drive edit")
			localAt, driveAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
			if tt.winner == "local" {
				localAt, driveAt = driveAt, localAt
			}
			setMtime(t, localPath, localAt)
			setMtime(t, outPath, driveAt)

			report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
			require.NoError(t, err)
			require.Equal(t, []Action{{ActionConflict, "keep.txt", tt.winner + " wins"}}, report.Actions)

			copy := "keep.txt.sync-conflict-" + tt.loser + "-20200102T030405"
			require.Equal(t, tt.winnerText, read(t, localPath), "the newest content must win at the path")
			require.Equal(t, tt.winnerText, read(t, outPath))
			assertLinked(t, outPath, localPath)
			require.Equal(t, tt.loserText, read(t, filepath.Join(src, copy)), "the losing content must be preserved locally")
			require.Equal(t, tt.loserText, read(t, filepath.Join(out, copy)), "the losing content must be preserved on the drive side")
			assertLinked(t, filepath.Join(src, copy), filepath.Join(out, copy))
			assertNoTempEntries(t, src)
			assertNoTempEntries(t, out)

			second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
			require.NoError(t, err)
			require.Empty(t, second.Actions, "a resolved conflict must not repeat")
		})
	}
}

// The conflict copy name must never overwrite an existing entry: when the
// natural name is taken on either side, the copy lands under the next free
// suffix after the timestamp.
func TestReconcileConflictCopyNameCollisionGetsSuffix(t *testing.T) {
	src, out, state := syncedPair(t)
	const taken = "keep.txt.sync-conflict-local-20200102T030405"
	write(t, filepath.Join(src, taken), "already here")
	saveSyncedState(t, src, out, state)

	localPath, outPath := filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt")
	replaceFile(t, localPath, "local edit")
	replaceFile(t, outPath, "drive edit")
	setMtime(t, localPath, time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC))
	setMtime(t, outPath, time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC))

	report, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionConflict, "keep.txt", "drive wins"}}, report.Actions)

	require.Equal(t, "already here", read(t, filepath.Join(src, taken)), "an existing entry must never be overwritten")
	require.Equal(t, "already here", read(t, filepath.Join(out, taken)))
	const copy = taken + "-1"
	require.Equal(t, "local edit", read(t, filepath.Join(src, copy)))
	require.Equal(t, "local edit", read(t, filepath.Join(out, copy)))
	assertLinked(t, filepath.Join(src, copy), filepath.Join(out, copy))

	second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Empty(t, second.Actions)
}

// Review Focus 2: a file still being written is deferred without mutation and
// without a journal write; once its mtime settles into the past the next pass
// converges.
func TestReconcileDefersFreshlyModifiedEntries(t *testing.T) {
	src, out, state := syncedPair(t)
	write(t, filepath.Join(src, "fresh.txt"), "still being written")
	stateBefore := read(t, state)
	srcBefore := treeSnapshot(t, src)

	opts := applyOpts(t)
	opts.MinAge = time.Hour
	report, err := Reconcile(baseConfig(t, src, out), state, opts)
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionDeferred, "fresh.txt", ""}}, report.Actions)
	require.Equal(t, stateBefore, read(t, state), "a deferred pass must leave the journal byte-identical")
	require.Equal(t, srcBefore, treeSnapshot(t, src), "a deferred pass must not touch the source tree")
	require.NoFileExists(t, filepath.Join(out, "fresh.txt"))

	backdate(t, filepath.Join(src, "fresh.txt"))
	report, err = Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionLinked, "fresh.txt", ""}}, report.Actions)
	assertLinked(t, filepath.Join(src, "fresh.txt"), filepath.Join(out, "fresh.txt"))

	third, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Empty(t, third.Actions)
}

// A file facing a directory is never resolved automatically: both sides stay
// untouched and the same report returns until a human settles it.
func TestReconcileLeavesTypeMismatchAloneAndRepeatsIt(t *testing.T) {
	src, out, state := syncedPair(t)
	require.NoError(t, os.Remove(filepath.Join(out, "keep.txt")))
	require.NoError(t, os.MkdirAll(filepath.Join(out, "keep.txt"), 0o755))
	stateBefore := read(t, state)
	srcBefore := treeSnapshot(t, src)
	outBefore := treeSnapshot(t, out)

	first, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, []Action{{ActionTypeConflict, "keep.txt", ""}}, first.Actions)
	require.Equal(t, stateBefore, read(t, state), "a type conflict must not commit the journal")
	require.Equal(t, srcBefore, treeSnapshot(t, src), "a type conflict must not touch the source tree")
	require.Equal(t, outBefore, treeSnapshot(t, out), "a type conflict must not touch the drive tree")

	second, err := Reconcile(baseConfig(t, src, out), state, applyOpts(t))
	require.NoError(t, err)
	require.Equal(t, first.Actions, second.Actions, "a type conflict must repeat until resolved by hand")
}

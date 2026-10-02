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
			},
			want: []Action{{ActionConflict, "keep.txt", ""}},
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
}

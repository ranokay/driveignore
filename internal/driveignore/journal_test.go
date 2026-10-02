package driveignore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// watchStatePathIn names a state file for the pair the way WatchStatePath
// does, but inside dir so tests never touch the user's real config directory.
func watchStatePathIn(t *testing.T, dir, input, output string) string {
	t.Helper()
	hash, err := WatchPairHash(input, output)
	require.NoError(t, err)
	return filepath.Join(dir, "watch-"+hash+".json")
}

func TestJournalRoundTripAndAtomicWrite(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "nested")
	path := watchStatePathIn(t, stateDir, src, out)

	saved := &journal{
		Version: journalVersion,
		Input:   src,
		Output:  out,
		Entries: map[string]journalEntry{
			"keep.txt":                  {Type: "file", Inode: 42, Size: 7, ModTime: 1_700_000_000_000_000_000},
			filepath.Join("sub", "dir"): {Type: "dir", LocalStamp: 11, OutStamp: 22},
		},
	}
	require.NoError(t, saveJournal(path, saved))

	loaded, ok := loadJournal(path)
	require.True(t, ok)
	require.Equal(t, saved, loaded)

	assertNoTempEntries(t, filepath.Dir(path))
}

func TestJournalMissingCorruptAndMismatchedAreUnusable(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	stateDir := t.TempDir()
	path := watchStatePathIn(t, stateDir, src, out)

	assertUnusable := func(t *testing.T, path string) {
		t.Helper()
		j, ok := loadJournal(path)
		require.False(t, ok)
		require.NotNil(t, j)
		require.Empty(t, j.Entries)
	}

	t.Run("missing", func(t *testing.T) {
		assertUnusable(t, filepath.Join(stateDir, "absent.json"))
	})

	t.Run("corrupt", func(t *testing.T) {
		write(t, path, "{not json")
		assertUnusable(t, path)
	})

	t.Run("wrong version", func(t *testing.T) {
		require.NoError(t, saveJournal(path, &journal{
			Version: journalVersion + 98,
			Input:   src,
			Output:  out,
			Entries: map[string]journalEntry{"keep.txt": {Type: "file", Inode: 42}},
		}))
		assertUnusable(t, path)
	})

	t.Run("different pair", func(t *testing.T) {
		otherIn, otherOut := t.TempDir(), t.TempDir()
		require.NoError(t, saveJournal(path, &journal{
			Version: journalVersion,
			Input:   otherIn,
			Output:  otherOut,
			Entries: map[string]journalEntry{"keep.txt": {Type: "file", Inode: 42}},
		}))
		assertUnusable(t, path)
	})
}

func TestWatchPathsAreStablePerPair(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()

	hash, err := WatchPairHash(src, out)
	require.NoError(t, err)
	require.Len(t, hash, 12)

	state, err := WatchStatePath(src, out)
	require.NoError(t, err)
	lock, err := WatchLockPath(src, out)
	require.NoError(t, err)

	configDir, err := os.UserConfigDir()
	require.NoError(t, err)
	wantDir := filepath.Join(configDir, "driveignore")
	require.Equal(t, filepath.Join(wantDir, "watch-"+hash+".json"), state)
	require.Equal(t, filepath.Join(wantDir, "watch-"+hash+".lock"), lock)

	again, err := WatchStatePath(src, out)
	require.NoError(t, err)
	require.Equal(t, state, again)
	againLock, err := WatchLockPath(src, out)
	require.NoError(t, err)
	require.Equal(t, lock, againLock)

	other := t.TempDir()
	otherInput, err := WatchStatePath(other, out)
	require.NoError(t, err)
	require.NotEqual(t, state, otherInput)
	otherOutput, err := WatchStatePath(src, other)
	require.NoError(t, err)
	require.NotEqual(t, state, otherOutput)
}

// The same directory spelled through a symlink and through its resolved path
// must key one journal and one lock: a watcher installed with one spelling has
// to exclude an upload, unify or clean run using the other.
func TestWatchPathsCanonicalizeSymlinkedRoots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	src, out := t.TempDir(), t.TempDir()
	resolvedSrc, err := filepath.EvalSymlinks(src)
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "src-link")
	require.NoError(t, os.Symlink(resolvedSrc, link))

	linkedState, err := WatchStatePath(link, out)
	require.NoError(t, err)
	resolvedState, err := WatchStatePath(resolvedSrc, out)
	require.NoError(t, err)
	require.Equal(t, resolvedState, linkedState, "both spellings must share one journal")

	linkedLock, err := WatchLockPath(link, out)
	require.NoError(t, err)
	resolvedLock, err := WatchLockPath(resolvedSrc, out)
	require.NoError(t, err)
	require.Equal(t, resolvedLock, linkedLock, "both spellings must share one lock")
}

// A pair whose directories no longer exist still hashes: --uninstall must
// derive the pair key after the trees are gone.
func TestWatchPairHashToleratesMissingRoots(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	alsoGone := filepath.Join(t.TempDir(), "also gone")
	hash, err := WatchPairHash(gone, alsoGone)
	require.NoError(t, err)
	require.Len(t, hash, 12)
}

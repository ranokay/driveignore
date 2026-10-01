package driveignore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func missingGlobal(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "global", ".global_driveignore")
}

func assertLinked(t *testing.T, source, output string) {
	t.Helper()
	srcInfo, err := os.Stat(source)
	require.NoError(t, err)
	outInfo, err := os.Stat(output)
	require.NoError(t, err)
	require.True(t, os.SameFile(srcInfo, outInfo), "%s and %s are not hardlinked", source, output)
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.ErrorIs(t, err, fs.ErrNotExist, "%s should not exist", path)
}

func TestWalkVisitsEntriesInOrderWithoutSpecialPaths(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "b.txt"), "b")
	write(t, filepath.Join(root, "sub", "a.txt"), "a")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "empty"), 0o755))

	var got []string
	require.NoError(t, Walk(root, func(_ string, _ fs.DirEntry, rel string) error {
		got = append(got, rel)
		return nil
	}))

	require.Equal(t, []string{"b.txt", "empty", "sub", filepath.Join("sub", "a.txt")}, got)
	for _, rel := range got {
		require.False(t, strings.HasSuffix(rel, string(os.PathSeparator)), "relative path %q has a trailing separator", rel)
	}
}

func TestWalkPropagatesRootErrors(t *testing.T) {
	err := Walk(filepath.Join(t.TempDir(), "missing"), func(string, fs.DirEntry, string) error {
		return nil
	})
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestUploadHardlinksFilesAndCreatesEmptyDirs(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "ignored.txt\nignored-dir\n")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(src, "ignored.txt"), "ignored")
	write(t, filepath.Join(src, "sub", "nested.txt"), "nested")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "emptydir"), 0o755))
	write(t, filepath.Join(src, "ignored-dir", "x.txt"), "x")

	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))

	assertLinked(t, filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt"))
	assertLinked(t, filepath.Join(src, "sub", "nested.txt"), filepath.Join(out, "sub", "nested.txt"))
	info, err := os.Stat(filepath.Join(out, "emptydir"))
	require.NoError(t, err, "empty directories must be uploaded")
	require.True(t, info.IsDir())
	assertNotExist(t, filepath.Join(out, "ignored.txt"))
	assertNotExist(t, filepath.Join(out, "ignored-dir"))
}

func TestUploadWithoutAnyDriveignoreFails(t *testing.T) {
	err := Upload(Options{Input: t.TempDir(), Output: t.TempDir(), GlobalIgnorePath: missingGlobal(t)})
	require.Error(t, err)
}

func TestUploadConflictKeepsExistingFileUnlessForced(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "same.txt"), "new")
	write(t, filepath.Join(out, "same.txt"), "old")

	var notices bytes.Buffer
	opts := Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t), Out: &notices}
	require.NoError(t, Upload(opts))
	require.Equal(t, "old", read(t, filepath.Join(out, "same.txt")))
	require.Contains(t, notices.String(), "same.txt")

	opts.Force = true
	require.NoError(t, Upload(opts))
	assertLinked(t, filepath.Join(src, "same.txt"), filepath.Join(out, "same.txt"))
}

func TestUploadHandlesUnicodeAndSpaceFilenames(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "ünïcode", "spa ce.txt"), "x")

	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))
	assertLinked(t, filepath.Join(src, "ünïcode", "spa ce.txt"), filepath.Join(out, "ünïcode", "spa ce.txt"))
}

func TestCleanRemovesFilesThatAreMissingFromSource(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))
	write(t, filepath.Join(out, "legacy.txt"), "legacy")
	write(t, filepath.Join(out, "sub", "legacy.txt"), "legacy")

	removed, err := Clean(Options{Input: src, Output: out})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"legacy.txt", filepath.ToSlash(filepath.Join("sub", "legacy.txt"))}, removed)
	assertLinked(t, filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt"))
	assertNotExist(t, filepath.Join(out, "legacy.txt"))
	assertNotExist(t, filepath.Join(out, "sub", "legacy.txt"))
}

func TestCleanAbortsInsteadOfDeletingWhenSourceStatFails(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(out, "keep.txt"), "not a link")
	write(t, filepath.Join(out, "sub", "other.txt"), "other")

	denied := errors.New("permission denied")
	_, err := Clean(Options{Input: src, Output: out, statFn: func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, filepath.Join("sub", "other.txt")) {
			return nil, denied
		}
		return os.Stat(path)
	}})
	require.ErrorIs(t, err, denied)
	_, statErr := os.Stat(filepath.Join(out, "sub", "other.txt"))
	require.NoError(t, statErr, "a stat error must never lead to deletion")
}

func TestDiffFindsMissingAndLegacyEntries(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))
	write(t, filepath.Join(src, "only-src.txt"), "s")
	write(t, filepath.Join(out, "only-out.txt"), "o")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "newdir"), 0o755))

	res, err := Diff(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"only-src.txt", "newdir"}, res.Missing)
	require.ElementsMatch(t, []string{"only-out.txt"}, res.Old)
}

func TestUnifyUploadsAndCleansInOnePass(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "ignored.txt\n")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(src, "ignored.txt"), "ignored")
	write(t, filepath.Join(out, "keep.txt"), "stale")
	write(t, filepath.Join(out, "legacy.txt"), "legacy")

	require.NoError(t, Unify(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))

	assertLinked(t, filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt"))
	assertNotExist(t, filepath.Join(out, "legacy.txt"))
	assertNotExist(t, filepath.Join(out, "ignored.txt"))
}

func TestLoadIgnoreSelectsLocalGlobalAndMerged(t *testing.T) {
	local := t.TempDir()
	write(t, filepath.Join(local, ".driveignore"), "local-only.txt\n")
	global := filepath.Join(t.TempDir(), ".global_driveignore")
	write(t, global, "global-only.txt\n")

	t.Run("local only", func(t *testing.T) {
		matcher, typ, err := LoadIgnore(global, local, false)
		require.NoError(t, err)
		require.Equal(t, LocalIgnore, typ)
		require.True(t, matcher.Match(filepath.Join(local, "local-only.txt"), false))
		require.False(t, matcher.Match(filepath.Join(local, "global-only.txt"), false))
	})

	t.Run("global only", func(t *testing.T) {
		other := t.TempDir()
		matcher, typ, err := LoadIgnore(global, other, false)
		require.NoError(t, err)
		require.Equal(t, GlobalIgnore, typ)
		require.True(t, matcher.Match(filepath.Join(other, "global-only.txt"), false))
		require.False(t, matcher.Match(filepath.Join(other, "local-only.txt"), false))
	})

	t.Run("merged", func(t *testing.T) {
		matcher, typ, err := LoadIgnore(global, local, true)
		require.NoError(t, err)
		require.Equal(t, MergedIgnore, typ)
		require.True(t, matcher.Match(filepath.Join(local, "local-only.txt"), false))
		require.True(t, matcher.Match(filepath.Join(local, "global-only.txt"), false))
	})

	t.Run("none", func(t *testing.T) {
		matcher, typ, err := LoadIgnore(missingGlobal(t), t.TempDir(), false)
		require.NoError(t, err)
		require.Equal(t, NoIgnore, typ)
		require.Nil(t, matcher)
	})
}

func TestUploadAndCleanLeaveSymlinksAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "real.txt"), "real")
	require.NoError(t, os.Symlink(filepath.Join(src, "real.txt"), filepath.Join(src, "link.txt")))

	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))
	assertNotExist(t, filepath.Join(out, "link.txt"))
	assertLinked(t, filepath.Join(src, "real.txt"), filepath.Join(out, "real.txt"))

	// Symlinks already inside the drive folder are left alone by clean.
	require.NoError(t, os.Symlink(filepath.Join(out, "real.txt"), filepath.Join(out, "stray-link.txt")))
	removed, err := Clean(Options{Input: src, Output: out})
	require.NoError(t, err)
	require.Empty(t, removed)
	_, err = os.Lstat(filepath.Join(out, "stray-link.txt"))
	require.NoError(t, err, "clean must not remove symlinks from the drive folder")
}

func TestCleanDryRunReportsWithoutRemoving(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(out, "legacy.txt"), "legacy")

	removed, err := Clean(Options{Input: src, Output: out, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []string{"legacy.txt"}, removed)
	require.Equal(t, "legacy", read(t, filepath.Join(out, "legacy.txt")), "dry-run must not remove anything")
}

func TestCleanPruneIgnoredRemovesFilesExcludedByDriveignore(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "ignored.txt\n")
	write(t, filepath.Join(src, "ignored.txt"), "ignored")
	require.NoError(t, os.Link(filepath.Join(src, "ignored.txt"), filepath.Join(out, "ignored.txt")))

	// Without pruning the linked drive copy stays: its source still exists.
	removed, err := Clean(Options{Input: src, Output: out})
	require.NoError(t, err)
	require.Empty(t, removed)
	require.FileExists(t, filepath.Join(out, "ignored.txt"))

	removed, err = Clean(Options{Input: src, Output: out, PruneIgnored: true, GlobalIgnorePath: missingGlobal(t)})
	require.NoError(t, err)
	require.Equal(t, []string{"ignored.txt"}, removed)
	assertNotExist(t, filepath.Join(out, "ignored.txt"))
}

func TestUploadFollowsSymlinkedInputRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	realSrc := t.TempDir()
	write(t, filepath.Join(realSrc, ".driveignore"), "")
	write(t, filepath.Join(realSrc, "keep.txt"), "keep")
	src := filepath.Join(t.TempDir(), "src")
	require.NoError(t, os.Symlink(realSrc, src))

	out := t.TempDir()
	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t)}))
	assertLinked(t, filepath.Join(realSrc, "keep.txt"), filepath.Join(out, "keep.txt"))
}

func TestCleanFollowsSymlinkedDriveRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	src := t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "keep")
	realOut := t.TempDir()
	write(t, filepath.Join(realOut, "legacy.txt"), "legacy")
	out := filepath.Join(t.TempDir(), "out")
	require.NoError(t, os.Symlink(realOut, out))

	removed, err := Clean(Options{Input: src, Output: out})
	require.NoError(t, err)
	require.Equal(t, []string{"legacy.txt"}, removed)
	assertNotExist(t, filepath.Join(realOut, "legacy.txt"))
}

func TestUploadForceKeepsExistingFileWhenLinkFails(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "same.txt"), "new")
	write(t, filepath.Join(out, "same.txt"), "old")

	boom := errors.New("link boom")
	err := Upload(Options{
		Input: src, Output: out, GlobalIgnorePath: missingGlobal(t), Force: true,
		linkFn: func(string, string) error { return boom },
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, "old", read(t, filepath.Join(out, "same.txt")), "a failed link must not destroy the existing file")

	entries, err := os.ReadDir(out)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".driveignore-", "temporary links must be cleaned up")
	}
}

func TestUploadForceReplacesDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "foo.txt"), "new")
	require.NoError(t, os.Symlink("missing-target", filepath.Join(out, "foo.txt")))

	var notices bytes.Buffer
	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t), Out: &notices}))
	require.Contains(t, notices.String(), "foo.txt")
	info, err := os.Lstat(filepath.Join(out, "foo.txt"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)

	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t), Force: true}))
	assertLinked(t, filepath.Join(src, "foo.txt"), filepath.Join(out, "foo.txt"))
}

func TestUploadForceReplacesFileWithDirectory(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "d"), 0o755))
	write(t, filepath.Join(out, "d"), "file")

	var notices bytes.Buffer
	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t), Out: &notices}))
	require.Contains(t, notices.String(), "d")
	info, err := os.Stat(filepath.Join(out, "d"))
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "without --force the file must stay")

	require.NoError(t, Upload(Options{Input: src, Output: out, GlobalIgnorePath: missingGlobal(t), Force: true}))
	info, err = os.Stat(filepath.Join(out, "d"))
	require.NoError(t, err)
	require.True(t, info.IsDir(), "with --force the directory must replace the file")
}

func TestEnsureFileCreatesMissingFileAndKeepsExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", ".global_driveignore")
	require.NoError(t, EnsureFile(path))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	if runtime.GOOS != "windows" {
		require.Equal(t, fs.FileMode(0o644), info.Mode().Perm())
	}

	require.NoError(t, os.WriteFile(path, []byte("keep\n"), 0o644))
	require.NoError(t, EnsureFile(path))
	require.Equal(t, "keep\n", read(t, path))
}

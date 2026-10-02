package driveignore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func globalFn(path string) func() (string, error) {
	return func() (string, error) { return path, nil }
}

func baseConfig(t *testing.T, src, out string) Config {
	t.Helper()
	return Config{Input: src, Output: out, globalPathFn: globalFn(missingGlobal(t))}
}

func runUpload(t *testing.T, cfg Config, opts UploadOptions) UploadResult {
	t.Helper()
	res, err := Upload(cfg, opts)
	require.NoError(t, err)
	return res
}

func runUnify(t *testing.T, cfg Config, opts UnifyOptions) UploadResult {
	t.Helper()
	res, err := Unify(cfg, opts)
	require.NoError(t, err)
	return res
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

func TestWalkHandlesSpacesAndUnicode(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "spa ce", "ünïcode.txt"), "x")

	var got []string
	require.NoError(t, Walk(root, func(_ string, _ fs.DirEntry, rel string) error {
		got = append(got, rel)
		return nil
	}))
	require.Equal(t, []string{filepath.Join("spa ce"), filepath.Join("spa ce", "ünïcode.txt")}, got)
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

	runUpload(t, baseConfig(t, src, out), UploadOptions{})

	assertLinked(t, filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt"))
	assertLinked(t, filepath.Join(src, "sub", "nested.txt"), filepath.Join(out, "sub", "nested.txt"))
	info, err := os.Stat(filepath.Join(out, "emptydir"))
	require.NoError(t, err, "empty directories must be uploaded")
	require.True(t, info.IsDir())
	assertNotExist(t, filepath.Join(out, "ignored.txt"))
	assertNotExist(t, filepath.Join(out, "ignored-dir"))
}

func TestUploadWithoutAnyDriveignoreFails(t *testing.T) {
	cfg := baseConfig(t, t.TempDir(), t.TempDir())
	_, err := Upload(cfg, UploadOptions{})
	require.ErrorContains(t, err, "no .driveignore found")

	globalPath, pathErr := cfg.globalPath()
	require.NoError(t, pathErr)
	require.ErrorContains(t, err, globalPath)
}

func TestUploadConflictKeepsExistingFileUnlessForced(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "same.txt"), "new")
	write(t, filepath.Join(out, "same.txt"), "old")

	cfg := baseConfig(t, src, out)
	res := runUpload(t, cfg, UploadOptions{})
	require.Equal(t, "old", read(t, filepath.Join(out, "same.txt")))
	require.Contains(t, res.Conflicts, "same.txt")

	runUpload(t, cfg, UploadOptions{Force: true})
	assertLinked(t, filepath.Join(src, "same.txt"), filepath.Join(out, "same.txt"))
}

func TestUploadHandlesUnicodeAndSpaceFilenames(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "ünïcode", "spa ce.txt"), "x")

	runUpload(t, baseConfig(t, src, out), UploadOptions{})
	assertLinked(t, filepath.Join(src, "ünïcode", "spa ce.txt"), filepath.Join(out, "ünïcode", "spa ce.txt"))
}

func TestCleanRemovesFilesThatAreMissingFromSource(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	cfg := baseConfig(t, src, out)
	runUpload(t, cfg, UploadOptions{})
	write(t, filepath.Join(out, "legacy.txt"), "legacy")
	write(t, filepath.Join(out, "sub", "legacy.txt"), "legacy")

	removed, err := Clean(cfg, CleanOptions{})
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

	cfg := baseConfig(t, src, out)
	denied := errors.New("permission denied")
	cfg.statFn = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, filepath.Join("sub", "other.txt")) {
			return nil, denied
		}
		return os.Stat(path)
	}
	_, err := Clean(cfg, CleanOptions{})
	require.ErrorIs(t, err, denied)
	_, statErr := os.Stat(filepath.Join(out, "sub", "other.txt"))
	require.NoError(t, statErr, "a stat error must never lead to deletion")
}

func TestDiffFindsMissingAndLegacyEntries(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	cfg := baseConfig(t, src, out)
	runUpload(t, cfg, UploadOptions{})
	write(t, filepath.Join(src, "only-src.txt"), "s")
	write(t, filepath.Join(out, "only-out.txt"), "o")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "newdir"), 0o755))

	res, err := Diff(cfg)
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

	runUnify(t, baseConfig(t, src, out), UnifyOptions{})

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
		matcher, _, err := LoadIgnore(local, false, globalFn(global))
		require.NoError(t, err)
		require.True(t, matcher.Match(filepath.Join(local, "local-only.txt"), false))
		require.False(t, matcher.Match(filepath.Join(local, "global-only.txt"), false))
	})

	t.Run("global only", func(t *testing.T) {
		other := t.TempDir()
		matcher, _, err := LoadIgnore(other, false, globalFn(global))
		require.NoError(t, err)
		require.True(t, matcher.Match(filepath.Join(other, "global-only.txt"), false))
		require.False(t, matcher.Match(filepath.Join(other, "local-only.txt"), false))
	})

	t.Run("merged", func(t *testing.T) {
		matcher, _, err := LoadIgnore(local, true, globalFn(global))
		require.NoError(t, err)
		require.True(t, matcher.Match(filepath.Join(local, "local-only.txt"), false))
		require.True(t, matcher.Match(filepath.Join(local, "global-only.txt"), false))
	})

	t.Run("none", func(t *testing.T) {
		matcher, _, err := LoadIgnore(t.TempDir(), false, globalFn(missingGlobal(t)))
		require.NoError(t, err)
		require.Nil(t, matcher)
	})
}

func TestLoadIgnoreResolvesGlobalPathOnlyWhenNeeded(t *testing.T) {
	local := t.TempDir()
	write(t, filepath.Join(local, ".driveignore"), "local-only.txt\n")
	boom := errors.New("no user config dir")

	// A local root file with merge off makes the global path irrelevant.
	matcher, _, err := LoadIgnore(local, false, func() (string, error) { return "", boom })
	require.NoError(t, err)
	require.True(t, matcher.Match(filepath.Join(local, "local-only.txt"), false))

	// Merging makes it relevant.
	_, _, err = LoadIgnore(local, true, func() (string, error) { return "", boom })
	require.ErrorIs(t, err, boom)
}

func TestUploadAndCleanLeaveSymlinksAlone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires privileges on Windows")
	}
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "real.txt"), "real")
	require.NoError(t, os.Symlink(filepath.Join(src, "real.txt"), filepath.Join(src, "link.txt")))

	cfg := baseConfig(t, src, out)
	runUpload(t, cfg, UploadOptions{})
	assertNotExist(t, filepath.Join(out, "link.txt"))
	assertLinked(t, filepath.Join(src, "real.txt"), filepath.Join(out, "real.txt"))

	// Symlinks already inside the drive folder are left alone by clean.
	require.NoError(t, os.Symlink(filepath.Join(out, "real.txt"), filepath.Join(out, "stray-link.txt")))
	removed, err := Clean(cfg, CleanOptions{})
	require.NoError(t, err)
	require.Empty(t, removed)
	_, err = os.Lstat(filepath.Join(out, "stray-link.txt"))
	require.NoError(t, err, "clean must not remove symlinks from the drive folder")
}

func TestCleanDryRunReportsWithoutRemoving(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "keep")
	write(t, filepath.Join(out, "legacy.txt"), "legacy")

	removed, err := Clean(baseConfig(t, src, out), CleanOptions{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []string{"legacy.txt"}, removed)
	require.Equal(t, "legacy", read(t, filepath.Join(out, "legacy.txt")), "dry-run must not remove anything")
}

func TestCleanPruneIgnoredRemovesFilesExcludedByDriveignore(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "ignored.txt\n")
	write(t, filepath.Join(src, "ignored.txt"), "ignored")
	require.NoError(t, os.Link(filepath.Join(src, "ignored.txt"), filepath.Join(out, "ignored.txt")))

	cfg := baseConfig(t, src, out)

	// Without pruning the linked drive copy stays: its source still exists.
	removed, err := Clean(cfg, CleanOptions{})
	require.NoError(t, err)
	require.Empty(t, removed)
	require.FileExists(t, filepath.Join(out, "ignored.txt"))

	removed, err = Clean(cfg, CleanOptions{PruneIgnored: true})
	require.NoError(t, err)
	require.Equal(t, []string{"ignored.txt"}, removed)
	assertNotExist(t, filepath.Join(out, "ignored.txt"))
}

func TestCleanPruneIgnoredWithoutAnyIgnoreFileProceeds(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, "keep.txt"), "keep")
	require.NoError(t, os.Link(filepath.Join(src, "keep.txt"), filepath.Join(out, "keep.txt")))

	removed, err := Clean(baseConfig(t, src, out), CleanOptions{PruneIgnored: true})
	require.NoError(t, err)
	require.Empty(t, removed)
	require.FileExists(t, filepath.Join(out, "keep.txt"))
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
	runUpload(t, baseConfig(t, src, out), UploadOptions{})
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

	removed, err := Clean(baseConfig(t, src, out), CleanOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"legacy.txt"}, removed)
	assertNotExist(t, filepath.Join(realOut, "legacy.txt"))
}

func TestUploadForceKeepsExistingFileWhenLinkFails(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "same.txt"), "new")
	write(t, filepath.Join(out, "same.txt"), "old")

	cfg := baseConfig(t, src, out)
	boom := errors.New("link boom")
	cfg.linkFn = func(string, string) error { return boom }
	_, err := Upload(cfg, UploadOptions{Force: true})
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

	cfg := baseConfig(t, src, out)
	res := runUpload(t, cfg, UploadOptions{})
	require.Contains(t, res.Conflicts, "foo.txt")
	info, err := os.Lstat(filepath.Join(out, "foo.txt"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)

	runUpload(t, cfg, UploadOptions{Force: true})
	assertLinked(t, filepath.Join(src, "foo.txt"), filepath.Join(out, "foo.txt"))
}

func TestUploadForceReplacesFileWithDirectory(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	require.NoError(t, os.MkdirAll(filepath.Join(src, "d"), 0o755))
	write(t, filepath.Join(out, "d"), "file")

	cfg := baseConfig(t, src, out)
	res := runUpload(t, cfg, UploadOptions{})
	require.Contains(t, res.Conflicts, "d")
	info, err := os.Stat(filepath.Join(out, "d"))
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "without --force the file must stay")

	runUpload(t, cfg, UploadOptions{Force: true})
	info, err = os.Stat(filepath.Join(out, "d"))
	require.NoError(t, err)
	require.True(t, info.IsDir(), "with --force the directory must replace the file")
}

func TestUploadCopyCreatesIndependentCopies(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")

	runUpload(t, baseConfig(t, src, out), UploadOptions{Copy: true})

	outPath := filepath.Join(out, "keep.txt")
	srcInfo, err := os.Stat(filepath.Join(src, "keep.txt"))
	require.NoError(t, err)
	outInfo, err := os.Stat(outPath)
	require.NoError(t, err)
	require.False(t, os.SameFile(srcInfo, outInfo), "copy mode must not hardlink")
	require.Equal(t, "keep", read(t, outPath))
	require.True(t, srcInfo.ModTime().Equal(outInfo.ModTime()), "modification time is preserved for later comparisons")
	if runtime.GOOS != "windows" {
		require.Equal(t, srcInfo.Mode().Perm(), outInfo.Mode().Perm())
	}
}

func TestUploadCopyIsIdempotent(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	cfg := baseConfig(t, src, out)

	runUpload(t, cfg, UploadOptions{Copy: true})
	before, err := os.Stat(filepath.Join(out, "keep.txt"))
	require.NoError(t, err)
	runUpload(t, cfg, UploadOptions{Copy: true})
	after, err := os.Stat(filepath.Join(out, "keep.txt"))
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "an in-sync copy must not be rewritten")
}

func TestUnifyCopyReplacesChangedSource(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "one")
	cfg := baseConfig(t, src, out)

	runUnify(t, cfg, UnifyOptions{Copy: true})
	require.Equal(t, "one", read(t, filepath.Join(out, "keep.txt")))

	write(t, filepath.Join(src, "keep.txt"), "two-longer")
	runUnify(t, cfg, UnifyOptions{Copy: true})
	require.Equal(t, "two-longer", read(t, filepath.Join(out, "keep.txt")))
}

func TestUnifyCopyReplacesSameSizeEditImmediately(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "one")
	cfg := baseConfig(t, src, out)

	runUnify(t, cfg, UnifyOptions{Copy: true})
	// A same-size edit made within the timestamp tolerance must still be
	// detected (racy timestamps are resolved by comparing content).
	write(t, filepath.Join(src, "keep.txt"), "two")
	runUnify(t, cfg, UnifyOptions{Copy: true})
	require.Equal(t, "two", read(t, filepath.Join(out, "keep.txt")))
}

func TestCleanKeepsInSyncCopiesAndRemovesStaleOnes(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	cfg := baseConfig(t, src, out)
	runUpload(t, cfg, UploadOptions{Copy: true})

	removed, err := Clean(cfg, CleanOptions{})
	require.NoError(t, err)
	require.Empty(t, removed, "an in-sync copy must be kept")

	// Same size, changed content and a future timestamp: stale.
	write(t, filepath.Join(src, "keep.txt"), "KEEP")
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(src, "keep.txt"), future, future))
	removed, err = Clean(cfg, CleanOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"keep.txt"}, removed)
	assertNotExist(t, filepath.Join(out, "keep.txt"))
}

func TestDiffTreatsCopiesAsInSync(t *testing.T) {
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "keep.txt"), "keep")
	cfg := baseConfig(t, src, out)
	runUpload(t, cfg, UploadOptions{Copy: true})

	res, err := Diff(cfg)
	require.NoError(t, err)
	require.Empty(t, res.Missing)
	require.Empty(t, res.Old)
}

func TestUploadCopyFailureKeepsExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block reads on Windows")
	}
	src, out := t.TempDir(), t.TempDir()
	write(t, filepath.Join(src, ".driveignore"), "")
	write(t, filepath.Join(src, "same.txt"), "new-content")
	write(t, filepath.Join(out, "same.txt"), "old")
	require.NoError(t, os.Chmod(filepath.Join(src, "same.txt"), 0o000))

	_, err := Upload(baseConfig(t, src, out), UploadOptions{Copy: true, Force: true})
	require.Error(t, err)
	require.Equal(t, "old", read(t, filepath.Join(out, "same.txt")), "a failed copy must not destroy the existing file")
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

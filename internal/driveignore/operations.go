package driveignore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Config is the invocation context shared by every core operation. Input is
// the source directory; Output is the drive folder, except for Clean where
// Output is walked and Input is only used as the reference.
type Config struct {
	Input  string
	Output string
	// MergeIgnores merges the global and input dir .driveignore files.
	MergeIgnores bool

	// Log receives verbose diagnostics. nil disables them.
	Log func(format string, args ...any)

	statFn       func(string) (os.FileInfo, error)   // nil means os.Stat
	lstatFn      func(string) (os.FileInfo, error)   // nil means os.Lstat
	linkFn       func(oldname, newname string) error // nil means os.Link
	mkdirAllFn   func(string, os.FileMode) error     // nil means os.MkdirAll
	renameFn     func(oldname, newname string) error // nil means os.Rename
	copyFileFn   func(sourcePath, dst string) error  // nil means copyFile
	globalPathFn func() (string, error)              // nil means GlobalIgnorePath
}

// UploadOptions configures Upload. Force overwrites existing entries with the
// same relative path; Copy copies files instead of hardlinking them, for
// filesystems that do not support hardlinks (virtual drives, FAT, network
// shares); clean and diff then treat equal size and modification time as in
// sync.
type UploadOptions struct {
	Force bool
	Copy  bool
}

// UploadResult reports the conflicts Upload left alone.
type UploadResult struct {
	// Conflicts lists relative paths whose drive-side entry already exists.
	Conflicts []string
}

// CleanOptions configures Clean. DryRun reports removals without deleting
// anything; PruneIgnored removes drive files that the ignore rules exclude
// even when the source still contains them.
type CleanOptions struct {
	DryRun       bool
	PruneIgnored bool
}

// UnifyOptions configures Unify. PruneIgnored is forwarded to its clean half;
// Copy applies to the upload half.
type UnifyOptions struct {
	PruneIgnored bool
	Copy         bool
}

// DiffResult lists relative paths that exist on only one side.
type DiffResult struct {
	Missing []string // present in Input, missing from Output
	Old     []string // present in Output, missing from Input
}

func (c Config) stat(path string) (os.FileInfo, error) {
	if c.statFn != nil {
		return c.statFn(path)
	}
	return os.Stat(path)
}

func (c Config) link(oldname, newname string) error {
	if c.linkFn != nil {
		return c.linkFn(oldname, newname)
	}
	return os.Link(oldname, newname)
}

func (c Config) lstat(path string) (os.FileInfo, error) {
	if c.lstatFn != nil {
		return c.lstatFn(path)
	}
	return os.Lstat(path)
}

func (c Config) mkdirAll(path string, perm os.FileMode) error {
	if c.mkdirAllFn != nil {
		return c.mkdirAllFn(path, perm)
	}
	return os.MkdirAll(path, perm)
}

func (c Config) rename(oldname, newname string) error {
	if c.renameFn != nil {
		return c.renameFn(oldname, newname)
	}
	return os.Rename(oldname, newname)
}

func (c Config) copyFile(sourcePath, dst string) error {
	if c.copyFileFn != nil {
		return c.copyFileFn(sourcePath, dst)
	}
	return copyFile(sourcePath, dst)
}

func (c Config) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// globalPath resolves the global .driveignore location: the injected seam in
// tests, GlobalIgnorePath otherwise.
func (c Config) globalPath() (string, error) {
	if c.globalPathFn != nil {
		return c.globalPathFn()
	}
	return GlobalIgnorePath()
}

// rules loads whatever ignore rules apply to the invocation. A nil Matcher
// means no ignore file exists anywhere; the returned path is the resolved
// global location when it was needed.
func (c Config) rules() (Matcher, string, error) {
	return LoadIgnore(c.Input, c.MergeIgnores, c.globalPath)
}

// ignore returns the rules Upload and Diff require, or an error naming where
// a .driveignore was looked for.
func (c Config) ignore() (Matcher, error) {
	matcher, globalPath, err := c.rules()
	if err != nil {
		return nil, err
	}
	if matcher == nil {
		return nil, fmt.Errorf("no .driveignore found in %s or at %s", c.Input, globalPath)
	}
	return matcher, nil
}

// resolveRoot follows symlinks so a directory named through a link is walked
// instead of silently skipped.
func resolveRoot(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	return filepath.EvalSymlinks(path)
}

func resolveRoots(c Config) (Config, error) {
	input, err := resolveRoot(c.Input)
	if err != nil {
		return c, err
	}
	output, err := resolveRoot(c.Output)
	if err != nil {
		return c, err
	}
	c.Input, c.Output = input, output
	return c, nil
}

// Upload hardlinks the files of Input into Output, honoring the ignore rules.
// Directories are created, including empty ones. When an entry already exists
// under the same relative path it is reported in the UploadResult and skipped;
// with Force it is replaced by a hardlink or directory from the source.
func Upload(c Config, opts UploadOptions) (UploadResult, error) {
	c, err := resolveRoots(c)
	if err != nil {
		return UploadResult{}, err
	}
	matcher, err := c.ignore()
	if err != nil {
		return UploadResult{}, err
	}
	u := &uploader{Config: c, UploadOptions: opts}
	err = Walk(c.Input, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			u.logf("skipped symlink: %s", filepath.ToSlash(rel))
			return nil
		}
		if excluded, dir := skip(matcher, path, entry); excluded {
			if dir {
				u.logf("skipped directory: %s", filepath.ToSlash(rel))
				return filepath.SkipDir
			}
			u.logf("skipped file: %s", filepath.ToSlash(rel))
			return nil
		}
		return u.uploadEntry(path, filepath.Join(u.Output, rel), entry, rel)
	})
	if err != nil {
		// Conflicts found before the error are still reported.
		return UploadResult{Conflicts: u.conflicts}, err
	}
	return UploadResult{Conflicts: u.conflicts}, nil
}

// uploader carries the per-upload options and collected conflicts alongside
// the shared Config, so the implementation helpers keep a single receiver.
type uploader struct {
	Config
	UploadOptions
	conflicts []string
}

func (u *uploader) reportConflict(rel string) {
	u.conflicts = append(u.conflicts, rel)
}

// uploadEntry creates the drive-side entry for one source path, or reconciles
// it with an entry that is already there.
func (u *uploader) uploadEntry(sourcePath, goalPath string, entry fs.DirEntry, rel string) error {
	relSlash := filepath.ToSlash(rel)
	goalInfo, goalErr := u.lstat(goalPath)
	if goalErr != nil && !errors.Is(goalErr, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", goalPath, goalErr)
	}
	if errors.Is(goalErr, fs.ErrNotExist) {
		if entry.IsDir() {
			return u.createDirectory(goalPath, rel)
		}
		if err := u.mkdirAll(filepath.Dir(goalPath), 0o755); err != nil {
			return err
		}
		return u.installFile(sourcePath, goalPath, relSlash)
	}
	return u.reconcile(sourcePath, goalPath, entry, goalInfo, relSlash)
}

func (u *uploader) createDirectory(goalPath, rel string) error {
	if err := u.mkdirAll(goalPath, 0o755); err != nil {
		return err
	}
	u.logf("created directory: %s", filepath.ToSlash(rel))
	return nil
}

// reconcile handles an existing drive-side entry. Without Force, conflicts are
// reported and skipped. With Force the entry is replaced; a directory is never
// deleted to make room for a file, that conflict must be resolved by hand.
func (u *uploader) reconcile(sourcePath, goalPath string, entry fs.DirEntry, goalInfo os.FileInfo, rel string) error {
	if entry.IsDir() {
		if goalInfo.IsDir() {
			return nil
		}
		if !u.Force {
			u.reportConflict(rel)
			return nil
		}
		u.logf("replacing a file with a directory: %s", rel)
		if err := os.Remove(goalPath); err != nil {
			return fmt.Errorf("replace %s: %w", goalPath, err)
		}
		return u.createDirectory(goalPath, rel)
	}
	if goalInfo.IsDir() {
		if !u.Force {
			u.reportConflict(rel)
			return nil
		}
		return fmt.Errorf("cannot replace directory %s with a file; remove it manually", rel)
	}
	if goalInfo.Mode()&fs.ModeSymlink == 0 {
		sourceInfo, err := u.stat(sourcePath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", sourcePath, err)
		}
		if u.inSync(sourcePath, goalPath, sourceInfo, goalInfo) {
			return nil
		}
	}
	if !u.Force {
		u.reportConflict(rel)
		return nil
	}
	if err := u.mkdirAll(filepath.Dir(goalPath), 0o755); err != nil {
		return err
	}
	return u.installFile(sourcePath, goalPath, rel)
}

// inSync reports whether the goal already matches, from Upload's point of
// view: in link mode only a hardlink counts, while copy mode also accepts an
// up-to-date copy.
func (u *uploader) inSync(sourcePath, goalPath string, sourceInfo, goalInfo os.FileInfo) bool {
	if os.SameFile(sourceInfo, goalInfo) {
		return true
	}
	return u.Copy && filesInSync(sourcePath, goalPath, sourceInfo, goalInfo)
}

// installFile places the source file at goalPath as a hardlink, or as a copy
// in copy mode. It always works through a temporary sibling and a rename, so
// an existing drive file is never missing while the new one is prepared.
func (u *uploader) installFile(sourcePath, goalPath, rel string) error {
	tmp, err := os.CreateTemp(filepath.Dir(goalPath), ".driveignore-*")
	if err != nil {
		return fmt.Errorf("install %s: %w", rel, err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName) // free the name for the link or copy

	var installErr error
	if u.Copy {
		installErr = u.copyFile(sourcePath, tmpName)
	} else {
		installErr = u.link(sourcePath, tmpName)
	}
	if installErr != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install %s: %w", rel, installErr)
	}
	if err := u.rename(tmpName, goalPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install %s: %w", rel, err)
	}
	if u.Copy {
		u.logf("copied file: %s", rel)
	} else {
		u.logf("created hard link: %s", rel)
	}
	return nil
}

// copyFile copies sourcePath to dst, preserving permissions and modification
// time so later comparisons can tell an up-to-date copy from a stale one.
func copyFile(sourcePath, dst string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, source); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, time.Now(), info.ModTime())
}

// filesInSync reports whether the goal file already matches the source file.
// Hardlinks always match. Otherwise sizes must agree and modification times
// must match within a small tolerance for timestamp rounding; when a
// timestamp is recent enough to be racy, the content is compared too, so an
// edit made within the same second is never mistaken for an up-to-date copy.
func filesInSync(sourcePath, goalPath string, sourceInfo, goalInfo os.FileInfo) bool {
	if sourceInfo.IsDir() || goalInfo.IsDir() {
		return false
	}
	if os.SameFile(sourceInfo, goalInfo) {
		return true
	}
	if sourceInfo.Size() != goalInfo.Size() {
		return false
	}
	const tolerance = 2 * time.Second
	delta := goalInfo.ModTime().Sub(sourceInfo.ModTime())
	if delta > tolerance || delta < -tolerance {
		return false
	}
	if time.Since(goalInfo.ModTime()) < tolerance || time.Since(sourceInfo.ModTime()) < tolerance {
		return sameContent(sourcePath, goalPath)
	}
	return true
}

// sameContent reports whether two files have identical content, comparing in
// chunks so large files are not read into memory at once.
func sameContent(pathA, pathB string) bool {
	a, err := os.Open(pathA)
	if err != nil {
		return false
	}
	defer func() { _ = a.Close() }()
	b, err := os.Open(pathB)
	if err != nil {
		return false
	}
	defer func() { _ = b.Close() }()

	bufA := make([]byte, 64*1024)
	bufB := make([]byte, 64*1024)
	for {
		nA, errA := io.ReadFull(a, bufA)
		nB, errB := io.ReadFull(b, bufB)
		if nA != nB || !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false
		}
		endA := errA == io.EOF || errA == io.ErrUnexpectedEOF
		endB := errB == io.EOF || errB == io.ErrUnexpectedEOF
		if endA || endB {
			return endA == endB
		}
		if errA != nil || errB != nil {
			return false
		}
	}
}

// Clean removes files from Output whose source counterpart is missing or is a
// different file. Directories are left in place. With PruneIgnored, files that
// the ignore rules exclude are removed as well; DryRun reports without
// removing.
func Clean(c Config, opts CleanOptions) ([]string, error) {
	c, err := resolveRoots(c)
	if err != nil {
		return nil, err
	}
	var ignored Matcher
	if opts.PruneIgnored {
		matcher, _, err := c.rules()
		if err != nil {
			return nil, err
		}
		ignored = matcher // nil means nothing is ignored
	}
	var removed []string
	err = Walk(c.Output, func(path string, entry fs.DirEntry, rel string) error {
		didRemove, err := c.removeEntry(path, entry, rel, ignored, opts.DryRun)
		if err != nil {
			return err
		}
		if didRemove {
			removed = append(removed, filepath.ToSlash(rel))
		}
		return nil
	})
	return removed, err
}

// removeEntry reports whether the drive-side entry at path is stale and
// removes it unless dryRun is set. A stat error other than not-exist aborts
// instead of being treated as a missing file, so a transient error can never
// delete data.
func (c Config) removeEntry(path string, entry fs.DirEntry, rel string, ignored Matcher, dryRun bool) (bool, error) {
	if entry.Type()&fs.ModeSymlink != 0 || entry.IsDir() {
		return false, nil
	}
	sourcePath := filepath.Join(c.Input, rel)
	sourceInfo, sourceErr := c.stat(sourcePath)
	if sourceErr != nil && !errors.Is(sourceErr, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", sourcePath, sourceErr)
	}
	if sourceErr == nil {
		entryInfo, err := entry.Info()
		if err != nil {
			return false, err
		}
		keep := filesInSync(sourcePath, path, sourceInfo, entryInfo)
		pruned := ignored != nil && ignored.Match(sourcePath, false)
		if keep && !pruned {
			return false, nil
		}
	}
	relSlash := filepath.ToSlash(rel)
	if !dryRun {
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("remove %s: %w", relSlash, err)
		}
	}
	if dryRun {
		c.logf("would remove: %s", relSlash)
	} else {
		c.logf("removed: %s", relSlash)
	}
	return true, nil
}

// sameEntry reports whether the entry and its counterpart at other describe
// the same file or directory. A missing counterpart yields false with a nil
// error so callers can treat it as a difference.
func sameEntry(c Config, entryPath string, entry fs.DirEntry, other string) (bool, error) {
	otherInfo, err := c.stat(other)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", other, err)
	}
	if entry.IsDir() != otherInfo.IsDir() {
		return false, nil
	}
	if entry.IsDir() {
		return true, nil
	}
	entryInfo, err := entry.Info()
	if err != nil {
		return false, err
	}
	return filesInSync(other, entryPath, otherInfo, entryInfo), nil
}

// Diff walks both sides and reports paths that exist on only one of them.
// Ignore rules apply to the Input side only.
func Diff(c Config) (DiffResult, error) {
	c, err := resolveRoots(c)
	if err != nil {
		return DiffResult{}, err
	}
	matcher, err := c.ignore()
	if err != nil {
		return DiffResult{}, err
	}
	var res DiffResult
	err = Walk(c.Input, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if excluded, dir := skip(matcher, path, entry); excluded {
			if dir {
				return filepath.SkipDir
			}
			return nil
		}
		same, err := sameEntry(c, path, entry, filepath.Join(c.Output, rel))
		if err != nil {
			return err
		}
		if !same {
			res.Missing = append(res.Missing, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	err = Walk(c.Output, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		same, err := sameEntry(c, path, entry, filepath.Join(c.Input, rel))
		if err != nil {
			return err
		}
		if !same {
			res.Old = append(res.Old, filepath.ToSlash(rel))
		}
		return nil
	})
	return res, err
}

// Unify uploads with Force and then cleans, so Output mirrors Input minus the
// ignored files. The upload half's conflicts are returned; the removed list is
// discarded.
func Unify(c Config, opts UnifyOptions) (UploadResult, error) {
	res, err := Upload(c, UploadOptions{Force: true, Copy: opts.Copy})
	if err != nil {
		return res, err
	}
	_, err = Clean(c, CleanOptions{PruneIgnored: opts.PruneIgnored})
	return res, err
}

package driveignore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Options configures a core operation. Input is the source directory; Output
// is the drive folder, except for Clean where Output is walked and Input is
// only used as the reference.
type Options struct {
	Input            string
	Output           string
	GlobalIgnorePath string
	MergeIgnores     bool
	Force            bool
	// DryRun makes Clean report its removals without deleting anything.
	DryRun bool
	// PruneIgnored makes Clean remove drive files that the ignore rules
	// exclude even when the source still contains them.
	PruneIgnored bool

	// Out receives user-facing notices. Defaults to io.Discard.
	Out io.Writer
	// Log receives verbose diagnostics. nil disables them.
	Log func(format string, args ...any)

	statFn func(string) (os.FileInfo, error)   // nil means os.Stat
	linkFn func(oldname, newname string) error // nil means os.Link
}

// DiffResult lists relative paths that exist on only one side.
type DiffResult struct {
	Missing []string // present in Input, missing from Output
	Old     []string // present in Output, missing from Input
}

func (o Options) out() io.Writer {
	if o.Out == nil {
		return io.Discard
	}
	return o.Out
}

func (o Options) stat(path string) (os.FileInfo, error) {
	if o.statFn != nil {
		return o.statFn(path)
	}
	return os.Stat(path)
}

func (o Options) link(oldname, newname string) error {
	if o.linkFn != nil {
		return o.linkFn(oldname, newname)
	}
	return os.Link(oldname, newname)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

func (o Options) notice(rel string) {
	_, _ = fmt.Fprintf(o.out(), "cannot upload '%s'. A file with the same name already exists.\n", rel)
}

func (o Options) ignore() (Matcher, error) {
	matcher, typ, err := LoadIgnore(o.GlobalIgnorePath, o.Input, o.MergeIgnores)
	if err != nil {
		return nil, err
	}
	if typ == NoIgnore {
		return nil, fmt.Errorf("no .driveignore found in %s or at %s", o.Input, o.GlobalIgnorePath)
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

func resolveRoots(o Options) (Options, error) {
	input, err := resolveRoot(o.Input)
	if err != nil {
		return o, err
	}
	output, err := resolveRoot(o.Output)
	if err != nil {
		return o, err
	}
	o.Input, o.Output = input, output
	return o, nil
}

// Upload hardlinks the files of Input into Output, honoring the ignore rules.
// Directories are created, including empty ones. When an entry already exists
// under the same relative path it is reported and skipped; with Force it is
// replaced by a hardlink or directory from the source.
func Upload(o Options) error {
	o, err := resolveRoots(o)
	if err != nil {
		return err
	}
	matcher, err := o.ignore()
	if err != nil {
		return err
	}
	return Walk(o.Input, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			o.logf("skipped symlink: %s", filepath.ToSlash(rel))
			return nil
		}
		if entry.IsDir() && matcher.Match(path, true) {
			o.logf("skipped directory: %s", filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		if !entry.IsDir() && matcher.Match(path, false) {
			o.logf("skipped file: %s", filepath.ToSlash(rel))
			return nil
		}
		return o.uploadEntry(path, filepath.Join(o.Output, rel), entry, rel)
	})
}

// uploadEntry creates the drive-side entry for one source path, or reconciles
// it with an entry that is already there.
func (o Options) uploadEntry(sourcePath, goalPath string, entry fs.DirEntry, rel string) error {
	relSlash := filepath.ToSlash(rel)
	goalInfo, goalErr := os.Lstat(goalPath)
	if goalErr != nil && !errors.Is(goalErr, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", goalPath, goalErr)
	}
	if errors.Is(goalErr, fs.ErrNotExist) {
		if entry.IsDir() {
			return o.createDirectory(goalPath, rel)
		}
		if err := os.MkdirAll(filepath.Dir(goalPath), 0o755); err != nil {
			return err
		}
		if err := o.link(sourcePath, goalPath); err != nil {
			return fmt.Errorf("link %s: %w", relSlash, err)
		}
		o.logf("created hard link: %s", relSlash)
		return nil
	}
	return o.reconcile(sourcePath, goalPath, entry, goalInfo, relSlash)
}

func (o Options) createDirectory(goalPath, rel string) error {
	if err := os.MkdirAll(goalPath, 0o755); err != nil {
		return err
	}
	o.logf("created directory: %s", filepath.ToSlash(rel))
	return nil
}

// reconcile handles an existing drive-side entry. Without Force, conflicts are
// reported and skipped. With Force the entry is replaced; a directory is never
// deleted to make room for a file, that conflict must be resolved by hand.
func (o Options) reconcile(sourcePath, goalPath string, entry fs.DirEntry, goalInfo os.FileInfo, rel string) error {
	if entry.IsDir() {
		if goalInfo.IsDir() {
			return nil
		}
		if !o.Force {
			o.notice(rel)
			return nil
		}
		o.logf("replacing a file with a directory: %s", rel)
		if err := os.Remove(goalPath); err != nil {
			return fmt.Errorf("replace %s: %w", goalPath, err)
		}
		return o.createDirectory(goalPath, rel)
	}
	if goalInfo.IsDir() {
		if !o.Force {
			o.notice(rel)
			return nil
		}
		return fmt.Errorf("cannot replace directory %s with a file; remove it manually", rel)
	}
	if goalInfo.Mode()&fs.ModeSymlink == 0 {
		sourceInfo, err := o.stat(sourcePath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", sourcePath, err)
		}
		if os.SameFile(sourceInfo, goalInfo) {
			return nil
		}
	}
	if !o.Force {
		o.notice(rel)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(goalPath), 0o755); err != nil {
		return err
	}
	return o.replaceWithLink(sourcePath, goalPath, rel)
}

// replaceWithLink links sourcePath beside goalPath and renames the new link
// over the goal. A failed link never removes the existing drive file, and the
// rename replaces a regular file or symlink in one step.
func (o Options) replaceWithLink(sourcePath, goalPath, rel string) error {
	tmp, err := os.CreateTemp(filepath.Dir(goalPath), ".driveignore-*")
	if err != nil {
		return fmt.Errorf("replace %s: %w", rel, err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName) // free the name for the link
	if err := o.link(sourcePath, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("link %s: %w", rel, err)
	}
	if err := os.Rename(tmpName, goalPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", rel, err)
	}
	o.logf("created hard link: %s", rel)
	return nil
}

// Clean removes files from Output whose source counterpart is missing or is a
// different file. Directories are left in place. Stat errors other than
// not-exist abort the walk instead of being treated as missing files, so a
// transient error can never delete data. With PruneIgnored, files that the
// ignore rules exclude are removed as well; DryRun reports without removing.
func Clean(o Options) ([]string, error) {
	o, err := resolveRoots(o)
	if err != nil {
		return nil, err
	}
	var ignored Matcher
	if o.PruneIgnored {
		matcher, _, err := LoadIgnore(o.GlobalIgnorePath, o.Input, o.MergeIgnores)
		if err != nil {
			return nil, err
		}
		ignored = matcher
	}
	var removed []string
	err = Walk(o.Output, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		sourcePath := filepath.Join(o.Input, rel)
		sourceInfo, sourceErr := o.stat(sourcePath)
		if sourceErr != nil && !errors.Is(sourceErr, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", sourcePath, sourceErr)
		}
		if sourceErr == nil {
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			sameFile := os.SameFile(entryInfo, sourceInfo)
			pruned := ignored != nil && ignored.Match(sourcePath, false)
			if sameFile && !pruned {
				return nil
			}
		}
		if !o.DryRun {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove %s: %w", filepath.ToSlash(rel), err)
			}
		}
		removed = append(removed, filepath.ToSlash(rel))
		if o.DryRun {
			o.logf("would remove: %s", filepath.ToSlash(rel))
		} else {
			o.logf("removed: %s", filepath.ToSlash(rel))
		}
		return nil
	})
	return removed, err
}

// sameEntry reports whether the entry and its counterpart at other describe
// the same file or directory. A missing counterpart yields false with a nil
// error so callers can treat it as a difference.
func sameEntry(o Options, entry fs.DirEntry, other string) (bool, error) {
	otherInfo, err := o.stat(other)
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
	return os.SameFile(entryInfo, otherInfo), nil
}

// Diff walks both sides and reports paths that exist on only one of them.
// Ignore rules apply to the Input side only.
func Diff(o Options) (DiffResult, error) {
	o, err := resolveRoots(o)
	if err != nil {
		return DiffResult{}, err
	}
	matcher, err := o.ignore()
	if err != nil {
		return DiffResult{}, err
	}
	var res DiffResult
	err = Walk(o.Input, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() && matcher.Match(path, true) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && matcher.Match(path, false) {
			return nil
		}
		same, err := sameEntry(o, entry, filepath.Join(o.Output, rel))
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
	err = Walk(o.Output, func(_ string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		same, err := sameEntry(o, entry, filepath.Join(o.Input, rel))
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
// ignored files.
func Unify(o Options) error {
	o.Force = true
	if err := Upload(o); err != nil {
		return err
	}
	_, err := Clean(o)
	return err
}

package driveignore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	gitignore "github.com/monochromegane/go-gitignore"
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

	// Out receives user-facing notices. Defaults to io.Discard.
	Out io.Writer
	// Log receives verbose diagnostics. nil disables them.
	Log func(format string, args ...any)

	statFn func(string) (os.FileInfo, error) // nil means os.Stat
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

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

func (o Options) ignore() (gitignore.IgnoreMatcher, error) {
	matcher, typ, err := LoadIgnore(o.GlobalIgnorePath, o.Input, o.MergeIgnores)
	if err != nil {
		return nil, err
	}
	if typ == NoIgnore {
		return nil, fmt.Errorf("no .driveignore found in %s or at %s", o.Input, o.GlobalIgnorePath)
	}
	return matcher, nil
}

// Upload hardlinks the files of Input into Output, honoring the ignore rules.
// Directories are created, including empty ones. When a file already exists
// under the same relative path it is reported and skipped; with Force it is
// replaced by a hardlink to the source file.
func Upload(o Options) error {
	matcher, err := o.ignore()
	if err != nil {
		return err
	}
	return Walk(o.Input, func(path string, entry fs.DirEntry, rel string) error {
		if entry.IsDir() && matcher.Match(path, true) {
			o.logf("skipped directory: %s", filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		if !entry.IsDir() && matcher.Match(path, false) {
			o.logf("skipped file: %s", filepath.ToSlash(rel))
			return nil
		}

		goalPath := filepath.Join(o.Output, rel)
		goalInfo, goalErr := o.stat(goalPath)
		if goalErr != nil && !errors.Is(goalErr, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", goalPath, goalErr)
		}
		goalMissing := errors.Is(goalErr, fs.ErrNotExist)

		sameNameDifferentFile := false
		if goalErr == nil && !entry.IsDir() {
			sourceInfo, err := o.stat(path)
			if err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}
			if !os.SameFile(sourceInfo, goalInfo) {
				if !o.Force {
					_, _ = fmt.Fprintf(o.out(), "cannot upload '%s'. A file with the same name already exists.\n", filepath.ToSlash(rel))
					return nil
				}
				o.logf("overwriting a file with same name: %s", filepath.ToSlash(rel))
				if err := os.Remove(goalPath); err != nil {
					return fmt.Errorf("replace %s: %w", goalPath, err)
				}
				sameNameDifferentFile = true
			}
		}

		if !goalMissing && !sameNameDifferentFile {
			return nil
		}
		if entry.IsDir() {
			if err := os.MkdirAll(goalPath, 0o755); err != nil {
				return err
			}
			o.logf("created directory: %s", filepath.ToSlash(rel))
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(goalPath), 0o755); err != nil {
			return err
		}
		if err := os.Link(path, goalPath); err != nil {
			return fmt.Errorf("link %s: %w", filepath.ToSlash(rel), err)
		}
		o.logf("created hard link: %s", filepath.ToSlash(rel))
		return nil
	})
}

// Clean removes files from Output whose source counterpart is missing or is a
// different file. Directories are left in place. Stat errors other than
// not-exist abort the walk instead of being treated as missing files, so a
// transient error can never delete data.
func Clean(o Options) ([]string, error) {
	var removed []string
	err := Walk(o.Output, func(path string, entry fs.DirEntry, rel string) error {
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
			if os.SameFile(entryInfo, sourceInfo) {
				return nil
			}
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove %s: %w", filepath.ToSlash(rel), err)
		}
		removed = append(removed, filepath.ToSlash(rel))
		o.logf("removed: %s", filepath.ToSlash(rel))
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
	matcher, err := o.ignore()
	if err != nil {
		return DiffResult{}, err
	}
	var res DiffResult
	err = Walk(o.Input, func(path string, entry fs.DirEntry, rel string) error {
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

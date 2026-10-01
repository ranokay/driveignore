package driveignore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/go-git/go-git/v5/plumbing/format/gitignore"
)

// IgnoreType says which .driveignore files an operation loaded.
type IgnoreType int

const (
	// NoIgnore means neither a local nor a global .driveignore exists.
	NoIgnore IgnoreType = iota
	// LocalIgnore means only the source directory's .driveignore applies.
	LocalIgnore
	// GlobalIgnore means only the global .driveignore applies.
	GlobalIgnore
	// MergedIgnore means both apply, with local patterns taking precedence.
	MergedIgnore
)

// Matcher reports whether a path (relative to or below the source directory)
// is excluded by the loaded .driveignore files.
type Matcher interface {
	Match(path string, isDir bool) bool
}

type pathMatcher struct {
	matcher gitignore.Matcher
	root    string
}

func (m pathMatcher) Match(path string, isDir bool) bool {
	rel, err := filepath.Rel(m.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return m.matcher.Match(strings.Split(filepath.ToSlash(rel), "/"), isDir)
}

// LoadIgnore resolves the .driveignore files that apply to localPath: the
// local file at localPath/.driveignore and the global file at globalPath.
// When merge is set and both files exist, global patterns are applied first
// and local patterns override them.
func LoadIgnore(globalPath, localPath string, merge bool) (Matcher, IgnoreType, error) {
	localContent, localErr := os.ReadFile(filepath.Join(localPath, ".driveignore"))
	if localErr != nil && !errors.Is(localErr, fs.ErrNotExist) {
		return nil, NoIgnore, localErr
	}
	globalContent, globalErr := os.ReadFile(globalPath)
	if globalErr != nil && !errors.Is(globalErr, fs.ErrNotExist) {
		return nil, NoIgnore, globalErr
	}
	localExists := localErr == nil
	globalExists := globalErr == nil

	switch {
	case localExists && (!globalExists || !merge):
		return newMatcher(localPath, localContent), LocalIgnore, nil
	case globalExists && (!localExists || !merge):
		return newMatcher(localPath, globalContent), GlobalIgnore, nil
	case localExists && globalExists:
		merged := append(bytes.Clone(globalContent), '\n')
		merged = append(merged, localContent...)
		return newMatcher(localPath, merged), MergedIgnore, nil
	default:
		return nil, NoIgnore, nil
	}
}

// newMatcher parses gitignore-syntax content. Blank lines and comments are
// skipped here; a backslash-escaped leading hash is a literal pattern, so the
// escape is stripped to keep matching portable across operating systems.
// Lines are split without a length limit so long rules cannot silently drop
// the patterns that follow them.
func newMatcher(root string, content []byte) Matcher {
	var patterns []gitignore.Pattern
	for _, raw := range bytes.Split(content, []byte("\n")) {
		line := strings.TrimSuffix(string(raw), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, `\#`); ok {
			line = "#" + rest
		}
		// Git treats "foo/**" as everything inside foo, not foo itself.
		// go-git matches the bare prefix, and upload would then skip the
		// whole directory; adding the trailing segment restores git's
		// semantics so negations inside foo stay reachable.
		if strings.HasSuffix(line, "/**") {
			line += "/*"
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	return pathMatcher{matcher: gitignore.NewMatcher(patterns), root: root}
}

// GlobalIgnorePath returns the path of the global .driveignore inside the
// user's config directory.
func GlobalIgnorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "driveignore", ".global_driveignore"), nil
}

// EnsureFile creates path and its parents when path does not exist yet.
// Existing files are left untouched.
func EnsureFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o644)
}

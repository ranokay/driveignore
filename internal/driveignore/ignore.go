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

// Matcher reports whether a path (relative to or below the source directory)
// is excluded by the loaded .driveignore files.
type Matcher interface {
	Match(path string, isDir bool) bool
}

type pathMatcher struct {
	patterns []gitignore.Pattern
	root     string
}

func (m pathMatcher) Match(path string, isDir bool) bool {
	rel, err := filepath.Rel(m.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return matchPatterns(m.patterns, strings.Split(filepath.ToSlash(rel), "/"), isDir)
}

// matchPatterns applies patterns in reverse order so the last matching
// pattern wins, the way git resolves overlapping rules.
func matchPatterns(patterns []gitignore.Pattern, path []string, isDir bool) bool {
	for i := len(patterns) - 1; i >= 0; i-- {
		switch patterns[i].Match(path, isDir) {
		case gitignore.Exclude:
			return true
		case gitignore.Include:
			return false
		}
	}
	return false
}

// LoadIgnore resolves the ignore rules that apply to root: the root file at
// root/.driveignore, the global file resolved through globalPath, and every
// nested .driveignore below root. The global path is resolved and its file
// read only when those rules can matter: merge is true, or no root file
// exists. Nested patterns are anchored to their own directory and override
// rules from shallower files. A nil Matcher with a nil error reports that no
// ignore file exists anywhere; the returned string is the resolved global
// path, empty when it was never needed.
func LoadIgnore(root string, merge bool, globalPath func() (string, error)) (Matcher, string, error) {
	localFile := filepath.Join(root, ".driveignore")
	localPatterns, localExists, err := readPatterns(localFile, nil)
	if err != nil {
		return nil, "", err
	}

	var globalPatterns []gitignore.Pattern
	var globalExists bool
	var globalPathUsed string
	if merge || !localExists {
		globalPathUsed, err = globalPath()
		if err != nil {
			return nil, "", err
		}
		globalPatterns, globalExists, err = readPatterns(globalPathUsed, nil)
		if err != nil {
			return nil, "", err
		}
	}

	var patterns []gitignore.Pattern
	switch {
	case localExists && (!globalExists || !merge):
		patterns = append(patterns, localPatterns...)
	case globalExists && (!localExists || !merge):
		patterns = append(patterns, globalPatterns...)
	case localExists && globalExists:
		patterns = append(patterns, globalPatterns...)
		patterns = append(patterns, localPatterns...)
	}

	nested, nestedFound, err := nestedPatterns(root)
	if err != nil {
		return nil, "", err
	}
	patterns = append(patterns, nested...)
	if !localExists && !globalExists && !nestedFound {
		return nil, globalPathUsed, nil
	}
	return pathMatcher{patterns: patterns, root: root}, globalPathUsed, nil
}

// readPatterns parses the file at path into patterns anchored at domain. A
// missing file is not an error.
func readPatterns(path string, domain []string) ([]gitignore.Pattern, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return parsePatterns(content, domain), true, nil
}

// nestedPatterns collects .driveignore files below root, in walk order so
// parents come before children and deeper files win. The root file is skipped
// because LoadIgnore handles it. found reports whether any nested file exists.
func nestedPatterns(root string) (patterns []gitignore.Pattern, found bool, err error) {
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != ".driveignore" {
			return nil
		}
		dir := filepath.Dir(path)
		if dir == root {
			return nil
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		domain := strings.Split(filepath.ToSlash(rel), "/")
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		patterns = append(patterns, parsePatterns(content, domain)...)
		found = true
		return nil
	})
	return patterns, found, err
}

// parsePatterns parses gitignore-syntax content. Blank lines and comments are
// skipped here; a backslash-escaped leading hash is a literal pattern, so the
// escape is stripped to keep matching portable across operating systems.
// Lines are split without a length limit so long rules cannot silently drop
// the patterns that follow them.
func parsePatterns(content []byte, domain []string) []gitignore.Pattern {
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
		patterns = append(patterns, gitignore.ParsePattern(line, domain))
	}
	return patterns
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

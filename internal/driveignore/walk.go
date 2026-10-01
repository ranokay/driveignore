package driveignore

import (
	"io/fs"
	"path/filepath"
)

// skip reports whether the matcher excludes the entry. skipDir is true when
// the entry is a directory whose whole subtree is excluded.
func skip(matcher Matcher, path string, entry fs.DirEntry) (skip, skipDir bool) {
	if entry.IsDir() {
		return matcher.Match(path, true), true
	}
	return matcher.Match(path, false), false
}

// Walk visits every entry below root in lexical order, skipping root itself.
// The callback receives the full path, the directory entry, and the path
// relative to root. Directory entries keep their platform separators in rel;
// display code converts with filepath.ToSlash.
func Walk(root string, fn func(path string, entry fs.DirEntry, rel string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return fn(path, entry, rel)
	})
}

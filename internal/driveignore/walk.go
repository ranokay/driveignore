package driveignore

import (
	"io/fs"
	"path/filepath"
)

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

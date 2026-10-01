package driveignore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	gitignore "github.com/monochromegane/go-gitignore"
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

// LoadIgnore resolves the .driveignore files that apply to localPath: the
// local file at localPath/.driveignore and the global file at globalPath.
// When merge is set and both files exist, global patterns are applied first
// and local patterns can override them.
func LoadIgnore(globalPath, localPath string, merge bool) (gitignore.IgnoreMatcher, IgnoreType, error) {
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
		return gitignore.NewGitIgnoreFromReader(localPath, bytes.NewReader(localContent)), LocalIgnore, nil
	case globalExists && (!localExists || !merge):
		return gitignore.NewGitIgnoreFromReader(localPath, bytes.NewReader(globalContent)), GlobalIgnore, nil
	case localExists && globalExists:
		merged := append(bytes.Clone(globalContent), '\n')
		merged = append(merged, localContent...)
		return gitignore.NewGitIgnoreFromReader(localPath, bytes.NewReader(merged)), MergedIgnore, nil
	default:
		return nil, NoIgnore, nil
	}
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

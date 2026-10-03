package driveignore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// journalVersion identifies the on-disk state format. A journal from another
// version is unusable, never migrated: degrading to an empty journal can only
// produce creations, while trusting a differently shaped one could authorize
// deleting a file that was never proven synced.
const journalVersion = 1

// journalEntry records one path proven synced between the two sides, so a
// later pass can tell a one-sided path that was created from one that was
// deleted. Type is "file" or "dir"; times and stamps are unix nanoseconds.
// Files use Inode/Size/ModTime (the mtime both hardlinked names share), while
// directories use LocalStamp/OutStamp (their per-directory change stamps on
// each side), where 0 means unknown and forces a walk.
type journalEntry struct {
	Type       string
	Inode      uint64
	Size       int64
	ModTime    int64
	LocalStamp int64
	OutStamp   int64
}

// journal is the per-pair deletion proof: the set of paths known to be synced
// on both sides. It is written whole, only after a complete clean pass.
type journal struct {
	Version int
	Input   string
	Output  string
	Entries map[string]journalEntry
}

// loadJournal reads the state file at path. Every failure — missing,
// unreadable, corrupt, a different format version, or a stored pair that does
// not match the file's pair key — collapses into an empty journal and false.
// The journal is what licenses deletions, so no partially usable state may
// ever be read back: an empty journal can at worst duplicate or restore
// files, while a bad one could authorize deleting real ones.
func loadJournal(path string) (*journal, bool) {
	empty := func() *journal {
		return &journal{Version: journalVersion, Entries: map[string]journalEntry{}}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return empty(), false
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return empty(), false
	}
	if j.Version != journalVersion {
		return empty(), false
	}
	expected, err := WatchStatePath(j.Input, j.Output)
	if err != nil || filepath.Base(expected) != filepath.Base(path) {
		return empty(), false
	}
	if j.Entries == nil {
		j.Entries = map[string]journalEntry{}
	}
	return &j, true
}

// saveJournal atomically replaces the state file at path through a temporary
// sibling and a rename, so a reader or a crash can never observe a
// half-written journal.
func saveJournal(path string, j *journal) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".driveignore-watch-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	encErr := json.NewEncoder(tmp).Encode(j)
	closeErr := tmp.Close()
	if encErr != nil || closeErr != nil {
		_ = os.Remove(tmpName)
		if encErr != nil {
			return encErr
		}
		return closeErr
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// WatchPairHash returns a stable short name for the (input, output) pair,
// derived from both absolute paths with symlinks resolved, so one directory
// named through different spellings (for example /var and /private/var, or a
// Drive mount reached through two paths) keys one journal, lock and launchd
// label. Per-pair artifacts (journal, lock, launchd label) are keyed by it.
func WatchPairHash(input, output string) (string, error) {
	canonInput, err := canonicalPairRoot(input)
	if err != nil {
		return "", err
	}
	canonOutput, err := canonicalPairRoot(output)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonInput + "\x00" + canonOutput))
	return hex.EncodeToString(sum[:])[:12], nil
}

// canonicalPairRoot is the pair-key spelling of one root: absolute, with
// symlinks resolved. A path EvalSymlinks cannot resolve keeps its absolute
// spelling instead of failing, so --uninstall still derives the pair key after
// the trees are gone.
func canonicalPairRoot(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs, nil
	}
	return resolved, nil
}

// WatchStatePath returns the journal path for the pair inside the driveignore
// directory under the user's config directory.
func WatchStatePath(input, output string) (string, error) {
	return watchPairPath(input, output, ".json")
}

// WatchLockPath returns the lock path for the pair, next to the journal.
func WatchLockPath(input, output string) (string, error) {
	return watchPairPath(input, output, ".lock")
}

// watchPairPath owns the per-pair file naming so the state, lock and any
// future artifacts share one key.
func watchPairPath(input, output, ext string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	hash, err := WatchPairHash(input, output)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "driveignore", "watch-"+hash+ext), nil
}

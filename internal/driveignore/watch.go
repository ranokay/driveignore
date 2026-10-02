package driveignore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// defaultChurnAge is the window within which a file's modification time marks
// it as possibly still being written, so a pass defers acting on it.
const defaultChurnAge = 2 * time.Second

// errApplyNotImplemented marks the deliberate gap between classifying a pass
// and applying it: Reconcile only serves dry runs until the apply step lands.
var errApplyNotImplemented = errors.New("watch: applying actions is not implemented yet")

// WatchOptions configures a reconcile pass. DryRun classifies without
// mutating anything; OneWay makes the local tree authoritative. MinAge is the
// churn window (zero means defaultChurnAge); TrashDir is where local
// deletions are moved (empty means the user's Trash).
type WatchOptions struct {
	DryRun   bool
	OneWay   bool
	MinAge   time.Duration
	TrashDir string
}

// ActionKind names what a pass decided for one path. The string is the stable
// label the command layer prints; each kind names the side it changes.
type ActionKind string

const (
	ActionCreatedDir   ActionKind = "created-dir"
	ActionLinked       ActionKind = "linked"
	ActionRelinked     ActionKind = "relinked"
	ActionImported     ActionKind = "imported"
	ActionTrashedLocal ActionKind = "trashed-local"
	ActionRemovedDrive ActionKind = "removed-drive"
	ActionConflict     ActionKind = "conflict"
	ActionDeferred     ActionKind = "deferred"
	ActionTypeConflict ActionKind = "type-conflict"
)

// Action is one classified path: Kind is what a pass would do, Path is the
// relative path with forward slashes, Detail carries the "self-heal" direction
// of a relink ("local wins" or "drive wins") and is empty otherwise.
type Action struct {
	Kind   ActionKind
	Path   string
	Detail string
}

// Report is the full decision of one pass, ordered so creations happen
// parent-first and deletions child-first.
type Report struct {
	Actions []Action
}

// googleStubExts is the built-in exclusion list for the placeholder files
// Drive for desktop materializes for native Google documents. It applies
// before .driveignore rules, and Task 2 confirmed the mount produces no other
// client-side temp artifacts.
var googleStubExts = map[string]bool{
	".gdoc":      true,
	".gsheet":    true,
	".gslides":   true,
	".gdraw":     true,
	".gshortcut": true,
}

func isGoogleStub(name string) bool {
	return googleStubExts[strings.ToLower(filepath.Ext(name))]
}

// Reconcile scans both trees and reports the actions a pass would take. The
// journal decides whether a one-sided path was created or deleted; without a
// usable one every one-sided path is a creation. Only DryRun is served for
// now: a real pass reports errApplyNotImplemented until apply lands.
func Reconcile(cfg Config, statePath string, opts WatchOptions) (Report, error) {
	if !opts.DryRun {
		return Report{}, errApplyNotImplemented
	}
	cfg, err := resolveRoots(cfg)
	if err != nil {
		return Report{}, err
	}
	// Watch always needs rules: without a .driveignore there is no agreed
	// view of what belongs in the pair.
	matcher, err := cfg.ignore()
	if err != nil {
		return Report{}, err
	}
	minAge := opts.MinAge
	if minAge == 0 {
		minAge = defaultChurnAge
	}
	j, ok := loadJournal(statePath)
	if !ok {
		cfg.logf("watch state %s is unusable; treating one-sided paths as creations", statePath)
	}

	run := &watchRun{
		cfg:     cfg,
		opts:    opts,
		minAge:  minAge,
		journal: j,
		local:   map[string]*sideEntry{},
		out:     map[string]*sideEntry{},
	}
	if err := run.scan(cfg.Input, run.local, matcher); err != nil {
		return Report{}, err
	}
	if err := run.scan(cfg.Output, run.out, matcher); err != nil {
		return Report{}, err
	}
	return Report{Actions: run.actions()}, nil
}

// watchRun carries one pass's observations. local and out are keyed by the
// walk's relative path so the two sides line up directly.
type watchRun struct {
	cfg     Config
	opts    WatchOptions
	minAge  time.Duration
	journal *journal
	local   map[string]*sideEntry
	out     map[string]*sideEntry
}

// sideEntry is one path seen during a walk. Files carry the stat data the
// decisions need; directories only their existence, because remote-origin
// directories have synthetic size, mode and nlink (Task 2 finding).
type sideEntry struct {
	isDir bool
	info  os.FileInfo
	inode uint64
}

// scan walks one side, pruning subtrees whose recorded change stamps still
// match both sides. Progress ticks every entry the walk actually visits, so a
// pruned subtree stays silent.
func (r *watchRun) scan(root string, seen map[string]*sideEntry, matcher Matcher) error {
	return Walk(root, func(path string, entry fs.DirEntry, rel string) error {
		if entry.Type()&fs.ModeSymlink != 0 {
			r.cfg.tick(rel)
			r.cfg.logf("skipped symlink: %s", filepath.ToSlash(rel))
			return nil
		}
		if isGoogleStub(entry.Name()) {
			r.cfg.tick(rel)
			r.cfg.logf("skipped Google-native stub: %s", filepath.ToSlash(rel))
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if excluded, skipDir := skip(matcher, filepath.Join(r.cfg.Input, rel), entry); excluded {
			r.cfg.tick(rel)
			if skipDir {
				r.cfg.logf("skipped directory: %s", filepath.ToSlash(rel))
				return filepath.SkipDir
			}
			r.cfg.logf("skipped file: %s", filepath.ToSlash(rel))
			return nil
		}
		if entry.IsDir() {
			if r.prune(rel) {
				return filepath.SkipDir
			}
			r.cfg.tick(rel)
			seen[rel] = &sideEntry{isDir: true}
			return nil
		}
		r.cfg.tick(rel)
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		inode, _ := fileInode(path) // 0 when unknown: anchors fail closed
		seen[rel] = &sideEntry{info: info, inode: inode}
		return nil
	})
}

// prune reports whether rel is a directory neither side changed since the
// journal recorded it: both current change stamps equal their recorded ones.
// A missing entry, a missing path or a zero (unknown) stamp forces the walk.
func (r *watchRun) prune(rel string) bool {
	entry, ok := r.journal.Entries[rel]
	if !ok || entry.Type != "dir" || entry.LocalStamp == 0 || entry.OutStamp == 0 {
		return false
	}
	localStamp, err := dirChangeStamp(filepath.Join(r.cfg.Input, rel))
	if err != nil || localStamp == 0 {
		return false
	}
	outStamp, err := dirChangeStamp(filepath.Join(r.cfg.Output, rel))
	if err != nil || outStamp == 0 {
		return false
	}
	return localStamp == entry.LocalStamp && outStamp == entry.OutStamp
}

// actions classifies every path either walk saw and orders the result.
func (r *watchRun) actions() []Action {
	var actions []Action
	for rel := range r.local {
		if action, ok := r.classify(rel); ok {
			actions = append(actions, action)
		}
	}
	for rel := range r.out {
		if _, seen := r.local[rel]; seen {
			continue
		}
		if action, ok := r.classify(rel); ok {
			actions = append(actions, action)
		}
	}
	sortActions(actions)
	return actions
}

// classify decides one path from what both walks saw and the journal. It
// reports false when the path is in sync or needs no action. One-way is
// local-authoritative: drive content never wins and drive-only paths go away.
func (r *watchRun) classify(rel string) (Action, bool) {
	local, localOK := r.local[rel]
	out, outOK := r.out[rel]
	entry, journaled := r.journal.Entries[rel]
	path := filepath.ToSlash(rel)

	switch {
	case localOK && outOK:
		if local.isDir != out.isDir {
			return Action{ActionTypeConflict, path, ""}, true
		}
		if local.isDir {
			return Action{}, false
		}
		if os.SameFile(local.info, out.info) {
			return Action{}, false
		}
		if r.opts.OneWay {
			return r.relink(path, local.info, "local wins"), true
		}
		if journaled && entry.Type == "file" && entry.Inode != 0 {
			if local.inode == entry.Inode {
				return r.relink(path, out.info, "drive wins"), true
			}
			if out.inode == entry.Inode {
				return r.relink(path, local.info, "local wins"), true
			}
		}
		if r.fresh(local.info) || r.fresh(out.info) {
			return Action{ActionDeferred, path, ""}, true
		}
		return Action{ActionConflict, path, ""}, true

	case localOK && !outOK:
		if local.isDir {
			if r.opts.OneWay || !journaled || entry.Type != "dir" {
				return Action{ActionCreatedDir, path, ""}, true
			}
			// A journaled directory the drive side lost is a deletion to
			// propagate, but only once the surviving side is empty: a
			// directory holding content is never removed.
			if dirEmpty(filepath.Join(r.cfg.Input, rel)) {
				return Action{ActionTrashedLocal, path, ""}, true
			}
			return Action{}, false
		}
		if !r.opts.OneWay && journaled && entry.Type == "file" && entry.Inode != 0 && local.inode == entry.Inode {
			if r.fresh(local.info) {
				return Action{ActionDeferred, path, ""}, true
			}
			// A survivor older than the journal's record may be a different
			// file that reused the inode: route it to creation, never deletion.
			if local.info.ModTime().UnixNano() >= entry.ModTime {
				return Action{ActionTrashedLocal, path, ""}, true
			}
		}
		return r.link(path, local.info), true

	case outOK && !localOK:
		if out.isDir {
			if r.opts.OneWay || (journaled && entry.Type == "dir") {
				if dirEmpty(filepath.Join(r.cfg.Output, rel)) {
					return Action{ActionRemovedDrive, path, ""}, true
				}
				return Action{}, false
			}
			return Action{ActionCreatedDir, path, ""}, true
		}
		if r.opts.OneWay {
			if r.fresh(out.info) {
				return Action{ActionDeferred, path, ""}, true
			}
			return Action{ActionRemovedDrive, path, ""}, true
		}
		if journaled && entry.Type == "file" && entry.Inode != 0 && out.inode == entry.Inode {
			if r.fresh(out.info) {
				return Action{ActionDeferred, path, ""}, true
			}
			// Same inode-reuse guard as the local-only deletion above.
			if out.info.ModTime().UnixNano() >= entry.ModTime {
				return Action{ActionRemovedDrive, path, ""}, true
			}
		}
		return r.importFile(path, out.info), true
	}
	return Action{}, false
}

// relink repairs a broken link from sourceInfo, the side that changed, unless
// that file is fresh enough to still be mid-write.
func (r *watchRun) relink(path string, source os.FileInfo, detail string) Action {
	if r.fresh(source) {
		return Action{ActionDeferred, path, ""}
	}
	return Action{ActionRelinked, path, detail}
}

// link installs a local-only file on the drive side.
func (r *watchRun) link(path string, source os.FileInfo) Action {
	if r.fresh(source) {
		return Action{ActionDeferred, path, ""}
	}
	return Action{ActionLinked, path, ""}
}

// importFile installs a drive-only file on the local side.
func (r *watchRun) importFile(path string, source os.FileInfo) Action {
	if r.fresh(source) {
		return Action{ActionDeferred, path, ""}
	}
	return Action{ActionImported, path, ""}
}

// fresh reports whether the file was modified within the churn window, so a
// pass must not copy it yet: it may still be mid-write.
func (r *watchRun) fresh(info os.FileInfo) bool {
	return time.Since(info.ModTime()) < r.minAge
}

// dirEmpty reports whether the directory holds no entries. A read error is
// treated as not empty: a pass must never remove a directory it could not
// inspect.
func dirEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) == 0
}

// sortActions orders a report so creations happen parent-first and deletions
// child-first: ascending by path, except deletions deepest-first.
func sortActions(actions []Action) {
	sort.Slice(actions, func(i, j int) bool {
		left, right := actions[i], actions[j]
		leftDel, rightDel := isDeletion(left.Kind), isDeletion(right.Kind)
		if leftDel != rightDel {
			return !leftDel
		}
		if leftDel {
			leftDepth := strings.Count(left.Path, "/")
			rightDepth := strings.Count(right.Path, "/")
			if leftDepth != rightDepth {
				return leftDepth > rightDepth
			}
		}
		return left.Path < right.Path
	})
}

func isDeletion(kind ActionKind) bool {
	return kind == ActionTrashedLocal || kind == ActionRemovedDrive
}

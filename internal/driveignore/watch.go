package driveignore

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// defaultChurnAge is the window within which a file's modification time marks
// it as possibly still being written, so a pass defers acting on it.
const defaultChurnAge = 2 * time.Second

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

// Detail values carried by relink and conflict actions to name the winning
// side. They appear in dry-run output, so keep the strings stable.
const (
	detailLocalWins = "local wins"
	detailDriveWins = "drive wins"
)

// Action is one classified path: Kind is what a pass would do, Path is the
// relative path with forward slashes, and Detail names the winning side of a
// self-heal relink or a conflict ("local wins" or "drive wins"); it is empty
// otherwise.
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

// Reconcile scans both trees, decides one pass's actions and, unless DryRun,
// applies them and commits the journal. A dry run never writes. A real pass
// saves state only after every action succeeded, so an error leaves the
// previous journal intact and the next pass reclassifies whatever landed.
func Reconcile(cfg Config, statePath string, opts WatchOptions) (Report, error) {
	// The state file is named after the pair as the caller named it, so a
	// replacement journal must record those same strings to load back.
	pairInput, pairOutput := cfg.Input, cfg.Output
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
		j.Input, j.Output = pairInput, pairOutput
	}

	run := &watchRun{
		cfg:     cfg,
		opts:    opts,
		minAge:  minAge,
		journal: j,
		local:   map[string]*sideEntry{},
		out:     map[string]*sideEntry{},
		pruned:  map[string]bool{},
	}
	if err := run.scan(cfg.Input, run.local, matcher); err != nil {
		return Report{}, err
	}
	if err := run.scan(cfg.Output, run.out, matcher); err != nil {
		return Report{}, err
	}
	report := Report{Actions: run.actions()}
	if opts.DryRun {
		return report, nil
	}
	unsettled, deleted, err := run.apply(report)
	if err != nil {
		return report, err
	}
	if err := run.commit(statePath, unsettled, deleted); err != nil {
		return report, err
	}
	return report, nil
}

// watchRun carries one pass's observations. local and out are keyed by the
// walk's relative path so the two sides line up directly. pruned records the
// directories the scan skipped because both change stamps matched the
// journal, so the refresh knows their entries carry over untouched.
type watchRun struct {
	cfg     Config
	opts    WatchOptions
	minAge  time.Duration
	journal *journal
	local   map[string]*sideEntry
	out     map[string]*sideEntry
	pruned  map[string]bool
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
				r.pruned[rel] = true
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
	// A path below a file/directory mismatch is part of the same divergent
	// subtree, so the ancestor's report-only conflict is the only decision
	// until a human resolves it. Acting on a descendant would install through
	// a path that is a file on the other side and fail the whole pass.
	if r.belowTypeConflict(rel) {
		return Action{}, false
	}
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
			return r.relink(path, local.info, detailLocalWins), true
		}
		if journaled && entry.Type == "file" && entry.Inode != 0 {
			if local.inode == entry.Inode {
				return r.relink(path, out.info, detailDriveWins), true
			}
			if out.inode == entry.Inode {
				return r.relink(path, local.info, detailLocalWins), true
			}
		}
		if r.fresh(local.info) || r.fresh(out.info) {
			return Action{ActionDeferred, path, ""}, true
		}
		return Action{ActionConflict, path, conflictDetail(local.info, out.info)}, true

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

// belowTypeConflict reports whether an ancestor of rel is present on both
// sides with mismatched kinds.
func (r *watchRun) belowTypeConflict(rel string) bool {
	for parent := filepath.Dir(rel); parent != rel && parent != "."; parent = filepath.Dir(parent) {
		local, localOK := r.local[parent]
		out, outOK := r.out[parent]
		if localOK && outOK && local.isDir != out.isDir {
			return true
		}
	}
	return false
}

// relink repairs a broken link from sourceInfo, the side that changed, unless
// that file is fresh enough to still be mid-write.
func (r *watchRun) relink(path string, source os.FileInfo, detail string) Action {
	if r.fresh(source) {
		return Action{ActionDeferred, path, ""}
	}
	return Action{ActionRelinked, path, detail}
}

// conflictDetail names the side a conflict resolves to: the newest mtime
// wins, and equal times favor local so the outcome is deterministic.
func conflictDetail(local, out os.FileInfo) string {
	if out.ModTime().After(local.ModTime()) {
		return detailDriveWins
	}
	return detailLocalWins
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

// apply executes the report and returns the paths whose action it left for a
// later pass, marked together with their ancestors, plus the paths it deleted.
// It runs before the journal commit, so any error leaves the previous journal
// intact and the next pass reclassifies the half-applied result, converges and
// commits then.
func (r *watchRun) apply(report Report) (unsettled, deleted map[string]bool, err error) {
	unsettled = map[string]bool{}
	deleted = map[string]bool{}
	for _, action := range report.Actions {
		switch action.Kind {
		case ActionCreatedDir:
			rel := filepath.FromSlash(action.Path)
			goal := filepath.Join(r.cfg.Input, rel)
			if _, local := r.local[rel]; local {
				goal = filepath.Join(r.cfg.Output, rel)
			}
			if err := r.cfg.mkdirAll(goal, 0o755); err != nil {
				return nil, nil, fmt.Errorf("create directory %s: %w", action.Path, err)
			}
			r.cfg.logf("created directory: %s", action.Path)
		case ActionLinked, ActionImported, ActionRelinked:
			source, goal := r.installTargets(action)
			if err := r.cfg.mkdirAll(filepath.Dir(goal), 0o755); err != nil {
				return nil, nil, fmt.Errorf("create parent of %s: %w", action.Path, err)
			}
			if err := installLink(r.cfg, source, goal, action.Path); err != nil {
				return nil, nil, err
			}
		case ActionTrashedLocal:
			if err := r.trash(action.Path); err != nil {
				return nil, nil, err
			}
			deleted[filepath.FromSlash(action.Path)] = true
		case ActionRemovedDrive:
			rel := filepath.FromSlash(action.Path)
			if err := r.cfg.remove(filepath.Join(r.cfg.Output, rel)); err != nil {
				return nil, nil, fmt.Errorf("remove %s: %w", action.Path, err)
			}
			deleted[rel] = true
			r.cfg.logf("removed: %s", action.Path)
		case ActionConflict:
			if err := r.resolveConflict(action); err != nil {
				return nil, nil, err
			}
		default:
			// Report-only kinds (deferred, type-conflict) run again next
			// pass; their paths and ancestors must stay unpruned.
			r.cfg.logf("left for a later pass: %s: %s", action.Kind, action.Path)
			markUnsettled(unsettled, action.Path)
		}
	}
	return unsettled, deleted, nil
}

// resolveConflict applies one ActionConflict. Detail names the winning side;
// the losing file is first preserved as a hardlinked copy on both sides, then
// the winner's content is linked at the original path on the losing side, so
// both versions of the file survive the pass.
func (r *watchRun) resolveConflict(action Action) error {
	rel := filepath.FromSlash(action.Path)
	localPath, outPath := filepath.Join(r.cfg.Input, rel), filepath.Join(r.cfg.Output, rel)
	winnerPath, loserPath, loserSide := localPath, outPath, "drive"
	loserRoot, otherRoot := r.cfg.Output, r.cfg.Input
	if action.Detail == detailDriveWins {
		winnerPath, loserPath = outPath, localPath
		loserSide = "local"
		loserRoot, otherRoot = r.cfg.Input, r.cfg.Output
	}
	loserInfo, err := r.cfg.stat(loserPath)
	if err != nil {
		return fmt.Errorf("conflict %s: %w", action.Path, err)
	}
	copyRel, err := r.conflictCopyPath(action.Path, loserInfo, loserSide)
	if err != nil {
		return err
	}
	loserCopy := filepath.Join(loserRoot, filepath.FromSlash(copyRel))
	otherCopy := filepath.Join(otherRoot, filepath.FromSlash(copyRel))
	if err := installLink(r.cfg, loserPath, loserCopy, copyRel); err != nil {
		return err
	}
	if err := installLink(r.cfg, loserCopy, otherCopy, copyRel); err != nil {
		return err
	}
	if err := installLink(r.cfg, winnerPath, loserPath, action.Path); err != nil {
		return err
	}
	r.cfg.logf("kept conflict copy: %s", copyRel)
	return nil
}

// conflictCopyPath names the copy preserving a conflict's losing side: the
// full file name plus .sync-conflict-<side>-<loser mtime in UTC>, with -1,
// -2, ... appended after the timestamp while the name is taken on either
// side. The losing file's mtime keeps names deterministic (no clock read).
func (r *watchRun) conflictCopyPath(rel string, loser os.FileInfo, side string) (string, error) {
	base := rel + ".sync-conflict-" + side + "-" + loser.ModTime().UTC().Format("20060102T150405")
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		free, err := r.conflictNameFree(name)
		if err != nil {
			return "", err
		}
		if free {
			return name, nil
		}
	}
}

// conflictNameFree reports whether rel is absent on both sides. An lstat error
// other than not-exist aborts: a copy that might collide must not be created.
func (r *watchRun) conflictNameFree(rel string) (bool, error) {
	relPath := filepath.FromSlash(rel)
	for _, root := range []string{r.cfg.Input, r.cfg.Output} {
		_, err := r.cfg.lstat(filepath.Join(root, relPath))
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("check conflict copy %s: %w", rel, err)
		}
	}
	return true, nil
}

// trash moves the local entry for rel into the pass's Trash directory under
// its base name. A move that cannot land fails the pass: the survivor stays
// put and nothing is committed. The error names --trash-dir because the
// built-in ~/.Trash default may not exist (Linux, Windows).
func (r *watchRun) trash(rel string) error {
	target, err := r.trashTarget(rel)
	if err != nil {
		return fmt.Errorf("trash %s: %w (set --trash-dir to an existing writable directory)", rel, err)
	}
	source := filepath.Join(r.cfg.Input, filepath.FromSlash(rel))
	if err := r.cfg.rename(source, target); err != nil {
		return fmt.Errorf("trash %s: %w (set --trash-dir to an existing writable directory)", rel, err)
	}
	r.cfg.logf("moved to Trash: %s", rel)
	return nil
}

// trashTarget names a free path in the pass's Trash directory for rel,
// appending -1, -2, ... before the extension on collision. A failed collision
// check aborts instead of gambling on overwriting an entry it could not read.
func (r *watchRun) trashTarget(rel string) (string, error) {
	dir, err := r.trashDir()
	if err != nil {
		return "", err
	}
	base := filepath.Base(filepath.FromSlash(rel))
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		candidate := filepath.Join(dir, name)
		_, err := r.cfg.lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("check trash target %s: %w", candidate, err)
		}
	}
}

// trashDir resolves the configured Trash directory, defaulting to the user's
// ~/.Trash. It is deliberately not created: a move that cannot land there must
// fail the pass rather than delete the survivor.
func (r *watchRun) trashDir() (string, error) {
	if r.opts.TrashDir != "" {
		return r.opts.TrashDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Trash directory: %w", err)
	}
	return filepath.Join(home, ".Trash"), nil
}

// installTargets names the file an install copies from and the path that must
// receive it. The kind decides the direction, except a relink, where Detail
// names the side that changed and therefore wins.
func (r *watchRun) installTargets(action Action) (source, goal string) {
	rel := filepath.FromSlash(action.Path)
	source, goal = filepath.Join(r.cfg.Input, rel), filepath.Join(r.cfg.Output, rel)
	if action.Kind == ActionImported || (action.Kind == ActionRelinked && action.Detail == detailDriveWins) {
		source, goal = goal, source
	}
	return source, goal
}

// installLink places sourcePath at goalPath as a hardlink through a temporary
// sibling and a rename, so goalPath is never missing while the new entry is
// prepared. It is the upload install recipe, usable in either direction.
func installLink(cfg Config, sourcePath, goalPath, rel string) error {
	tmp, err := os.CreateTemp(filepath.Dir(goalPath), ".driveignore-*")
	if err != nil {
		return fmt.Errorf("install %s: %w", rel, err)
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName) // free the name for the link

	if err := cfg.link(sourcePath, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install %s: %w", rel, err)
	}
	if err := cfg.rename(tmpName, goalPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("install %s: %w", rel, err)
	}
	cfg.logf("created hard link: %s", rel)
	return nil
}

// commit refreshes and saves the journal after a clean apply. Paths the pass
// settled are re-statted so file anchors follow a repair and directory change
// stamps describe the committed state; deleted paths drop their anchor, while
// paths with an unresolved action keep their previous entries and block their
// parent directories from refreshing stamps, so the next pass walks them again
// instead of pruning the action away. An unobserved entry survives only under
// a pruned subtree; one whose path vanished on both sides is dropped, because
// a stale anchor could authorize deleting a later file that reuses its inode.
// The write is skipped when the refresh produced the journal already on disk.
func (r *watchRun) commit(statePath string, unsettled, deleted map[string]bool) error {
	fresh := r.refreshEntries(unsettled, deleted)
	if maps.Equal(fresh, r.journal.Entries) {
		return nil
	}
	r.journal.Entries = fresh
	return saveJournal(statePath, r.journal)
}

// markUnsettled records path and every ancestor directory, using the walk's
// path separator so the refresh can look them up directly.
func markUnsettled(unsettled map[string]bool, path string) {
	for rel := filepath.FromSlash(path); ; {
		unsettled[rel] = true
		parent := filepath.Dir(rel)
		if parent == rel || parent == "." {
			return
		}
		rel = parent
	}
}

// refreshEntries rebuilds the journal from the post-apply state. An observed
// path keeps its previous anchor unless this pass deleted it, so a directory
// awaiting deletion keeps the proof the next pass needs, while an applied
// deletion cannot leave a stale inode behind. Settle rebuilds synced paths
// from current stats; paths under a pruned subtree carry over untouched.
func (r *watchRun) refreshEntries(unsettled, deleted map[string]bool) map[string]journalEntry {
	fresh := make(map[string]journalEntry, len(r.journal.Entries)+len(r.local))
	for rel, previous := range r.journal.Entries {
		if r.underPruned(rel) || (r.observed(rel) && !deleted[rel]) {
			fresh[rel] = previous
		}
	}
	for rel := range r.local {
		r.settle(fresh, unsettled, rel)
	}
	for rel := range r.out {
		if _, both := r.local[rel]; both {
			continue
		}
		r.settle(fresh, unsettled, rel)
	}
	return fresh
}

func (r *watchRun) observed(rel string) bool {
	if _, ok := r.local[rel]; ok {
		return true
	}
	_, ok := r.out[rel]
	return ok
}

// settle overwrites rel's entry with fresh stat data when the path is synced
// and its directory may be pruned. A directory holding an unsettled action
// keeps its previous stamps so the next pass walks it and re-reports.
func (r *watchRun) settle(fresh map[string]journalEntry, unsettled map[string]bool, rel string) {
	entry, ok := r.currentEntry(rel)
	if !ok {
		return
	}
	if entry.Type == "dir" && unsettled[rel] {
		return
	}
	fresh[rel] = entry
}

// underPruned reports whether rel itself or one of its ancestors was pruned
// by the scan: a path that was not walked cannot have changed, so its
// previous entry carries over.
func (r *watchRun) underPruned(rel string) bool {
	for {
		if r.pruned[rel] {
			return true
		}
		parent := filepath.Dir(rel)
		if parent == rel || parent == "." {
			return false
		}
		rel = parent
	}
}

// currentEntry re-stats both sides of rel and describes the journal entry a
// synced path deserves now. It reports false when the path is not proven
// synced: missing on either side, different files of the same kind, or an
// unreadable stat.
func (r *watchRun) currentEntry(rel string) (journalEntry, bool) {
	relPath := filepath.FromSlash(rel)
	localPath := filepath.Join(r.cfg.Input, relPath)
	outPath := filepath.Join(r.cfg.Output, relPath)
	localInfo, err := r.cfg.stat(localPath)
	if err != nil {
		return journalEntry{}, false
	}
	outInfo, err := r.cfg.stat(outPath)
	if err != nil {
		return journalEntry{}, false
	}
	if localInfo.IsDir() != outInfo.IsDir() {
		return journalEntry{}, false
	}
	if localInfo.IsDir() {
		localStamp, err := dirChangeStamp(localPath)
		if err != nil {
			return journalEntry{}, false
		}
		outStamp, err := dirChangeStamp(outPath)
		if err != nil {
			return journalEntry{}, false
		}
		return journalEntry{Type: "dir", LocalStamp: localStamp, OutStamp: outStamp}, true
	}
	if !os.SameFile(localInfo, outInfo) {
		return journalEntry{}, false
	}
	inode, err := fileInode(localPath)
	if err != nil {
		return journalEntry{}, false
	}
	return journalEntry{Type: "file", Inode: inode, Size: localInfo.Size(), ModTime: localInfo.ModTime().UnixNano()}, true
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

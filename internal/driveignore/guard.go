package driveignore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Guard action kinds extend the shared action vocabulary: a guard pass reports
// the paths it stamped and unstamped.
const (
	ActionSealed   ActionKind = "sealed"
	ActionUnsealed ActionKind = "unsealed"
)

// GuardOptions configures one guard pass. DryRun classifies without changing
// attributes or the in-memory walk state.
type GuardOptions struct {
	DryRun bool
}

// Guard keeps the .driveignore rules of one folder inside My Drive enforced by
// stamping matching paths with the File Provider ignore attribute, so Google
// Drive for desktop keeps them on disk but out of the cloud. The guarded
// folder is Config.Input; Config.Output is unused. A Guard is not safe for
// concurrent use.
type Guard struct {
	cfg  Config
	root string

	matcher Matcher
	// rulesSource and rulesBytes remember which rule file built the matcher
	// and its exact content; a changed file rebuilds the matcher and drops
	// every stamp so the next pass walks the whole tree again.
	rulesSource string
	rulesBytes  []byte

	// stamps maps visited directories to the change stamp the walk saw, so an
	// unchanged directory can be skipped.
	stamps map[string]int64
	// sealed records the paths known to carry the stamp, so a stamp removed
	// outside the guard is restored even when its path did not change.
	sealed map[string]bool // rel -> isDir
}

// NewGuard loads the rules of the guarded folder: its own .driveignore first,
// the global ignore file as the fallback. No rules anywhere is an error.
func NewGuard(cfg Config) (*Guard, error) {
	g := &Guard{
		cfg:    cfg,
		root:   cfg.Input,
		stamps: map[string]int64{},
		sealed: map[string]bool{},
	}
	if err := g.refreshRules(); err != nil {
		return nil, err
	}
	return g, nil
}

// Pass walks the guarded folder and brings every visited path to its rule
// state: matching paths are stamped, paths whose rules were removed are
// unstamped, and stamps lost on sealed paths are restored. The report lists
// every change; actions are ordered as the walk visited the paths, with the
// restored stamps sorted by path after them.
func (g *Guard) Pass(opts GuardOptions) (Report, error) {
	if err := g.refreshRules(); err != nil {
		return Report{}, err
	}

	var actions []Action
	visited := map[string]int64{}
	walkErr := Walk(g.root, func(path string, entry fs.DirEntry, rel string) error {
		g.cfg.tick(rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		slashed := filepath.ToSlash(rel)

		if entry.IsDir() {
			stamp, stampErr := dirChangeStamp(path)
			if stampErr == nil && stamp != 0 {
				if previous, ok := g.stamps[rel]; ok && previous == stamp {
					visited[rel] = stamp
					return filepath.SkipDir
				}
				visited[rel] = stamp
			}
			matched := g.matcher.Match(path, true)
			sealed, err := g.cfg.sealed(path)
			if err != nil {
				return err
			}
			switch {
			case matched && !sealed:
				actions = append(actions, Action{Kind: ActionSealed, Path: slashed})
				if !opts.DryRun {
					if err := g.cfg.seal(path); err != nil {
						return err
					}
					g.sealed[rel] = true
				}
			case matched:
				if !opts.DryRun {
					g.sealed[rel] = true
				}
			case sealed:
				actions = append(actions, Action{Kind: ActionUnsealed, Path: slashed})
				if !opts.DryRun {
					if err := g.cfg.unseal(path); err != nil {
						return err
					}
					delete(g.sealed, rel)
				}
			}
			if matched {
				// A sealed directory covers its whole subtree, so children are
				// never stamped individually. That matches git's rule that a
				// path inside an excluded directory cannot be re-included.
				return filepath.SkipDir
			}
			return nil
		}

		if entry.Name() == ".driveignore" || isGoogleStub(entry.Name()) {
			return nil
		}
		matched := g.matcher.Match(path, false)
		sealed, err := g.cfg.sealed(path)
		if err != nil {
			return err
		}
		switch {
		case matched && !sealed:
			actions = append(actions, Action{Kind: ActionSealed, Path: slashed})
			if !opts.DryRun {
				if err := g.cfg.seal(path); err != nil {
					return err
				}
				g.sealed[rel] = false
			}
		case !matched && sealed:
			actions = append(actions, Action{Kind: ActionUnsealed, Path: slashed})
			if !opts.DryRun {
				if err := g.cfg.unseal(path); err != nil {
					return err
				}
				delete(g.sealed, rel)
			}
		case matched && sealed && !opts.DryRun:
			g.sealed[rel] = false
		}
		return nil
	})
	if walkErr != nil {
		return Report{Actions: actions}, walkErr
	}
	if opts.DryRun {
		return Report{Actions: actions}, nil
	}

	for rel, stamp := range visited {
		g.stamps[rel] = stamp
	}
	return g.restoreLostStamps(actions)
}

// restoreLostStamps stamps back the sealed paths whose attribute disappeared
// outside the guard. A vanished stamp never weakens the rules: while the path
// still matches them it is restored, otherwise it is forgotten.
func (g *Guard) restoreLostStamps(actions []Action) (Report, error) {
	var restored []Action
	for rel, isDir := range g.sealed {
		path := filepath.Join(g.root, rel)
		sealed, err := g.cfg.sealed(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				delete(g.sealed, rel)
				continue
			}
			return Report{Actions: actions}, err
		}
		if sealed {
			continue
		}
		if !g.matcher.Match(path, isDir) {
			delete(g.sealed, rel)
			continue
		}
		if err := g.cfg.seal(path); err != nil {
			return Report{Actions: actions}, err
		}
		restored = append(restored, Action{Kind: ActionSealed, Path: filepath.ToSlash(rel)})
	}
	sort.Slice(restored, func(i, j int) bool { return restored[i].Path < restored[j].Path })
	return Report{Actions: append(actions, restored...)}, nil
}

// refreshRules loads the rule file when it changed. Content, not timestamps,
// is compared, so an edit within one clock tick is never missed.
func (g *Guard) refreshRules() error {
	source, data, err := readRuleContent(filepath.Join(g.root, ".driveignore"))
	if err != nil {
		return err
	}
	if source == "" {
		globalPath, err := g.cfg.globalPath()
		if err != nil {
			return err
		}
		source, data, err = readRuleContent(globalPath)
		if err != nil {
			return err
		}
		if source == "" {
			return fmt.Errorf("no .driveignore found in %s or at %s", g.root, globalPath)
		}
	}
	if source == g.rulesSource && bytes.Equal(data, g.rulesBytes) {
		return nil
	}
	g.matcher = pathMatcher{patterns: parsePatterns(data, nil), root: g.root}
	g.rulesSource, g.rulesBytes = source, data
	g.stamps = map[string]int64{}
	return nil
}

// readRuleContent reads one rule file. A missing file is not an error: the
// returned source is empty so the caller can try the next location.
func readRuleContent(path string) (string, []byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return path, data, nil
}

// GuardRootHash returns the stable artifact key for one guarded folder,
// canonicalized the same way pair keys are so spellings of a directory agree.
func GuardRootHash(root string) (string, error) {
	canonical, err := canonicalPairRoot(root)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:12], nil
}

// GuardLockPath returns the lock file for one guarded folder, next to the
// watch artifacts under the user's config directory.
func GuardLockPath(root string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	hash, err := GuardRootHash(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "driveignore", "guard-"+hash+".lock"), nil
}

// seal stamps path with the ignore attribute, through the injected effect when
// one is present.
func (c Config) seal(path string) error {
	if c.setSealFn != nil {
		return c.setSealFn(path)
	}
	return setIgnoreAttr(path)
}

// unseal removes the ignore attribute from path.
func (c Config) unseal(path string) error {
	if c.clearSealFn != nil {
		return c.clearSealFn(path)
	}
	return removeIgnoreAttr(path)
}

// sealed reports whether path carries the ignore attribute.
func (c Config) sealed(path string) (bool, error) {
	if c.hasSealFn != nil {
		return c.hasSealFn(path)
	}
	return hasIgnoreAttr(path)
}

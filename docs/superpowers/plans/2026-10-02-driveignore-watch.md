# driveignore watch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `driveignore watch` — continuous two-way, ignore-aware reconciliation between a local source tree and its Drive folder, with recoverable, journal-anchored deletions.

**Architecture:** A poll loop in the command layer drives a single-pass `Reconcile` in the core package. A per-pair JSON journal records paths proven synced (inode/mtime/size) so one-sided paths are classified as creations or deletions without guessing; the journaled inode anchors which side of a broken link changed. Mutations are hardlink installs, Trash moves, removals and conflict copies; the journal commits atomically only after a clean pass. No Google Drive API: Drive for desktop stays the cloud engine.

**Tech Stack:** Go 1.27, cobra, go-git ignore matcher, testify, launchd plist from a pure builder, flock (unix) / `LockFileEx` (windows, golang.org/x/sys).

**Spec:** https://github.com/ranokay/driveignore/issues/37

## Global Constraints

- Hardlink mode only. `--copy` and cross-filesystem pairs must error; no silent degradation.
- Journal written temp-file + rename, only after a complete pass. Missing, corrupt or unusable state = empty-journal semantics for that pass: one-sided paths are creations, deletions are impossible.
- A path is deleted from one side only when the journal proved it synced and the scan finds it missing on exactly one side. The file anchor is: inode and type match, surviving mtime not older than recorded; size is deliberately not required (in-place edits change size while remaining the same file). Ambiguity routes to creation or conflict, never deletion.
- Local deletions move to the user's Trash; if trashing is impossible, the pass fails closed (error, no commit).
- Ignore rules apply in both directions; ignored paths are never journaled and never acted on.
- `watch` runs cross-platform in the foreground; `--install`/`--uninstall` are macOS-only.
- Churn guard: entries modified within the last 2 seconds are deferred.
- Watch interval: base 2s, maximum 60s, doubling while idle or after errors.
- Scan pruning uses a per-directory change stamp: ctime on unix. Windows reports 0, which means always walk, because NTFS updates directory timestamps lazily (a CI runner reproduced this). 0/unknown always walks; remote-side changes move ctime without moving mtime (Task 2 finding).
- No Google Drive API, no content hashing.
- Every task ends with `mise run check` green and a conventional commit scoped to the package; only the listed paths are staged.

## Review Focus

1. Unicode, spaces and deep paths on either side — must converge like the rest of the tool (test lands in Task 5).
2. A file still being written when a pass scans it — must defer, never link mid-write (Task 7).
3. Name collision when moving a file to Trash — must never overwrite an existing trashed file (Task 6).
4. Inode-anchor false positive: the same path recreated with a different inode or a backdated mtime — must be treated as a creation, never as a deletion (Task 6).
5. Crash between mutation and journal commit — the next pass must converge with nothing lost (Task 5).

---

### Task 1: Setup — land progress reporting, keep both trees content-identical

**Files:**
- Commit (already edited, uncommitted): `README.md`, `cmd/clean.go`, `cmd/diff.go`, `cmd/options.go`, `cmd/unify.go`, `cmd/upload.go`, `internal/driveignore/driveignore_test.go`, `internal/driveignore/operations.go`
- Add: `cmd/progress.go`, `cmd/progress_test.go` (exist only in this clone), `docs/superpowers/plans/2026-10-02-driveignore-watch.md` (this plan)

- [ ] **Step 1: Create the working branch**

```bash
git switch -c watch-mode
```

- [ ] **Step 2: Copy the two new files into the mirror source** so the next `unify` cannot clean them out of the Drive tree

```bash
cp cmd/progress.go cmd/progress_test.go "$HOME/Downloads/workspaces/driveignore/cmd/"
diff cmd/progress.go "$HOME/Downloads/workspaces/driveignore/cmd/progress.go"
diff cmd/progress_test.go "$HOME/Downloads/workspaces/driveignore/cmd/progress_test.go"
```

Expected: no diff output.

- [ ] **Step 3: Verify the suite.** Run: `mise run check`. Expected: fmt, vet, lint (0 issues) and tests pass.

- [ ] **Step 4: Commit**

```bash
git add README.md cmd/clean.go cmd/diff.go cmd/options.go cmd/unify.go cmd/upload.go cmd/progress.go cmd/progress_test.go internal/driveignore/driveignore_test.go internal/driveignore/operations.go docs/superpowers/plans/2026-10-02-driveignore-watch.md
git commit -m "feat(cmd): report progress during long walks"
```

From here on, work in this clone (`My Drive/workspaces/driveignore`). The local clone shares every edited file's inode, so trees stay identical for modified files; whenever a task creates a new file, copy it to `~/Downloads/workspaces/driveignore` the same way (a mismatch would be cleaned from Drive by the next `unify`).

---

### Task 2: Characterize the Drive mount (manual)

**Files:** none in the repo. Deliverable: a findings comment on issue #37.

This runs against the real mount with the browser driving drive.google.com as "another device". Do not start any reconciler work against the real pair before this is recorded.

- [ ] **Step 1: Create a sandbox** `My Drive/.driveignore-watch-sandbox/` containing `remote.txt` (any content) and `incoming/`.

- [ ] **Step 2: Reverse hardlink check.** From a scratch local dir (e.g. `~/Downloads/workspaces/temp`), run `ln "$HOME/Google Drive/My Drive/.driveignore-watch-sandbox/remote.txt" ./watch-link.txt`, then `ls -li` both and `cat ./watch-link.txt`. Record: does the link succeed, same inode, content readable?

- [ ] **Step 3: Remote replace semantics.** Edit `remote.txt` in the Drive web UI to new content and save. Poll the local mount. Record: does the local file update in place (inode unchanged) or get replaced (new inode)? How long does propagation take?

- [ ] **Step 4: Directory mtime propagation.** From the web UI, add and then delete a file inside `incoming/`. Record whether the local `incoming/` directory mtime changes for each operation.

- [ ] **Step 5: Cloud-only materialization.** From the web UI, create `incoming/cloud-only.txt` with content. Without opening it locally, `ls -l` it and try `ln` it to a local scratch path, then read it. Record: do sizes look real, does linking trigger materialization, is the content correct?

- [ ] **Step 6: Temp artifacts.** While uploading a larger file through the mount (e.g. copy a few hundred MB local file into the sandbox), watch the sandbox with `ls -la` in a loop. Record any client temp/sidecar names.

- [ ] **Step 7: Record and clean up.** Post the findings as a comment on issue #37, then delete `.driveignore-watch-sandbox/`. If any finding contradicts a spec assumption (for example reverse linking not working), STOP and update the spec before continuing.

---

### Task 3: State journal

**Files:**
- Create: `internal/driveignore/journal.go`
- Test: `internal/driveignore/journal_test.go`

**Interfaces:**
- Consumes: `os.UserConfigDir` (as `GlobalIgnorePath` already uses).
- Produces:
  - `type journalEntry struct { Type string; Inode uint64; Size int64; ModTime int64; LocalStamp int64; OutStamp int64 }` (types `"file"`/`"dir"`; times and stamps are unix nanos; files use `Inode`/`Size`/`ModTime` (shared mtime), dirs use `LocalStamp`/`OutStamp` (change stamps, 0 = unknown → always walk))
  - `type journal struct { Version int; Input string; Output string; Entries map[string]journalEntry }`
  - `func loadJournal(path string) (*journal, bool)` — returns an empty journal and `false` when missing, unreadable, corrupt, wrong version, or Input/Output mismatch; never returns a "partially usable" journal
  - `func saveJournal(path string, j *journal) error` — `MkdirAll` + temp file (prefix `.driveignore-watch-`) + rename
  - `func WatchPairHash(input, output string) (string, error)` — `sha256(canonicalInput+"\x00"+canonicalOutput)[:12]`, where each root is absolute with symlinks resolved (`filepath.EvalSymlinks`); a root that cannot be resolved (gone when `--uninstall` runs) keeps its absolute spelling
  - `func WatchStatePath(input, output string) (string, error)` — `UserConfigDir/driveignore/watch-<WatchPairHash>.json`
  - `func WatchLockPath(input, output string) (string, error)` — same hash, `.lock`

- [ ] **Step 1: Write the failing tests**

```go
func TestJournalRoundTripAndAtomicWrite(t *testing.T)
// save→load preserves entries; after save the parent dir contains no `.driveignore-watch-` leftovers.

func TestJournalMissingCorruptAndMismatchedAreUnusable(t *testing.T)
// missing path → empty + false; garbage bytes → empty + false; version 99 → false; Input/Output naming a different pair → false.

func TestWatchPathsAreStablePerPair(t *testing.T)
// same pair twice → equal paths; different input or output → different paths; both under the driveignore config dir.
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./internal/driveignore -run 'TestJournal|TestWatchPaths' -v`. Expected: build failure, `undefined: loadJournal` (and friends).

- [ ] **Step 3: Implement `journal.go`.** Straightforward JSON codec; `loadJournal` collapses every failure into `(empty, false)`; `saveJournal` rewrites atomically. Comment why: the journal is the deletion proof, so no partial state may ever be read back.

- [ ] **Step 4: Run tests to verify they pass.** Run: `go test ./internal/driveignore -run 'TestJournal|TestWatchPaths' -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driveignore/journal.go internal/driveignore/journal_test.go
git commit -m "feat(driveignore): add watch state journal"
```

---

### Task 4: Scan and decide (dry-run report)

**Files:**
- Create: `internal/driveignore/watch.go`, `internal/driveignore/stamp_darwin.go`, `internal/driveignore/stamp_linux.go`, `internal/driveignore/stamp_windows.go`, `internal/driveignore/stamp_other.go` (`//go:build !darwin && !linux && !windows`; returns 0 = always walk)
- Test: `internal/driveignore/watch_test.go`, `internal/driveignore/stamp_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Consumes: Task 3's `journal`, `loadJournal`; existing `Config.rules()`, `Walk`, injected fs funcs.
- Produces (all exported from `internal/driveignore`):
  - `type WatchOptions struct { DryRun bool; OneWay bool; MinAge time.Duration; TrashDir string }` (`MinAge == 0` → `defaultChurnAge = 2 * time.Second`; `TrashDir == ""` → `~/.Trash`)
  - `type ActionKind string` with constants `ActionCreatedDir "created-dir"`, `ActionLinked "linked"`, `ActionRelinked "relinked"`, `ActionImported "imported"`, `ActionTrashedLocal "trashed-local"`, `ActionRemovedDrive "removed-drive"`, `ActionConflict "conflict"`, `ActionDeferred "deferred"`, `ActionTypeConflict "type-conflict"`
  - `type Action struct { Kind ActionKind; Path string; Detail string }`
  - `type Report struct { Actions []Action }`
  - `func Reconcile(cfg Config, statePath string, opts WatchOptions) (Report, error)` — Task 4 serves `DryRun: true`; for `!DryRun` it temporarily returns `errApplyNotImplemented` (removed in Task 5)
  - `func dirChangeStamp(path string) (int64, error)` — unix: directory ctime in unix nanos; windows: always 0 (NTFS directory timestamps update lazily), meaning always walk; 0 means unknown (always walk)

**Decision rules this task must implement (the table from the spec):** both sides same inode → nothing; both sides different inodes → anchor side unchanged, changed side wins → `ActionRelinked` (`Detail` `"local wins"` or `"drive wins"`); neither anchor → `ActionConflict`; local-only journaled with matching inode and surviving mtime not older than recorded (file) → `ActionTrashedLocal` (the Drive copy was deleted; trash the local survivor); local-only otherwise (no entry, different inode, or an older survivor) → `ActionLinked`; drive-only journaled with matching inode and surviving mtime not older than recorded (file) → `ActionRemovedDrive` (the local copy was deleted; remove the Drive entry); drive-only otherwise → `ActionImported`; missing dirs are created (either side) → `ActionCreatedDir`; journaled dir missing on the Drive side and empty locally → `ActionTrashedLocal`, missing locally and empty on Drive → `ActionRemovedDrive`; type mismatch → `ActionTypeConflict` (report-only; descendants of the mismatched path are suppressed until a human resolves it, because installing through a path that is a file on the other side would fail the pass); entries modified within `MinAge` → `ActionDeferred`. `OneWay` flips all drive-wins/anchor logic to local wins and turns drive-only (journaled or not) into `ActionRemovedDrive`. Built-in exclusions apply before `.driveignore` rules: the Google-native stub family (`*.gdoc`, `*.gsheet`, `*.gslides`, `*.gdraw`, `*.gshortcut`); Task 2 observed no client temp artifacts inside the mount, so nothing else is added. Excluded paths are never journaled or acted on. Scanning is pruned by change stamps: descend into a directory when either side's `dirChangeStamp` differs from its journaled stamp, the journal lacks the entry, or a stamp is 0 (unknown). Never compare directory size, nlink or mode across sides — Task 2 found them synthetic for remote-origin dirs.

**Test helpers to add in `watch_test.go`** (used by later tasks too):
- `syncedPair(t) (src, out, state string)` — twin temp trees whose files are hardlinked, plus a saved journal with correct entries and dir change stamps.
- `watchOpts(t) WatchOptions` — `MinAge: time.Nanosecond`, `TrashDir: filepath.Join(t.TempDir(), "Trash")`.
- `tickCollector(cfg *Config) *[]string` — assigns `cfg.Progress`.
- `replaceFile(t, path, content string)` — writes through a temp name + rename so the inode changes (simulates an editor atomic save / Drive client replace).

- [ ] **Step 1: Write the failing tests** — one table-driven test over the decision table plus targeted tests:

```go
func TestReconcileDryRunClassifiesEveryDecisionRow(t *testing.T)
// table rows: same inode, local anchor, drive anchor, neither anchor (conflict),
// local-only new/journaled/anchor-mismatch/backdated-survivor, drive-only new/journaled/anchor-mismatch/backdated-survivor,
// both-missing, type mismatch, empty-dir removal; assert Action kinds and Details.

func TestReconcileDryRunNeverWrites(t *testing.T)
// after a dry run the state file is unchanged and no files changed on either side.

func TestReconcileOneWayIsLocalAuthoritative(t *testing.T)
// drive-only new file → ActionRemovedDrive; local-only journaled → ActionLinked; drive anchor → "local wins".

func TestReconcilePrunesUntouchedSubtrees(t *testing.T)
// tick collector: a touched subtree's path appears, an untouched subtree's paths do not, and a subtree whose stamps are 0 is always walked.

func TestReconcileSkipsIgnoredAndSymlinks(t *testing.T)
// .driveignore matches, Google-native stubs and symlinks on either side produce no actions and no journal entries.
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./internal/driveignore -run TestReconcile -v`. Expected: build failure, `undefined: Reconcile`.

- [ ] **Step 3: Implement `watch.go`** with the scan (`Walk` over each side, prune by change stamps from the journal), the classification above, and report assembly. Order actions: creations ascending by path, deletions deepest-first. Tick `cfg.Progress` for every walked entry (the existing `Config.tick` seam); pruned subtrees produce no ticks. The journal is loaded once; unusable state logs through `cfg.Log` and behaves as empty.

- [ ] **Step 4: Run tests to verify they pass.** Run: `go test ./internal/driveignore -run TestReconcile -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driveignore/watch.go internal/driveignore/watch_test.go
git commit -m "feat(driveignore): classify watch decisions into a dry-run report"
```

---

### Task 5: Apply creations, repairs and imports; commit the journal

**Files:**
- Modify: `internal/driveignore/watch.go`, `internal/driveignore/watch_test.go`

**Interfaces:**
- Consumes: Task 4's exported types and `Reconcile`.
- Produces: `Reconcile` with `!DryRun` fully implemented — applies the report, then commits the journal; on any action error returns the error **before** saving state.

Implementation notes: installs reuse the existing temp-link + rename recipe (`installFile`-style) in either direction; new directories via `MkdirAll` both ways; the journal is refreshed after mutations (stat again, so dir mtimes reflect the pass) and entries for paths gone on both sides are dropped; deferred paths keep their previous entries untouched.

- [ ] **Step 1: Write the failing tests**

```go
func TestReconcileLinksAndCommitsState(t *testing.T)
// new local file and new drive file converge (same inode both sides); journal records both; second pass reports zero actions.

func TestReconcileRepairsBrokenLinksInBothDirections(t *testing.T)
// replaceFile on the local side → drive adopts local content ("local wins"); replaceFile on drive → local adopts drive content.

func TestReconcileCreatesEmptyDirsAndHandlesUnicodeDeepPaths(t *testing.T)
// "ünï code/深/very/deep/path file.txt" converges both ways (Review Focus 1).

func TestReconcileAbortsBeforeCommitOnFailure(t *testing.T)
// cfg.linkFn fails on the second install: Reconcile errors, state file byte-identical, next clean pass converges (Review Focus 5).
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./internal/driveignore -run 'TestReconcileLinks|TestReconcileRepairs|TestReconcileCreates|TestReconcileAborts' -v`. Expected: FAIL (`errApplyNotImplemented` or missing behavior).

- [ ] **Step 3: Implement apply + commit** in `watch.go`. Deletion kinds in the report are ignored until Task 6 (they cannot occur yet because tests seed no such state); `errApplyNotImplemented` is deleted.

- [ ] **Step 4: Run tests to verify they pass.** Run: `go test ./internal/driveignore -run TestReconcile -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driveignore/watch.go internal/driveignore/watch_test.go
git commit -m "feat(driveignore): apply watch creations, repairs and imports"
```

---

### Task 6: Deletions with the safety invariants and Trash

**Files:**
- Modify: `internal/driveignore/operations.go` (add `removeFn` to `Config`, default `os.Remove`), `internal/driveignore/watch.go`, `internal/driveignore/watch_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces: `Config.removeFn func(string) error` (nil → `os.Remove`); deletion behavior per the spec.

Rules to implement exactly (apply executes the report; classification already proved the anchors — do not re-classify):
- `trashed-local`: move the file to `TrashDir` under its base name; on collision append `-1`, `-2`, …; if the move fails, return the error (fail closed).
- `removed-drive`: `removeFn(drivePath)`.
- Directories: journaled + missing on one side + empty on the surviving side → remove the empty dir (deepest first). No inode anchor for dirs: an empty dir has nothing to lose.
- The anchor checks (journal entry, inode, surviving mtime not older than recorded) live in Task 4 classification.

- [ ] **Step 1: Write the failing tests**

```go
func TestReconcileDeletesBothDirectionsFromJournalProof(t *testing.T)
// local delete → drive entry removed; drive delete → local file moved to TrashDir.

func TestReconcileNeverDeletesWithoutMatchingAnchor(t *testing.T)
// drive-only with a different inode → imported, not removed; surviving file chtimes'd older than recorded → creation route (Review Focus 4).

func TestReconcileTrashCollisionDoesNotOverwrite(t *testing.T)
// TrashDir already holds "same.txt": the second one lands as "same-1.txt" (Review Focus 3).

func TestReconcileDeletionFailsClosedWhenTrashIsImpossible(t *testing.T)
// TrashDir unwritable/missing: error returned, state not committed, both files still present.

func TestReconcileOneWayPropagatesLocalDeletesOnly(t *testing.T)
// one-way: local delete removes from drive; local-only journaled is re-linked to drive, not trashed locally.
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./internal/driveignore -run 'TestReconcileDelet|TestReconcileNeverDeletes|TestReconcileTrash|TestReconcileOneWay' -v`. Expected: FAIL.

- [ ] **Step 3: Implement deletions** in `watch.go` and `removeFn` in `operations.go`.

- [ ] **Step 4: Run tests to verify they pass.** Run: `go test ./internal/driveignore -run TestReconcile -v`. Expected: PASS; whole package green.

- [ ] **Step 5: Commit**

```bash
git add internal/driveignore/operations.go internal/driveignore/watch.go internal/driveignore/watch_test.go
git commit -m "feat(driveignore): propagate watch deletions with journal proof and Trash"
```

---

### Task 7: Conflicts, churn guard and type mismatches

**Files:**
- Modify: `internal/driveignore/watch.go`, `internal/driveignore/watch_test.go`

**Interfaces:**
- Produces: conflict copies named `<full file name>.sync-conflict-<side>-<YYYYMMDDTHHMMSS>` (`side` is `local` or `drive`; e.g. `foo.txt.sync-conflict-local-20261002T120000`), created on **both** sides as hardlinks of the losing file, before the winning content is relinked at the original path; on collision append `-1`, `-2`, … after the timestamp; type mismatches and deferrals are report-only.

- [ ] **Step 1: Write the failing tests**

```go
func TestReconcileKeepsBothSidesWhenNeitherAnchorMatches(t *testing.T)
// both sides replaced: newer mtime wins at the path; both sides carry the conflict copy; second pass is a no-op.

func TestReconcileConflictCopyNameCollisionGetsSuffix(t *testing.T)

func TestReconcileDefersFreshlyModifiedEntries(t *testing.T)
// fresh mtime → ActionDeferred, no mutation, journal untouched; after Chtimes into the past the next pass converges (Review Focus 2).

func TestReconcileLeavesTypeMismatchAloneAndRepeatsIt(t *testing.T)
// file vs dir: ActionTypeConflict, no mutation, identical report on the next pass.
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./internal/driveignore -run 'TestReconcileKeeps|TestReconcileConflictCopy|TestReconcileDefers|TestReconcileLeaves' -v`. Expected: FAIL.

- [ ] **Step 3: Implement** conflict handling, the `MinAge` deferral check and the type-mismatch report in `watch.go`.

- [ ] **Step 4: Run tests to verify they pass.** Run: `go test ./internal/driveignore -v`. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/driveignore/watch.go internal/driveignore/watch_test.go
git commit -m "feat(driveignore): keep conflicts, defer churn, report type mismatches"
```

---

### Task 8: Pair lock, shared with the mutating commands

**Files:**
- Create: `internal/driveignore/lock.go`, `internal/driveignore/lock_unix.go` (`//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd`), `internal/driveignore/lock_windows.go` (`//go:build windows`), `internal/driveignore/lock_other.go` (`//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows`; returns a clear "pair lock unsupported" error so those GOOSes still compile, mirroring `stamp_other.go`)
- Test: `internal/driveignore/lock_test.go`, `cmd/watch_lock_test.go`
- Modify: `cmd/upload.go`, `cmd/unify.go`, `cmd/clean.go`, `go.mod`, `go.sum`

**Interfaces:**
- Produces: `func AcquireLock(path string) (release func() error, err error)` — takes an exclusive non-blocking lock; when the lock is held, err says which pair is busy; release unlocks and closes; process death releases automatically.
- Consumed by: `watch` (Task 9) and the three mutating commands below.

- [ ] **Step 1: Write the failing tests**

```go
func TestAcquireLockExcludesSecondHolder(t *testing.T)
// first acquire succeeds; second errors; after release the second succeeds.

func TestMutatingCommandsRefuseWhilePairLocked(t *testing.T) // cmd package
// acquire WatchLockPath(input, output) directly, then `execute()` upload/unify/clean → exit 1, stderr mentions the lock path.
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./internal/driveignore ./cmd -run 'TestAcquireLock|TestMutatingCommandsRefuse' -v`. Expected: build failure.

- [ ] **Step 3: Implement.** `lock.go` opens/creates the file `0o600` and calls `tryLock`; unix file uses `syscall.Flock(LOCK_EX|LOCK_NB)`; windows file uses `golang.org/x/sys/windows.LockFileEx` (run `go mod tidy` — `golang.org/x/sys` becomes a direct require). In each mutating command, acquire `WatchLockPath(input, args[0])` after `newOptions` and defer release.

- [ ] **Step 4: Run tests to verify they pass.** Run: `go test ./internal/driveignore ./cmd -v`. Expected: PASS. Also compile for windows: `GOOS=windows go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/driveignore/lock.go internal/driveignore/lock_unix.go internal/driveignore/lock_windows.go internal/driveignore/lock_test.go cmd/watch_lock_test.go cmd/upload.go cmd/unify.go cmd/clean.go go.mod go.sum
git commit -m "feat(driveignore): serialize mutating runs with a per-pair lock"
```

---

### Task 9: The `watch` command

**Files:**
- Create: `cmd/watch.go`, `cmd/watch_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: `Reconcile`, `WatchOptions`, `Report`, `Action`, `WatchStatePath`, `WatchLockPath`, `AcquireLock`, `newOptions`, the progress reporter.
- Produces: `driveignore watch [drive folder] [-i source]` with `--once`, `--dry-run`, `--one-way`, `--trash-dir <dir>` (where local deletions move; default `~/.Trash`, and a trash failure names this flag), `--interval <duration>` (default 2s, max 60s, values ≤ 0 are usage errors); `runWatchPass` and `nextWatchInterval(prev, base time.Duration, hadActions, failed bool) time.Duration` (base is the `--interval` value, default 2s; actions reset to base; idle doubles up to 60s; failures double up to 60s).

Behavior pinned for tests: before the first pass, watch probes hardlink support between the two trees (creates a hidden probe file on each side, hardlinks across, removes both) and fails with a clear message that watch requires one filesystem and `--copy` is unsupported. `--dry-run` prints `would <kind>: <path>` for every action (plus ` (<detail>)` when non-empty) and a final `would apply N action(s)`; real passes print `<kind>: <path>` only for `trashed-local`, `removed-drive`, `conflict`, `type-conflict`, `deferred` unless `--verbose`, plus a summary `pass: N action(s), K deferred in Xs` when N > 0; a zero-action pass prints nothing. `--once` exits non-zero on pass failure; loop mode logs errors and backs off. The pair lock is held for the process lifetime; the state path comes from `WatchStatePath`.

- [ ] **Step 1: Write the failing tests**

```go
func TestNextWatchInterval(t *testing.T)
// base→idle doubles; capped at 60s; action resets; failure doubles.

func TestWatchOnceDryRunReportsWithoutChanges(t *testing.T) // execute() seam
// temp pair, new local file: stdout lists `would linked: <path>` and the summary; files and state unchanged.

func TestWatchOnceAppliesAndIsIdempotent(t *testing.T)
// first `--once` links; second `--once` prints nothing and exits 0.

func TestWatchRefusesWhenPairLocked(t *testing.T)

func TestWatchRejectsPairWithoutHardlinks(t *testing.T)
// output dir without write permission → hardlink probe fails, exit 1, message names filesystem support.

func TestWatchIntervalMustBePositive(t *testing.T)
// --interval 0 → exit 2 usage error.
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./cmd -run TestWatch -v`. Expected: build failure.

- [ ] **Step 3: Implement `cmd/watch.go`**: flags, lock, state path, `runWatchPass` printing per the pinned rules, loop with `nextWatchInterval` and a real timer, `--once` early return, `runtime.GOOS`-independent. Add a README `watch` section: usage, the safety model in three sentences (hardlinks for content, journal for structure, Trash as the undo), and a pointer to `--dry-run`.

- [ ] **Step 4: Run tests to verify they pass.** Run: `mise run check`. Expected: green.

- [ ] **Step 5: Commit**

```bash
git add cmd/watch.go cmd/watch_test.go README.md
git commit -m "feat(cmd): add the watch command"
```

---

### Task 10: launchd install/uninstall

**Files:**
- Create: `cmd/launchd.go`, `cmd/launchd_test.go`
- Modify: `cmd/watch.go`, `README.md`

**Interfaces:**
- Produces:
  - `type agentConfig struct { Label, Binary, Input, Output, LogPath string }`
  - `func launchdPlist(c agentConfig) string` — deterministic XML with `RunAtLoad`, `KeepAlive`, `ProgramArguments` (`binary watch <output> -i <input>`), `StandardOutPath`/`StandardErrorPath` = `LogPath`
  - `func agentLabel(input, output string) string` — `dev.ranokay.driveignore.watch.<WatchPairHash(input, output)>`
  - `func installAgent(c agentConfig) error` / `func uninstallAgent(label string) error` — write/remove `~/Library/LaunchAgents/<label>.plist`, `launchctl bootstrap gui/<uid>` / `bootout gui/<uid>/<label>`; non-darwin returns a clear error
- Consumed by: `watch --install` / `--uninstall` flags.

- [ ] **Step 1: Write the failing tests**

```go
func TestLaunchdPlistPinsArgumentsAndLogs(t *testing.T)
// exact string assertions for label, both argument pairs, RunAtLoad, KeepAlive, log paths.

func TestAgentLabelIsStablePerPair(t *testing.T)

func TestInstallAgentRejectsNonDarwin(t *testing.T) // runtime.GOOS guard, skipped on darwin
```

- [ ] **Step 2: Run tests to verify they fail.** Run: `go test ./cmd -run 'TestLaunchd|TestAgentLabel|TestInstallAgent' -v`. Expected: build failure.

- [ ] **Step 3: Implement** `cmd/launchd.go` and add the two flags to `watch`: `--install` writes the plist for the current binary (`os.Executable`), log at `~/Library/Logs/driveignore/watch-<hash>.log`, then loads it; `--uninstall` reverses. Extend the README with install/uninstall and where logs live.

- [ ] **Step 4: Run tests to verify they pass.** Run: `mise run check`. Expected: green. Manual check on this machine: `dist/driveignore watch "$HOME/Google Drive/My Drive/workspaces" -i "$HOME/Downloads/workspaces" --install`, then `launchctl print gui/$(id -u)/dev.ranokay.driveignore.watch.<hash>` shows it loaded; `--uninstall` removes it. (Executed for real in Task 11, not now.)

- [ ] **Step 5: Commit**

```bash
git add cmd/launchd.go cmd/launchd_test.go cmd/watch.go README.md
git commit -m "feat(cmd): install watch as a launchd agent"
```

---

### Task 11: Rollout on the real pair (manual)

**Files:** none. Deliverable: a comment on issue #37 with the observed dry-run reports and the decision to proceed.

- [ ] **Step 1: Build.** Run: `mise run build`. Then run `mise run test:race` and expect green.

- [ ] **Step 2: Dry-run the real pair.**
```bash
dist/driveignore watch "$HOME/Google Drive/My Drive/workspaces" -i "$HOME/Downloads/workspaces" --once --dry-run
```
Expected: only plausible actions (links/imports for anything that changed since the last `unify`); no `trashed-local`, no `removed-drive` surprises. Post the output to issue #37. If anything looks wrong, STOP.

- [ ] **Step 3: Converge for real, twice.** Same command without `--dry-run`, then again; the second run must print nothing and exit 0.

- [ ] **Step 4: One-way soak.** Run a foreground one-way loop for a working session:
```bash
dist/driveignore watch "$HOME/Google Drive/My Drive/workspaces" -i "$HOME/Downloads/workspaces" --one-way
```
Create, edit and delete files locally while it runs; confirm the Drive tree follows and no reverse actions happen.

- [ ] **Step 5: Two-way soak and install.** Foreground without `--one-way`; verify an edit made in the Drive folder (and one made via the web UI) lands locally. Then install the agent with `--install`, confirm it survives logout/login, and record the outcome on issue #37.

---

## Further Notes

- **Working clone:** the Drive-side clone and the local clone share inodes for every edited file; new files must be copied over (Task 1 Step 2 pattern) until the watcher itself reconciles the pair. Committing from both clones would create duplicate commits — always commit from this one.
- **State naming:** one journal and one lock per pair, keyed by the hash of the two absolute paths; this also means a pair can be re-created safely if the state directory is wiped (creations only, never deletions).
- **One-way vs two-way:** `--one-way` is the staging/safety mode and stays useful permanently; it is exactly "continuous unify" (local authoritative, drive-only paths removed).
- **Spec deviations discovered during planning, both submitted for review with this plan:** the command surface gains `--one-way` (the spec's rollout stage requires it) and `--interval` bounds are enforced as usage errors.
- **Out of scope carried from the spec:** copy mode, non-macOS install, multiple pairs per process, Drive API, type-mismatch auto-resolution, content hashing.

# driveignore

[![ci](https://github.com/ranokay/driveignore/actions/workflows/ci.yml/badge.svg)](https://github.com/ranokay/driveignore/actions/workflows/ci.yml)

`driveignore` keeps parts of a folder out of Google Drive for desktop. It reads
`.driveignore` files (same pattern syntax as `.gitignore`) and hardlinks the
files that should sync into your Drive folder. No file copies, no repeated CLI
runs: Google Drive sees changes through the links.

This is a maintained fork of
[shilangyu/driveignore](https://github.com/shilangyu/driveignore).

## requirements

- [Google Drive for desktop](https://www.google.com/drive/download/) on Windows
  or macOS
- the source folder and the Drive folder on the same filesystem — hardlinks
  cannot cross volumes

## installing

### Homebrew (macOS)

```sh
brew tap ranokay/driveignore https://github.com/ranokay/driveignore
brew install --cask driveignore
```

### Scoop (Windows)

```powershell
scoop bucket add driveignore https://github.com/ranokay/driveignore
scoop install driveignore
```

### install script (macOS, Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/ranokay/driveignore/main/install.sh | sh
```

Installs the latest release to `~/.local/bin` and verifies the download
against the release checksums. Override the destination with `INSTALL_DIR`
and pin a version with `VERSION`:

```sh
curl -fsSL https://raw.githubusercontent.com/ranokay/driveignore/main/install.sh | \
  VERSION=v1.2.0 INSTALL_DIR="$HOME/bin" sh
```

### Go

```sh
go install github.com/ranokay/driveignore@latest
```

Requires Go 1.27 or newer; make sure `$(go env GOPATH)/bin` is on your `PATH`.

### manual archives

Download the archive for your OS from the
[releases](https://github.com/ranokay/driveignore/releases), extract it, and
move the `driveignore` binary (`driveignore.exe` on Windows) into a directory
on your `PATH` — for example `~/.local/bin` on macOS/Linux, or
`%USERPROFILE%\bin` added to `PATH` on Windows. Running the binary from the
extraction directory only works while you stay there, so prefer one of the
options above. Winget is not published yet; Windows users can use Scoop or the
zip archive.

## how to use

1. Create an empty folder and add it to Google Drive for desktop's mirror list.
2. Create a `.driveignore` in the root of the folder you want to sync.
3. Run `driveignore unify [path to your Drive folder]` from the source folder.

The current directory is mirrored into the Drive folder with the `.driveignore`
applied. Because the mirrored files are hardlinks, editing either copy updates
both and Google Drive handles the rest.

```
Usage:
  driveignore [command]

Commands:
  clean    cleans the drive folder from files that no longer exist in the source
  diff     compares the source with the drive folder
  global   prints the path to the global .driveignore
  unify    uploads the source and removes legacy files in one go
  upload   hardlinks the source into the drive folder

Flags:
      --verbose   print what is happening
  -h, --help      help for driveignore
```

Every command documents its flags in `--help`. The shared flags are:

- `-i, --input` (default `.`): source directory for `upload`, `diff`, `unify`
- `-M, --merge-ignores`: merge the global and the local `.driveignore`
- `--force` (`upload` only): overwrite existing files with the same name
- `--dry-run` (`clean` only): list the files that would be removed without
  removing them
- `--prune-ignored` (`clean`, `unify`): also remove drive files excluded by
  `.driveignore`, even when the source still contains them
- `--copy` (`upload`, `unify`): copy files instead of hardlinking them, for
  filesystems without hardlink support. `clean` and `diff` treat files with
  equal size and modification time as in sync (recent timestamps are verified
  by content), so copies are kept and refreshed like links are.
- `--exit-code` (`diff` only): exit with status 1 when differences exist
- `--version`: print the version and exit

## global .driveignore

`driveignore global` prints the path to a global `.driveignore` (creating it if
needed). Uploads from a directory without its own `.driveignore` use the global
one; pass `--merge-ignores` to combine both.

## ignore patterns

Patterns follow the same rules as `.gitignore`: `*`, `?`, `**`, `!` negation,
leading `/` anchoring and trailing `/` for directory-only matches.

`.driveignore` files can live in subdirectories. Each one applies to its own
subtree, its patterns are anchored to the directory containing the file, and
deeper files override shallower ones — the same way `.gitignore` works.

Symlinks are skipped: they are neither uploaded nor followed, and `clean`
leaves symlinks inside the drive folder untouched.

## hardlinks and copies

By default `upload` and `unify` hardlink files. Nothing is duplicated and
edits on either side stay in sync through Google Drive, but the source and the
drive folder must be on the same filesystem.

If hardlinks are not supported (virtual drives, FAT/exFAT, network shares),
pass `--copy`: files are copied, preserving permissions and modification
times, and later runs replace stale copies only. Copy mode is a one-way
mirror — changes made in the drive folder are overwritten by the next
`unify`.

## platform support

- Windows amd64, Windows arm64
- macOS amd64, macOS arm64
- Linux amd64, Linux arm64 (no official Google Drive client; useful with mounted
  drives, best-effort)

## development

```sh
mise install            # install the pinned Go toolchain and tools
mise run check          # formatting, vet, lint and tests
mise run test:race      # tests with the race detector
mise run bench          # benchmarks
mise run hooks:install  # install the hk pre-commit hook for this clone
```

The hooks run gofmt, golangci-lint, `go mod tidy`, actionlint and pinact on
staged files; bypass a run with `HK=0 git commit ...` or `git commit --no-verify`.
`hk check --all` runs the same steps over the whole repository.

## license

Apache-2.0. Originally written by Marcin Wojnarowski, later maintained by
[shilangyu](https://github.com/shilangyu); this fork continues the project.

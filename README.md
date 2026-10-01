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

- download the archive for your OS from the
  [releases](https://github.com/ranokay/driveignore/releases) and put
  `driveignore` on your `PATH`, or
- `go install github.com/ranokay/driveignore@latest` (Go 1.27 or newer), or
- build from source with [mise](https://mise.jdx.dev/): `mise run build` writes
  the binary to `dist/`

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
- `--force` (`upload` only): overwrite a drive file whose name collides with a
  source file

## global .driveignore

`driveignore global` prints the path to a global `.driveignore` (creating it if
needed). Uploads from a directory without its own `.driveignore` use the global
one; pass `--merge-ignores` to combine both.

## ignore patterns

Patterns follow the same rules as `.gitignore`: `*`, `?`, `**`, `!` negation,
leading `/` anchoring and trailing `/` for directory-only matches.

## platform support

- Windows amd64, Windows arm64
- macOS amd64, macOS arm64
- Linux amd64, Linux arm64 (no official Google Drive client; useful with mounted
  drives, best-effort)

## development

```sh
mise install        # install the pinned Go toolchain and tools
mise run check      # formatting, vet, lint and tests
mise run test:race  # tests with the race detector
mise run bench      # benchmarks
```

## license

Apache-2.0. Originally written by Marcin Wojnarowski, later maintained by
[shilangyu](https://github.com/shilangyu); this fork continues the project.

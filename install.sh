#!/bin/sh
# Installs driveignore from GitHub releases.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ranokay/driveignore/main/install.sh | sh
#
# Environment:
#   VERSION      release tag to install, for example v1.2.0 (default: latest)
#   INSTALL_DIR  destination directory (default: ~/.local/bin)
#   BASE_URL     download base URL (default: the GitHub release for VERSION)
set -eu

repo="ranokay/driveignore"
version="${VERSION:-}"
install_dir="${INSTALL_DIR:-$HOME/.local/bin}"

err() {
	printf 'install: %s\n' "$1" >&2
	exit 1
}

command -v curl >/dev/null 2>&1 || err "curl is required"

os=$(uname -s)
case "$os" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) err "unsupported operating system: $os (use the Windows archive or Scoop instead)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) err "unsupported architecture: $arch" ;;
esac

if [ -z "$version" ]; then
	version=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
		err "could not determine the latest release"
	version=${version##*/}
fi
case "$version" in
	v*) ;;
	*) version="v$version" ;;
esac

archive="driveignore_${version#v}_${os}_${arch}.tar.gz"
base="${BASE_URL:-https://github.com/$repo/releases/download/$version}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

printf 'downloading %s\n' "$archive"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || err "download failed: $base/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || err "could not download checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
else
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
fi
expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || err "no checksum found for $archive"
[ "$actual" = "$expected" ] || err "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp" || err "could not extract $archive"
mkdir -p "$install_dir"
if ! install -m 0755 "$tmp/driveignore" "$install_dir/driveignore" 2>/dev/null; then
	cp "$tmp/driveignore" "$install_dir/driveignore"
	chmod 0755 "$install_dir/driveignore"
fi

printf 'installed %s to %s\n' "$version" "$install_dir/driveignore"
case ":$PATH:" in
	*":$install_dir:"*) ;;
	*) printf "add %s to your PATH:\n  export PATH=\"%s:\$PATH\"\n" "$install_dir" "$install_dir" ;;
esac

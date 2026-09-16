#!/bin/sh
set -eu

case "${1:-}" in
  --help|-h)
    cat <<'HELP'
Usage: sh install.sh [VERSION]

Install a taskhub Release for this macOS or Linux machine.
VERSION defaults to latest. Example: sh install.sh v0.3.0
INSTALL_DIR selects the binary directory; default: ~/.local/bin.
The installer verifies the downloaded archive against its SHA-256 checksum.

After installing, run taskhub --help for the operating manual.
For natural-language task management in Codex, run taskhub skill install.
Existing server connection settings and task data are retained.
HELP
    exit 0
    ;;
esac

version=${1:-latest}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid release version' >&2; exit 1 ;; esac
case "$(uname -s)" in
  Darwin) target_os=darwin ;;
  Linux) target_os=linux ;;
  *) echo 'Supported systems: macOS and Linux' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) target_arch=arm64 ;;
  x86_64|amd64) target_arch=amd64 ;;
  *) echo 'Supported architectures: arm64 and amd64' >&2; exit 1 ;;
esac

base=https://github.com/felixfeng33/taskhub/releases
if [ "$version" = latest ]; then base="$base/latest/download"; else base="$base/download/$version"; fi
asset="taskhub_${target_os}_${target_arch}.tar.gz"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT HUP INT TERM
curl -fsSL --retry 2 "$base/$asset" -o "$stage/$asset"
curl -fsSL --retry 2 "$base/checksums.txt" -o "$stage/checksums.txt"
expected=$(awk -v file="$asset" '$2 == file { print $1 }' "$stage/checksums.txt")
if [ -z "$expected" ]; then echo 'Missing release checksum' >&2; exit 1; fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$stage/$asset" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$stage/$asset" | awk '{print $1}')
fi
if [ "$actual" != "$expected" ]; then echo 'Checksum mismatch' >&2; exit 1; fi
tar -xzf "$stage/$asset" -C "$stage" taskhub
destination=${INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$destination"
install -m 755 "$stage/taskhub" "$destination/taskhub.new.$$"
mv -f "$destination/taskhub.new.$$" "$destination/taskhub"
printf 'Installed %s/taskhub (%s)\n' "$destination" "$("$destination/taskhub" version)"
printf 'Make sure %s is on your PATH.\n' "$destination"

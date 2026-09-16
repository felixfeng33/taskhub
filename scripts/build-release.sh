#!/bin/sh
set -eu

version=${1:?Usage: scripts/build-release.sh v0.1.0}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid version' >&2; exit 1 ;; esac
cd "$(dirname "$0")/.."
mkdir -p dist
for platform in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  target_os=${platform%/*}
  target_arch=${platform#*/}
  stage=$(mktemp -d)
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build \
    -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/taskhub" ./cmd/taskhub
  cp LICENSE THIRD_PARTY_NOTICES README.md README.zh-CN.md "$stage/"
  COPYFILE_DISABLE=1 tar -C "$stage" -czf "dist/taskhub_${target_os}_${target_arch}.tar.gz" taskhub LICENSE THIRD_PARTY_NOTICES README.md README.zh-CN.md
  rm -rf "$stage"
done
cd dist
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum taskhub_*.tar.gz > checksums.txt
else
  shasum -a 256 taskhub_*.tar.gz > checksums.txt
fi

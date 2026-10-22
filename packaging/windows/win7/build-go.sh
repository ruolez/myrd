#!/usr/bin/env bash
# build-go.sh <dest> — builds the Go toolchain for the Windows 7 agent into
# <dest> (reused when it is already that build): the official Go source,
# pinned by its go.dev SHA-256, plus go1.27.3-win7.patch from this directory.
#
# The patch is the difference between that source and XTLS/go-win7's
# patched-1.27.3 tree (github.com/XTLS/go-win7), reviewed and kept here so no
# third-party toolchain is downloaded: PE minimum OS version 6.x, random
# numbers from BCryptGenRandom instead of ProcessPrng (Windows 10), no long
# path opt-in, console handle inheritance on Windows 7, GOTOOLCHAIN=local.
#
# Needs a Go 1.22+ on PATH to bootstrap, curl, patch and sha256sum/shasum.
set -euo pipefail
GO_VERSION=go1.27.3
GO_SRC_SHA256=6e7749035cf360f06b2575d162ea79df9fda8ef671889e5c375f8e50777f26bb
HERE="$(cd "$(dirname "$0")" && pwd)"
PATCH="$HERE/$GO_VERSION-win7.patch"
DEST="${1:?usage: build-go.sh <dest>}"

stamp="$GO_VERSION+$(cat "$PATCH" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-16)"
if [ -x "$DEST/bin/go" ] && [ "$(cat "$DEST/.win7-build" 2>/dev/null)" = "$stamp" ]; then
  echo "$DEST"
  exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
curl -fsSL -o "$work/go.src.tar.gz" "https://go.dev/dl/$GO_VERSION.src.tar.gz"
got="$( (sha256sum "$work/go.src.tar.gz" 2>/dev/null || shasum -a 256 "$work/go.src.tar.gz") | cut -d' ' -f1)"
if [ "$got" != "$GO_SRC_SHA256" ]; then
  echo "go source SHA-256 $got, want $GO_SRC_SHA256" >&2
  exit 1
fi
tar -xzf "$work/go.src.tar.gz" -C "$work"
(cd "$work/go" && patch -p1 --forward --no-backup-if-mismatch < "$PATCH" >&2)
(cd "$work/go/src" && GOROOT_BOOTSTRAP="$(go env GOROOT)" GOTOOLCHAIN=local ./make.bash >&2)

rm -rf "$DEST"
mkdir -p "$(dirname "$DEST")"
mv "$work/go" "$DEST"
echo "$stamp" > "$DEST/.win7-build"
echo "$DEST"

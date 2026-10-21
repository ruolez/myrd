#!/usr/bin/env bash
# Builds a static libvpx (VP9 only, real-time) for the full-color stream into
# native/common/build/libvpx-<target>/{include,lib}. Pinned and checksummed.
#   native/common/build-libvpx.sh darwin-arm64 | darwin-x86_64 | darwin-universal
#   native/common/build-libvpx.sh linux-x86_64 | linux-arm64 | win64-mingw
# x86 targets need nasm. Prints the install prefix on success.
set -euo pipefail
VERSION=1.16.0
SHA256=7a479a3c66b9f5d5542a4c6a1b7d3768a983b1e5c14c60a9396edc9b649e015c
HERE="$(cd "$(dirname "$0")" && pwd)"
BUILD="$HERE/build"
TARGET="${1:?usage: $0 <target>}"

fetch() {
  local tgz="$BUILD/libvpx-$VERSION.tar.gz"
  mkdir -p "$BUILD"
  if [[ ! -f "$tgz" ]]; then
    curl -fsSL -m 300 -o "$tgz.part" "https://github.com/webmproject/libvpx/archive/refs/tags/v$VERSION.tar.gz"
    mv "$tgz.part" "$tgz"
  fi
  local got
  got="$( (sha256sum "$tgz" 2>/dev/null || shasum -a 256 "$tgz") | cut -d' ' -f1)"
  if [[ "$got" != "$SHA256" ]]; then
    echo "libvpx-$VERSION.tar.gz: sha256 $got, want $SHA256" >&2
    rm -f "$tgz"
    exit 1
  fi
  if [[ ! -d "$BUILD/libvpx-$VERSION" ]]; then tar -xzf "$tgz" -C "$BUILD"; fi
}

# build <name> <libvpx --target> [extra configure args...]
build() {
  local name="$1" vtarget="$2"
  shift 2
  local prefix="$BUILD/libvpx-$name"
  local stamp="$VERSION $*"
  if [[ -f "$prefix/lib/libvpx.a" && -f "$prefix/.version" && "$(cat "$prefix/.version")" == "$stamp" ]]; then
    return
  fi
  local obj="$BUILD/obj-libvpx-$name"
  rm -rf "$obj" "$prefix"
  mkdir -p "$obj"
  (cd "$obj" && "$BUILD/libvpx-$VERSION/configure" --target="$vtarget" --prefix="$prefix" \
    --enable-static --disable-shared --enable-pic --enable-realtime-only --enable-multithread \
    --enable-vp9 --disable-vp8 --disable-examples --disable-tools --disable-docs --disable-unit-tests \
    --disable-webm-io --disable-libyuv --disable-install-bins --disable-install-srcs "$@" >/dev/null &&
    make -j"$(getconf _NPROCESSORS_ONLN)" >/dev/null && make install >/dev/null)
  echo "$stamp" >"$prefix/.version"
  rm -rf "$obj"
}

# The agent targets macOS 13 (packaging/macos); libvpx would take the SDK's.
MAC=(--extra-cflags="-mmacosx-version-min=${MIN_MACOS:-13.0}")
fetch
case "$TARGET" in
  darwin-arm64) build darwin-arm64 arm64-darwin20-gcc "${MAC[@]}" ;;
  darwin-x86_64) build darwin-x86_64 x86_64-darwin20-gcc --as=nasm "${MAC[@]}" ;;
  darwin-universal)
    build darwin-arm64 arm64-darwin20-gcc "${MAC[@]}"
    build darwin-x86_64 x86_64-darwin20-gcc --as=nasm "${MAC[@]}"
    out="$BUILD/libvpx-darwin-universal"
    mkdir -p "$out/lib"
    rm -rf "$out/include"
    cp -R "$BUILD/libvpx-darwin-arm64/include" "$out/include"
    xcrun lipo -create "$BUILD/libvpx-darwin-arm64/lib/libvpx.a" "$BUILD/libvpx-darwin-x86_64/lib/libvpx.a" \
      -output "$out/lib/libvpx.a"
    ;;
  linux-x86_64) build linux-x86_64 x86_64-linux-gcc --as=nasm ;;
  linux-arm64) build linux-arm64 arm64-linux-gcc ;;
  win64-mingw)
    # MSYS2 (the release runner) builds natively; elsewhere it is a cross build.
    case "$(uname -s)" in *_NT*) cross= ;; *) cross=x86_64-w64-mingw32- ;; esac
    CROSS="$cross" build win64-mingw x86_64-win64-gcc --as=nasm
    ;;
  *) echo "unknown target $TARGET" >&2; exit 2 ;;
esac
echo "$BUILD/libvpx-$TARGET"

#!/usr/bin/env bash
# Builds native/macos/build/librcnative.a — the macOS implementation of
# native/include/rc_native.h (Objective-C, ARC), with the shared full-color
# encoder (native/common/rc_vpx.c) and a static libvpx merged in.
#   ./build.sh             arm64 (host)
#   ARCHS="arm64 x86_64"   or UNIVERSAL=1 → universal (lipo)
#   DEBUG=1                -O0 -g and RCLOG to stderr
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
OUT="$HERE/build"
MIN_MACOS="${MIN_MACOS:-13.0}"
if [[ "${UNIVERSAL:-0}" == "1" ]]; then ARCHS="arm64 x86_64"; fi
ARCHS="${ARCHS:-$(uname -m)}"
OPT=(-O2)
[[ "${DEBUG:-0}" == "1" ]] && OPT=(-O0 -g -DRC_DEBUG=1)

SRCS=(rc_display.m rc_capture.m rc_input.m rc_ui.m)
COMMON="$HERE/../common"
mkdir -p "$OUT"
LIBS=()
for arch in $ARCHS; do
  VPX="$("$COMMON/build-libvpx.sh" "darwin-$arch" | tail -1)"
  OBJDIR="$OUT/obj-$arch"
  mkdir -p "$OBJDIR"
  OBJS=()
  INC=(-I"$HERE/../include" -I"$HERE/src" -I"$COMMON" -I"$VPX/include")
  for s in "${SRCS[@]}"; do
    o="$OBJDIR/${s%.m}.o"
    xcrun clang -c "$HERE/src/$s" -o "$o" -arch "$arch" -mmacosx-version-min="$MIN_MACOS" \
      -fobjc-arc -fmodules -Wall -Wextra -Wno-unused-parameter -Werror=implicit-function-declaration \
      "${OPT[@]}" "${INC[@]}"
    OBJS+=("$o")
  done
  xcrun clang -c "$COMMON/rc_vpx.c" -o "$OBJDIR/rc_vpx.o" -arch "$arch" -mmacosx-version-min="$MIN_MACOS" \
    -std=gnu11 -Wall -Wextra -Wno-unused-parameter "${OPT[@]}" "${INC[@]}"
  OBJS+=("$OBJDIR/rc_vpx.o")
  xcrun clang -c "$COMMON/rc_scale.c" -o "$OBJDIR/rc_scale.o" -arch "$arch" -mmacosx-version-min="$MIN_MACOS" \
    -std=gnu11 -Wall -Wextra "${OPT[@]}" "${INC[@]}"
  OBJS+=("$OBJDIR/rc_scale.o")
  xcrun libtool -static -o "$OUT/librcnative-$arch.a" "${OBJS[@]}" "$VPX/lib/libvpx.a" 2>/dev/null
  LIBS+=("$OUT/librcnative-$arch.a")
done
if [[ ${#LIBS[@]} -gt 1 ]]; then
  xcrun lipo -create "${LIBS[@]}" -output "$OUT/librcnative.a"
else
  cp "${LIBS[0]}" "$OUT/librcnative.a"
fi
echo "built $OUT/librcnative.a ($ARCHS)"

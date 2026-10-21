#!/usr/bin/env bash
# Builds the static lib and runs the ABI harness against it.
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
"$HERE/build.sh" >/dev/null
VPX="$("$HERE/../common/build-libvpx.sh" "darwin-$(uname -m)" | tail -1)"
xcrun clang -fobjc-arc -fmodules -O1 -mmacosx-version-min=13.0 -I"$HERE/../include" -I"$VPX/include" \
  "$HERE/test/harness.m" "$HERE/build/librcnative.a" -o "$HERE/build/rc-harness" \
  -framework ScreenCaptureKit -framework VideoToolbox -framework CoreMedia -framework CoreVideo \
  -framework CoreGraphics -framework ApplicationServices -framework AppKit -framework Foundation -framework Carbon
exec "$HERE/build/rc-harness"

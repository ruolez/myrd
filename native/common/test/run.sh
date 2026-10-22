#!/usr/bin/env bash
# Runs the BGRA → NV12 converter and preview scaler tests, then builds libvpx for the host and
# runs the full-color encoder tests.
set -euo pipefail
HERE="$(cd "$(dirname "$0")/.." && pwd)"
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) T=darwin-arm64 ;;
  Darwin-x86_64) T=darwin-x86_64 ;;
  Linux-x86_64) T=linux-x86_64 ;;
  Linux-aarch64) T=linux-arm64 ;;
  *) echo "unsupported host" >&2; exit 2 ;;
esac
mkdir -p "$HERE/build"
${CC:-cc} -O2 -std=gnu11 -Wall -Wextra -Wno-unused-parameter -I"$HERE" \
  "$HERE/test/nv12_test.c" "$HERE/rc_nv12.c" -o "$HERE/build/nv12-test"
${CC:-cc} -O2 -std=gnu11 -Wall -Wextra -I"$HERE" "$HERE/test/scale_test.c" "$HERE/rc_scale.c" -o "$HERE/build/scale-test"
"$HERE/build/scale-test"
"$HERE/build/nv12-test"
VPX="$("$HERE/build-libvpx.sh" "$T" | tail -1)"
${CC:-cc} -O2 -std=gnu11 -Wall -Wextra -Wno-unused-parameter -I"$HERE" -I"$HERE/../include" -I"$VPX/include" \
  "$HERE/test/vpx_test.c" "$HERE/rc_vpx.c" "$VPX/lib/libvpx.a" -lm -lpthread -o "$HERE/build/vpx-test"
exec "$HERE/build/vpx-test"

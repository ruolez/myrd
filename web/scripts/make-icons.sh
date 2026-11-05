#!/usr/bin/env bash
# Renders the PWA icons in public/icons/ from public/favicon.svg and
# scripts/icon-full-bleed.svg with headless Chrome. The PNGs are committed;
# rerun this after changing either SVG.
set -euo pipefail
cd "$(dirname "$0")/.."

CHROME=${CHROME:-}
if [[ -z $CHROME ]]; then
  for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" google-chrome chromium chromium-browser; do
    if command -v "$c" >/dev/null 2>&1 || [[ -x $c ]]; then CHROME=$c; break; fi
  done
fi
[[ -n $CHROME ]] || { echo "Chrome not found; set CHROME=/path/to/chrome" >&2; exit 1; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# render <svg file> <size> <glyph scale> <out png>
render() {
  local svg=$1 size=$2 scale=$3 out=$4
  cat >"$tmp/icon.html" <<HTML
<!doctype html><html><head><style>
html,body{margin:0;background:transparent}
svg{display:block;width:${size}px;height:${size}px}
#glyph{transform-box:view-box;transform-origin:32px 31.25px;transform:scale(${scale})}
</style></head><body>$(cat "$svg")</body></html>
HTML
  "$CHROME" --headless --disable-gpu --hide-scrollbars --force-device-scale-factor=1 \
    --default-background-color=00000000 --window-size="$size,$size" \
    --screenshot="$tmp/out.png" "file://$tmp/icon.html" >/dev/null 2>&1
  mv "$tmp/out.png" "$out"
  echo "$out"
}

render public/favicon.svg 192 1 public/icons/icon-192.png
render public/favicon.svg 512 1 public/icons/icon-512.png
# Maskable: the glyph must stay inside the central 80% circle.
render scripts/icon-full-bleed.svg 512 0.78 public/icons/maskable-512.png
render scripts/icon-full-bleed.svg 180 0.92 public/icons/apple-touch-icon.png

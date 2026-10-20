#!/usr/bin/env bash
# Builds and signs a macOS release: dist/macos/RemoteAgent-<VERSION>.pkg plus
# its .sig sidecar, ready for Admin → Releases.
#   SERVER_URL   control plane baked into the app (default https://myrd.site)
#   RELEASE_KEY  release private key file (default ~/.config/remotectl/release.key)
# Needs the identity from make-signing-cert.sh (or DEVELOPER_ID_APP).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck disable=SC1091
source "$HERE/product.env"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
export VERSION SERVER_URL="${SERVER_URL:-https://myrd.site}" UNIVERSAL=1
RELEASE_KEY="${RELEASE_KEY:-$HOME/.config/remotectl/release.key}"

[[ -n "${DEVELOPER_ID_APP:-}${CODESIGN_IDENTITY:-}" || -f "$HOME/.config/remotectl/codesign-identity" ]] ||
  { echo "no signing identity: run packaging/macos/make-signing-cert.sh once" >&2; exit 1; }
[[ -f "$RELEASE_KEY" ]] || { echo "missing release key $RELEASE_KEY" >&2; exit 1; }
if [[ -n "$(git -C "$ROOT" status --porcelain)" ]]; then
  echo "warning: the working tree has uncommitted changes" >&2
fi

"$HERE/build-app.sh"
"$HERE/build-pkg.sh"
APP="$ROOT/dist/macos/$APP_NAME.app"
PKG="$ROOT/dist/macos/$APP_NAME-$VERSION.pkg"
if codesign -dv "$APP" 2>&1 | grep -q '^Signature=adhoc'; then
  echo "$APP is ad-hoc signed; releases need the stable identity" >&2
  exit 1
fi
(cd "$ROOT/server" && "$ROOT/scripts/gohost.sh" run ./cmd/remotectl-release sign \
  --key-file "$RELEASE_KEY" --version "$VERSION" --os darwin --arch universal "$PKG")
(cd "$ROOT/server" && "$ROOT/scripts/gohost.sh" run ./cmd/remotectl-release verify "$PKG")
echo
echo "Upload both files in Admin → Releases:"
echo "  $PKG"
echo "  $PKG.sig"

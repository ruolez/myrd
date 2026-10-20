#!/usr/bin/env bash
# Builds dist/macos/RemoteAgent.app.
#   VERSION=1.2.3          bundle + agent version (default 0.0.0-dev)
#   SERVER_URL=https://…   control plane URL baked into Info.plist (LSEnvironment) + LaunchAgent
#   UNIVERSAL=1            arm64 + x86_64 (lipo); default host arch only
#   DEVELOPER_ID_APP="Developer ID Application: Org (TEAMID)"  real signing; else ad-hoc
#   RELEASE_PUBLIC_KEY=<b64 ed25519>  compiled in for update-manifest verification
#   GO=go                  Go command (default: scripts/gohost.sh, the repo's offline host Go)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck disable=SC1091
source "$HERE/product.env"
export VERSION="${VERSION:-0.0.0-dev}"
export SERVER_URL="${SERVER_URL:-https://remote.example.com}"
GO="${GO:-$ROOT/scripts/gohost.sh}"
if [[ "${UNIVERSAL:-0}" == "1" ]]; then ARCHS="arm64 x86_64"; fi
ARCHS="${ARCHS:-$(uname -m)}"
OUT="$ROOT/dist/macos"
BUILD="$OUT/build"
APP="$OUT/$APP_NAME.app"
mkdir -p "$BUILD"

ARCHS="$ARCHS" MIN_MACOS="$MIN_MACOS" "$ROOT/native/macos/build.sh"

LDFLAGS="-s -w -X main.version=$VERSION"
[[ -n "${RELEASE_PUBLIC_KEY:-}" ]] && LDFLAGS="$LDFLAGS -X main.releasePublicKey=$RELEASE_PUBLIC_KEY"
BINS=()
for arch in $ARCHS; do
  goarch="$arch"; [[ "$arch" == "x86_64" ]] && goarch=amd64
  echo "go build darwin/$goarch"
  (cd "$ROOT/agent" && CGO_ENABLED=1 GOOS=darwin GOARCH="$goarch" CGO_CFLAGS="-O2 -g -mmacosx-version-min=$MIN_MACOS" \
    "$GO" build -trimpath -ldflags "$LDFLAGS" -o "$BUILD/$EXECUTABLE-$arch" ./cmd/remote-agent)
  BINS+=("$BUILD/$EXECUTABLE-$arch")
done

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Library/LaunchAgents"
if [[ ${#BINS[@]} -gt 1 ]]; then
  xcrun lipo -create "${BINS[@]}" -output "$APP/Contents/MacOS/$EXECUTABLE"
else
  cp "${BINS[0]}" "$APP/Contents/MacOS/$EXECUTABLE"
fi
"$HERE/render.sh" "$HERE/Info.plist.in" "$APP/Contents/Info.plist"
"$HERE/render.sh" "$HERE/launchagent.plist.in" "$APP/Contents/Library/LaunchAgents/$BUNDLE_ID.plist"
cp "$HERE/uninstall.sh" "$APP/Contents/Resources/uninstall.sh"
chmod 755 "$APP/Contents/Resources/uninstall.sh"
[[ -f "$HERE/AppIcon.icns" ]] && cp "$HERE/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
printf 'APPL????' > "$APP/Contents/PkgInfo"
plutil -lint "$APP/Contents/Info.plist" >/dev/null
# A Finder launch gets its server URL only from LSEnvironment.
BAKED_URL="$(plutil -extract LSEnvironment.RC_SERVER_URL raw "$APP/Contents/Info.plist")"
[[ "$BAKED_URL" == "$SERVER_URL" ]] || { echo "Info.plist RC_SERVER_URL is '$BAKED_URL'" >&2; exit 1; }

if [[ -n "${DEVELOPER_ID_APP:-}" ]]; then
  codesign --force --options runtime --timestamp --entitlements "$HERE/entitlements.plist" \
    --sign "$DEVELOPER_ID_APP" "$APP"
else
  # Ad-hoc: runs locally, but TCC grants and the Keychain ACL reset on every rebuild.
  codesign --force --options runtime --entitlements "$HERE/entitlements.plist" --sign - "$APP"
fi
codesign --verify --strict --verbose=1 "$APP"
echo "built $APP ($ARCHS, $VERSION, server $BAKED_URL)"

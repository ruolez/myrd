#!/usr/bin/env bash
# Builds dist/macos/RemoteAgent-<version>.pkg from an already built .app.
#   VERSION and SERVER_URL are taken from the .app (build-app.sh bakes both); a different SERVER_URL is an error
#   DEVELOPER_ID_INSTALLER="Developer ID Installer: Org (TEAMID)"   signs the pkg
#   NOTARY_KEY_PATH / NOTARY_KEY_ID / NOTARY_ISSUER   App Store Connect API key → notarize + staple
# Scripted install with an enrollment token (no terminal on the device needed for GUI installs):
#   sudo defaults write /Library/Preferences/com.remotectl.agent EnrollToken XXXX-XXXX
#   sudo installer -pkg RemoteAgent-1.2.3.pkg -target /
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
# shellcheck disable=SC1091
source "$HERE/product.env"
OUT="$ROOT/dist/macos"
APP="$OUT/$APP_NAME.app"
[[ -d "$APP" ]] || { echo "missing $APP — run build-app.sh first" >&2; exit 1; }
# Version and server URL come from the app build-app.sh made.
VERSION="$(plutil -extract CFBundleShortVersionString raw "$APP/Contents/Info.plist")"
export VERSION
APP_URL="$(plutil -extract LSEnvironment.RC_SERVER_URL raw "$APP/Contents/Info.plist")"
[[ -z "${SERVER_URL:-}" || "$SERVER_URL" == "$APP_URL" ]] ||
  { echo "SERVER_URL $SERVER_URL differs from the app's $APP_URL — rebuild the app" >&2; exit 1; }
export SERVER_URL="$APP_URL"

WORK="$OUT/pkgwork"
rm -rf "$WORK"
mkdir -p "$WORK/root/Applications" "$WORK/root/Library/LaunchAgents" "$WORK/scripts"
ditto "$APP" "$WORK/root/Applications/$APP_NAME.app"
"$HERE/render.sh" "$HERE/launchagent.plist.in" "$WORK/root/Library/LaunchAgents/$BUNDLE_ID.plist"
for s in preinstall postinstall; do
  "$HERE/render.sh" "$HERE/scripts/$s" "$WORK/scripts/$s"
  chmod 755 "$WORK/scripts/$s"
done

# Never let Installer "relocate" the app to wherever an old copy lives.
pkgbuild --analyze --root "$WORK/root" "$WORK/component.plist" >/dev/null
plutil -replace 0.BundleIsRelocatable -bool NO "$WORK/component.plist"

pkgbuild --root "$WORK/root" --component-plist "$WORK/component.plist" --scripts "$WORK/scripts" \
  --identifier "$BUNDLE_ID.pkg" --version "$VERSION" --install-location / "$WORK/component.pkg"

cat > "$WORK/distribution.xml" <<XML
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>$APP_NAME</title>
  <options customize="never" require-scripts="true" hostArchitectures="arm64,x86_64"/>
  <os-version min="$MIN_MACOS"/>
  <domains enable_localSystem="true"/>
  <choices-outline><line choice="default"/></choices-outline>
  <choice id="default" title="$APP_NAME"><pkg-ref id="$BUNDLE_ID.pkg"/></choice>
  <pkg-ref id="$BUNDLE_ID.pkg" version="$VERSION" onConclusion="none">component.pkg</pkg-ref>
</installer-gui-script>
XML

PKG="$OUT/$APP_NAME-$VERSION.pkg"
SIGN=()
[[ -n "${DEVELOPER_ID_INSTALLER:-}" ]] && SIGN=(--sign "$DEVELOPER_ID_INSTALLER" --timestamp)
productbuild --distribution "$WORK/distribution.xml" --package-path "$WORK" ${SIGN[@]+"${SIGN[@]}"} "$PKG"

if [[ -n "${NOTARY_KEY_PATH:-}" && -n "${NOTARY_KEY_ID:-}" && -n "${NOTARY_ISSUER:-}" ]]; then
  xcrun notarytool submit "$PKG" --key "$NOTARY_KEY_PATH" --key-id "$NOTARY_KEY_ID" \
    --issuer "$NOTARY_ISSUER" --wait
  xcrun stapler staple "$PKG"
  spctl --assess --type install --verbose "$PKG"
fi
echo "built $PKG"

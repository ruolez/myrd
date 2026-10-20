#!/bin/sh
# Removes the agent: unloads the LaunchAgent and the update daemon,
# deregisters the device (DELETE /v1/agent/self), deletes the device key (file
# in the data dir, plus the Keychain item older builds kept), app and data.
# Shipped as RemoteAgent.app/Contents/Resources/uninstall.sh; needs root
# (re-launches itself through an admin prompt when started from the tray).
LABEL="com.remotectl.agent"
APP="/Applications/RemoteAgent.app"
BIN="$APP/Contents/MacOS/remote-agent"
UPDATER_LABEL="com.remotectl.updater"

if [ "$(id -u)" != "0" ]; then
  if [ -t 0 ]; then exec sudo "$0" "$@"; fi
  exec osascript -e "do shell script quoted form of \"$0\" with administrator privileges"
fi

launchctl bootout "system/$UPDATER_LABEL" 2>/dev/null || true
rm -f "/Library/LaunchDaemons/$UPDATER_LABEL.plist"
rm -rf "/Library/Application Support/RemoteAgent"

user=$(stat -f %Su /dev/console)
if [ -n "$user" ] && [ "$user" != "root" ]; then
  uid=$(id -u "$user")
  home=$(dscl . -read "/Users/$user" NFSHomeDirectory | sed 's/^NFSHomeDirectory: //')
  launchctl bootout "gui/$uid/$LABEL" 2>/dev/null || true
  if [ -x "$BIN" ]; then
    launchctl asuser "$uid" sudo -H -u "$user" env HOME="$home" "$BIN" uninstall || echo "server deregistration failed (continuing)"
  fi
  launchctl asuser "$uid" sudo -u "$user" security delete-generic-password -s "$LABEL" -a device-key >/dev/null 2>&1 || true
  rm -rf "$home/Library/Application Support/RemoteAgent"
fi

rm -f "/Library/LaunchAgents/$LABEL.plist"
rm -rf "$APP"
defaults delete "/Library/Preferences/$LABEL" 2>/dev/null || true
pkgutil --forget "$LABEL.pkg" >/dev/null 2>&1 || true
echo "RemoteAgent removed."

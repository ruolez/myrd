#!/bin/sh
# Starts the virtual display and desktop, then the agent (or any remote-agent
# subcommand passed as arguments, e.g. `selftest`).
set -eu

SCREEN="${RC_SCREEN:-1920x1080}"
rm -f /tmp/.X0-lock /tmp/.X11-unix/X0 2>/dev/null || true
Xvfb :0 -screen 0 "${SCREEN}x24" -nolisten tcp -dpi 96 +extension RANDR +extension DAMAGE -ac >/tmp/xvfb.log 2>&1 &

i=0
until xdpyinfo -display :0 >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 100 ]; then echo "Xvfb did not start" >&2; cat /tmp/xvfb.log >&2; exit 1; fi
  sleep 0.1
done

if [ "${RC_NO_DESKTOP:-}" != "1" ]; then
  dbus-launch --exit-with-session startxfce4 >/tmp/xfce.log 2>&1 &
fi

exec /usr/local/bin/remote-agent "$@"

#!/bin/sh
# Starts the virtual display and desktop, then the agent (or any remote-agent
# subcommand passed as arguments, e.g. `selftest`).
set -eu

SCREEN="${RC_SCREEN:-1920x1080}"
# The framebuffer is as large as the largest mode, so the remote resolution
# control can switch between these (Xvfb cannot grow past its start size).
MODES="2560x1600 2560x1440 1920x1200 1920x1080 1680x1050 1600x900 1440x900 1366x768 1280x800 1280x720 1024x768"
rm -f /tmp/.X0-lock /tmp/.X11-unix/X0 2>/dev/null || true
Xvfb :0 -screen 0 2560x1600x24 -noreset -nolisten tcp -dpi 96 +extension RANDR +extension DAMAGE -ac >/tmp/xvfb.log 2>&1 &

i=0
until xdpyinfo -display :0 >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 100 ]; then echo "Xvfb did not start" >&2; cat /tmp/xvfb.log >&2; exit 1; fi
  sleep 0.1
done

OUT="$(xrandr -display :0 | awk '/ connected/ {print $1; exit}')"
for m in $MODES $SCREEN; do
  w="${m%x*}" h="${m#*x}"
  xrandr -display :0 --newmode "$m" 0 "$w" 0 0 0 "$h" 0 0 0 2>/dev/null || true
  xrandr -display :0 --addmode "$OUT" "$m" 2>/dev/null || true
done
xrandr -display :0 --output "$OUT" --mode "$SCREEN"

if [ "${RC_NO_DESKTOP:-}" != "1" ]; then
  dbus-launch --exit-with-session startxfce4 >/tmp/xfce.log 2>&1 &
fi

exec /usr/local/bin/remote-agent "$@"

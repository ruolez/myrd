#!/usr/bin/env bash
# check-imports.sh <exe> [objdump] — fails when the exe imports a DLL that a
# stock Windows 10 does not ship (MinGW runtime: libwinpthread-1.dll,
# libstdc++-6.dll, libgcc_s_seh-1.dll, ...). Such an exe cannot start.
set -euo pipefail
exe="$1"
objdump="${2:-x86_64-w64-mingw32-objdump}"
dlls="$("$objdump" -p "$exe" | sed -n 's/^[[:space:]]*DLL Name: //p')"
bad="$(echo "$dlls" | grep -Ei '^lib' || true)"
if [ -n "$bad" ]; then
  echo "$exe imports DLLs Windows does not ship:" >&2
  while IFS= read -r dll; do echo "  $dll" >&2; done <<<"$bad"
  exit 1
fi
echo "$exe: imports only system DLLs ($(echo "$dlls" | wc -l | tr -d ' '))"

#!/usr/bin/env bash
# check-imports.sh <exe> [objdump] — fails when the exe imports a DLL that a
# stock Windows 10 does not ship (MinGW runtime: libwinpthread-1.dll,
# libstdc++-6.dll, libgcc_s_seh-1.dll, ...). Such an exe cannot start.
# With RC_TARGET=win7 (the Windows 7 build) it also fails on the Universal C
# Runtime and API set DLLs (api-ms-win-*: an update on Windows 7) and on
# imports of functions that Windows 7 does not have.
set -euo pipefail
exe="$1"
objdump="${2:-x86_64-w64-mingw32-objdump}"
dump="$("$objdump" -p "$exe")"
dlls="$(echo "$dump" | sed -n 's/^[[:space:]]*DLL Name: //p')"
bad="$(echo "$dlls" | grep -Ei '^lib' || true)"
if [ "${RC_TARGET:-}" = win7 ]; then
  bad="$bad"$'\n'"$(echo "$dlls" | grep -Ei '^(api-ms-win-|ext-ms-win-|ucrtbase)' || true)"
  # Windows 8+ functions a toolchain or runtime may import directly.
  win8='GetSystemTimePreciseAsFileTime|CreateFile2|WaitOnAddress|WakeByAddress(Single|All)|SetThreadDescription|GetThreadDescription|PrefetchVirtualMemory|SetDefaultDllDirectories|AddDllDirectory|GetCurrentPackageId|CreateRemoteThreadEx|SetProcessDpiAwarenessContext|GetDpiForMonitor|ProcessPrng|MFCreateDXGIDeviceManager|MFCreateDXGISurfaceBuffer'
  bad="$bad"$'\n'"$(echo "$dump" | grep -Eo "[[:space:]]($win8)\$" | tr -d '[:space:]' | sed 's/^/function /' || true)"
fi
bad="$(echo "$bad" | sed '/^$/d')"
if [ -n "$bad" ]; then
  echo "$exe imports what ${RC_TARGET:-Windows} does not ship:" >&2
  while IFS= read -r dll; do [ -n "$dll" ] && echo "  $dll" >&2; done <<<"$bad"
  exit 1
fi
echo "$exe: imports only system DLLs ($(echo "$dlls" | wc -l | tr -d ' '))${RC_TARGET:+, all on $RC_TARGET}"

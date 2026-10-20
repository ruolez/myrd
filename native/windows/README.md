# Windows native backend

Implements `native/include/rc_native.h` in C++17 (Win32 threads and locks
only, so the same sources build with MSVC and mingw-w64's win32 thread model).

| Concern | Implementation |
|---|---|
| Capture | DXGI Desktop Duplication (`DuplicateOutput1` → BGRA, falls back to `DuplicateOutput`). The capture thread follows the input desktop (`OpenInputDesktop` + `SetThreadDesktop`) and re-creates the duplication on `ACCESS_LOST`, so UAC prompts and the lock screen stay visible. With `RC_HAVE_WGC` (MSVC build) Windows.Graphics.Capture is used on the normal desktop (`IsCursorCaptureEnabled(false)`, `IsBorderRequired(false)`), switching to DXGI while a secure desktop is up. Static screens produce no frames; a keyframe request re-encodes the last frame. |
| Convert | `ID3D11VideoProcessor` BGRA → NV12 (BT.709, limited range) on the GPU, optional downscale to `max_width`/`max_height`, ring of 4 NV12 textures. |
| Encode | Media Foundation H.264 MFT on the same D3D11 device. Hardware async MFTs first (`MFT_ENUM_FLAG_HARDWARE`, `MF_TRANSFORM_ASYNC_UNLOCK`, event thread for `METransformNeedInput/HaveOutput`) → `mf-hw`; synchronous software MFT fallback (staging readback) → `mf-sw`. Low-latency mode, CBR, live `AVEncCommonMeanBitRate`, GOP from `keyframe_interval_ms`, no B-frames, `AVEncVideoForceKeyFrame` on PLI. Output is Annex-B; `MF_MT_MPEG_SEQUENCE_HEADER` is prepended to IDRs that lack SPS/PPS. A mid-session encoder failure recreates it with the software MFT. `mfplat.dll` is loaded at runtime, so the agent still starts on Windows N without the Media Feature Pack (capture returns `RC_ERR_UNSUPPORTED`). |
| Cursor | `GetCursorInfo` polled at ~120 Hz on its own thread; image from `GetIconInfo` (32-bit alpha, colour+mask, monochrome AND/XOR) → RGBA, id = FNV-1a of pixels. Never composited into the video. |
| Input | `SendInput`: absolute moves mapped into the monitor rect over `MOUSEEVENTF_VIRTUALDESK`, X buttons, wheel/hwheel with pixel → `WHEEL_DELTA` accumulation (100 px per notch), keys by set-1 scancode (`KEYEVENTF_SCANCODE` + `EXTENDEDKEY`; Pause uses the sentinel `0xE11D45` → `VK_PAUSE`), text via `KEYEVENTF_UNICODE`. |
| Clipboard | `CF_UNICODETEXT`. |
| Indicator | Per-monitor layered, topmost, click-through windows drawing a red border, `WDA_EXCLUDEFROMCAPTURE` so the controller does not see it; own UI thread. |
| Wallpaper | `rc_wallpaper_hide`: `SPI_SETDESKWALLPAPER` `""` / `NULL` without `SPIF_UPDATEINIFILE` (session only, logoff restores it), impersonating the console user (`WTSQueryUserToken`) so the reload reads their hive. `rc_init` reloads a wallpaper a crashed helper left off. |
| Consent | Topmost window on the input desktop with Deny / Allow once / Allow and remember and a countdown; returns 0/1/2. |
| Permissions | Always granted (no TCC equivalent; the helper runs as SYSTEM). |
| DPI | Per-monitor-aware v2, so every coordinate is physical pixels. |

## Builds

| Variant | Command | Output | Go link |
|---|---|---|---|
| mingw (default, CI smoke, v1 release) | `make -f Makefile.mingw` (or `native/windows/xbuild.sh` from macOS/Linux via Docker) | `build/librcnative.a` | `flags_windows.go`, static, exe imports only system DLLs |
| MSVC + WGC | `cmake -S native/windows -B native/windows/build/cmake -A x64 && cmake --build ... --config Release` | `build/msvc/rcnative.dll` + `rcnative.lib` | `go build -tags rcnative_msvc` (`flags_windows_msvc.go`); ship `rcnative.dll` next to the exe |

cgo on Windows always links with gcc (mingw), which cannot statically link
MSVC C++ objects, hence the DLL for the MSVC variant (plain C ABI, exports in
`rcnative.def`). `packaging/windows/build.ps1 -Native msvc` handles it.

## Process model (agent/internal/winsvc)

- `RemoteAgent` service (LocalSystem, `remote-agent.exe service`): enrollment
  (one-time `HKLM\SOFTWARE\RemoteAgent\EnrollToken` from `msiexec ENROLL_TOKEN=…`),
  DPAPI device key, signaling WebSocket.
- `remote-agent.exe helper` (the plan's `agent-session.exe`; one binary with a
  subcommand): spawned as SYSTEM into the active console session on
  `winsta0\default` (`WTSGetActiveConsoleSessionId`, `TokenSessionId`,
  `CreateProcessAsUser`), moved on console session change, restarted with
  backoff on crash. Runs `session.Manager`, the native backend and the tray.
- IPC: two one-way named pipes `\\.\pipe\remotectl-agent-s2h` / `-h2s`
  (synchronous pipe handles serialize reads and writes, so a single duplex
  pipe deadlocks), DACL `SYSTEM` only, length-prefixed JSON frames.

## Status

Compiles and links (mingw-w64 cross build) but has **not been run on
Windows**. First things to verify on real hardware: hardware MFT
negotiation on Intel/AMD/NVIDIA, SPS/PPS placement, DXGI re-creation across
UAC/lock, the helper spawn as SYSTEM, `WDA_EXCLUDEFROMCAPTURE` with
duplication, the WGC path under MSVC (never compiled here), and that hiding
and reloading the wallpaper as SYSTEM-impersonating-the-user brings per-monitor
and slideshow wallpapers back as they were.

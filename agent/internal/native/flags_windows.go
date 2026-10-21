//go:build windows && cgo && !rcnative_msvc

package native

// Links the mingw-w64 static backend built by native/windows/Makefile.mingw
// (DXGI Desktop Duplication + Media Foundation). libstdc++ and the
// winpthread it pulls in are linked statically so remote-agent.exe has no
// MinGW runtime DLL dependency (a missing libwinpthread-1.dll stops the
// service from starting), and mfplat.dll is loaded at runtime (Windows N
// editions lack it). native/windows/check-imports.sh enforces this.

/*
#cgo windows LDFLAGS: -L${SRCDIR}/../../../native/windows/build -lrcnative -lvpx
#cgo windows LDFLAGS: -ld3d11 -ldxgi -lmfuuid -lole32 -loleaut32 -luser32 -lgdi32 -lwinmm -lwtsapi32 -ladvapi32
#cgo windows LDFLAGS: -Wl,-Bstatic -lstdc++ -lwinpthread -Wl,-Bdynamic
*/
import "C"

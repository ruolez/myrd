//go:build windows && cgo && rcnative_msvc

package native

// Links rcnative.dll built with MSVC by native/windows/CMakeLists.txt
// (adds the Windows.Graphics.Capture path). The DLL must ship next to
// remote-agent.exe; build with `go build -tags rcnative_msvc`.

/*
#cgo windows LDFLAGS: -L${SRCDIR}/../../../native/windows/build/msvc -lrcnative
*/
import "C"

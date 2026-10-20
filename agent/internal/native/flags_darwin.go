//go:build darwin && cgo

package native

// Links the macOS backend built by native/macos/build.sh. Go's linker targets
// macOS 13.0; build with CGO_CFLAGS="-O2 -g -mmacosx-version-min=13.0" so cgo
// objects match it (packaging/macos/build-app.sh does).

/*
#cgo darwin LDFLAGS: -L${SRCDIR}/../../../native/macos/build -lrcnative
#cgo darwin LDFLAGS: -framework ScreenCaptureKit -framework VideoToolbox -framework CoreMedia -framework CoreVideo
#cgo darwin LDFLAGS: -framework CoreGraphics -framework ApplicationServices -framework AppKit -framework Carbon
#cgo darwin LDFLAGS: -framework Foundation -framework Security
*/
import "C"

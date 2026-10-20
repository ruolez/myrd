//go:build cgo

package native

/*
#include <stdint.h>
*/
import "C"

import (
	"runtime/cgo"
	"unsafe"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

func sinkFor(user unsafe.Pointer) platform.CaptureSink {
	defer func() { _ = recover() }() // handle already deleted: drop the late callback
	s, _ := cgo.Handle(uintptr(user)).Value().(platform.CaptureSink)
	return s
}

//export rcGoFrame
func rcGoFrame(user unsafe.Pointer, data *C.uint8_t, n C.int32_t, pts C.int64_t, key C.int32_t) {
	if s := sinkFor(user); s != nil && n > 0 {
		s.Frame(C.GoBytes(unsafe.Pointer(data), C.int(n)), int64(pts), key != 0)
	}
}

//export rcGoCursorShape
func rcGoCursorShape(user unsafe.Pointer, id C.uint32_t, w, h, hx, hy C.uint16_t, rgba *C.uint8_t, n C.int32_t) {
	if s := sinkFor(user); s != nil && n > 0 {
		s.CursorShape(uint32(id), uint16(w), uint16(h), uint16(hx), uint16(hy), C.GoBytes(unsafe.Pointer(rgba), C.int(n)))
	}
}

//export rcGoCursorPos
func rcGoCursorPos(user unsafe.Pointer, x, y C.uint16_t, visible C.uint8_t) {
	if s := sinkFor(user); s != nil {
		s.CursorPos(uint16(x), uint16(y), visible != 0)
	}
}

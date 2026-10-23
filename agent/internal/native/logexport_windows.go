//go:build cgo

package native

// #include <stdlib.h>
import "C"

// Exported apart from log_windows.go: a file with //export may not define C
// functions in its preamble.

//export rcGoLog
func rcGoLog(line *C.char) {
	if log := nativeLog.Load(); log != nil {
		log.Info("native", "line", C.GoString(line))
	}
}

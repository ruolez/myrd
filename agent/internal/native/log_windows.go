//go:build cgo

package native

/*
#include "rc_native.h"
extern void rcGoLog(char *line);
static void rc_set_go_log(int on) { rc_set_log(on ? (rc_log_fn)rcGoLog : NULL); }
*/
import "C"

import (
	"log/slog"
	"sync/atomic"
)

var nativeLog atomic.Pointer[slog.Logger]

// SetLogger sends the Windows backend's diagnostics (capture and encoder
// failures, the pipeline it fell back to) to log; nil stops them.
func SetLogger(log *slog.Logger) {
	nativeLog.Store(log)
	on := C.int(0)
	if log != nil {
		on = 1
	}
	C.rc_set_go_log(on)
}

//go:build cgo && !windows

package native

/*
#include "rc_native.h"
*/
import "C"

func screenLocked() bool { return C.rc_screen_locked() == 1 }

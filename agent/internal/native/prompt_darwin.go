//go:build cgo

package native

/*
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

import "github.com/ruolez/remotectl/agent/internal/platform"

// unlockPrompt: macOS shows its password field whenever the display is on;
// a sleeping display swallows the first key that wakes it.
func unlockPrompt() (platform.UnlockPrompt, string) {
	if C.CGDisplayIsAsleep(C.CGMainDisplayID()) != 0 {
		return platform.PromptHidden, "display asleep"
	}
	return platform.PromptReady, ""
}

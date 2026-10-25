//go:build cgo && !windows && !darwin

package native

import "github.com/ruolez/remotectl/agent/internal/platform"

func unlockPrompt() (platform.UnlockPrompt, string) { return platform.PromptUnknown, "" }

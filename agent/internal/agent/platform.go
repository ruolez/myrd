package agent

import "context"

// Platform hooks, set by platform_<os>.go init functions.
var (
	// promptEnrollToken asks the local user for an enrollment token when the
	// agent starts unenrolled (macOS first run), showing message. nil = no
	// native prompt.
	promptEnrollToken func(ctx context.Context, message string) (token string, ok bool)
	// uninstallAction backs the tray's Uninstall item. nil hides it.
	uninstallAction func()
	// requestPermissionsOnStart triggers the OS permission prompts for any
	// missing screen/input permission once at startup.
	requestPermissionsOnStart bool
	// restartIntoUpdate makes the service manager run the freshly installed
	// build when this process outlived its own update. nil = nothing to do.
	restartIntoUpdate func()
	// restartDevice and restartAgent back control `power` (session.Options
	// Restart / RestartAgent): each schedules its restart a moment out. nil
	// = not offered.
	restartDevice func() error
	restartAgent  func() error
)

package files

// LocalHost is not used on Windows (the service impersonates the user via
// winsvc.UserFS); it falls back to home only.
func volumeRoots() []Root { return nil }

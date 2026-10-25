package capproc

import "os"

// TakeStdout returns the worker's stdout for the wire. The Windows native
// library logs through rc_set_log and stderr, never stdout.
func TakeStdout() (*os.File, error) { return os.Stdout, nil }

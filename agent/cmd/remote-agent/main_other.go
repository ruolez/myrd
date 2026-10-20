//go:build !windows

package main

// platformMain lets an OS take over argument handling (Windows service and
// session-helper modes live in main_windows.go).
func platformMain(args []string) bool { return false }

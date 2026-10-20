package main

import "errors"

var errAlreadyRunning = errors.New("another copy of the agent is already running")

// singleInstance: the Windows service manager already runs one service, and
// the service one helper.
func singleInstance(string) (func(), error) { return func() {}, nil }

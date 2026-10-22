//go:build !win7

// Package buildinfo describes what this agent build is, beyond GOOS/GOARCH.
package buildinfo

import "runtime"

// Arch is the release arch this build reports when it enrolls and installs
// updates for (proto.ReleaseArches).
const Arch = runtime.GOARCH

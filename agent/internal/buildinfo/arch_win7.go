//go:build win7

package buildinfo

// Arch: the Windows 7 build (patched Go toolchain, GDI capture) is a release
// arch of its own, so it is only ever offered Windows 7 releases.
const Arch = "amd64-win7"

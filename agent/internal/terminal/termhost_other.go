//go:build !windows

package terminal

import (
	"fmt"
	"os"
)

// RunTermHost is `remote-agent term-host`, a Windows terminal child.
func RunTermHost([]string) int {
	fmt.Fprintln(os.Stderr, "term-host: Windows only")
	return 2
}

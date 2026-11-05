package main

import (
	"io"
	"os"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ruolez/remotectl/agent/internal/winsvc"
)

func dialShared(string) (io.ReadWriteCloser, error) {
	var sid uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid); err != nil {
		return nil, err
	}
	return os.OpenFile(winsvc.SharedPipe(sid), os.O_RDWR, 0)
}

func consoleSize() (int, int) {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info) != nil {
		return 80, 24
	}
	return int(info.Window.Right-info.Window.Left) + 1, int(info.Window.Bottom-info.Window.Top) + 1
}

// rawConsole turns off line editing and echo and switches the console to
// VT input and output.
func rawConsole() (func(), error) {
	in, out := windows.Handle(os.Stdin.Fd()), windows.Handle(os.Stdout.Fd())
	var inMode, outMode uint32
	if err := windows.GetConsoleMode(in, &inMode); err != nil {
		return nil, err
	}
	_ = windows.GetConsoleMode(out, &outMode)
	raw := inMode&^(windows.ENABLE_ECHO_INPUT|windows.ENABLE_LINE_INPUT|windows.ENABLE_PROCESSED_INPUT) |
		windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(in, raw); err != nil {
		return nil, err
	}
	_ = windows.SetConsoleMode(out, outMode|windows.ENABLE_PROCESSED_OUTPUT|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	return func() {
		_ = windows.SetConsoleMode(in, inMode)
		_ = windows.SetConsoleMode(out, outMode)
	}, nil
}

func watchSize(stop <-chan struct{}, cols, rows int, resize func(cols, rows int)) {
	for {
		select {
		case <-stop:
			return
		case <-time.After(300 * time.Millisecond):
		}
		if c, r := consoleSize(); c != cols || r != rows {
			cols, rows = c, r
			resize(c, r)
		}
	}
}

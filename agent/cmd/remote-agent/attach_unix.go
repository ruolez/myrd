//go:build !windows

package main

import (
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/ruolez/remotectl/agent/internal/agent"
	"github.com/ruolez/remotectl/agent/internal/config"
)

func dialShared(addr string) (io.ReadWriteCloser, error) {
	if addr == "" {
		addr = agent.SharedSocket(config.FromEnv().DataDir)
	}
	return net.Dial("unix", addr)
}

func consoleSize() (int, int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 {
		return 80, 24
	}
	return int(ws.Col), int(ws.Row)
}

// rawConsole puts the terminal in raw mode, like cfmakeraw.
func rawConsole() (func(), error) {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}

func watchSize(stop <-chan struct{}, _, _ int, resize func(cols, rows int)) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-stop:
			return
		case <-ch:
			resize(consoleSize())
		}
	}
}

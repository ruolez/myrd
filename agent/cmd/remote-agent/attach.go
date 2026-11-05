package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/ruolez/remotectl/agent/internal/terminal"
)

// cmdAttach is `remote-agent attach [shell:N]`: the person at the device
// opens a shell the agent keeps (a new one, or shell:N) in this terminal,
// where remote viewers can join it. -l lists the shells to join.
func cmdAttach(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	addr := fs.String("socket", "", "the agent's shared terminal socket (default: in the data dir)")
	list := fs.Bool("l", false, "list the shells you can join")
	_ = fs.Parse(args)
	conn, err := dialShared(*addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent attach: the agent is not reachable:", err)
		return 1
	}
	defer conn.Close()
	if *list {
		_ = terminal.WriteResize(conn, 80, 24)
		_ = terminal.WriteOpen(conn, terminal.ListShared)
		_, _ = io.Copy(os.Stdout, conn)
		return 0
	}
	cols, rows := consoleSize()
	restore, err := rawConsole()
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent attach: not a terminal:", err)
		return 1
	}
	var wmu sync.Mutex
	send := func(f func(io.Writer) error) error {
		wmu.Lock()
		defer wmu.Unlock()
		return f(conn)
	}
	_ = send(func(w io.Writer) error { return terminal.WriteResize(w, cols, rows) })
	_ = send(func(w io.Writer) error { return terminal.WriteOpen(w, fs.Arg(0)) })
	stop := make(chan struct{})
	go watchSize(stop, cols, rows, func(c, r int) {
		_ = send(func(w io.Writer) error { return terminal.WriteResize(w, c, r) })
	})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 && send(func(w io.Writer) error { return terminal.WriteInput(w, buf[:n]) }) != nil {
				return
			}
			if err != nil {
				_ = conn.Close()
				return
			}
		}
	}()
	_, err = io.Copy(os.Stdout, conn)
	close(stop)
	restore()
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(os.Stderr, "\r\nremote-agent attach:", err)
	}
	fmt.Fprintln(os.Stderr, "\r\n[shared terminal closed]")
	return 0
}

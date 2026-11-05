package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// SharedHost is a Host whose shells the person at the device can join
// from a terminal window of their own ("Open shared terminal",
// `remote-agent attach`).
type SharedHost interface {
	// NewShared starts a shell as the person at the device.
	NewShared(cols, rows int) (Stream, Source, error)
	// JoinShared joins a shell from SharedList.
	JoinShared(id string, cols, rows int) (Stream, Source, error)
	SharedList() []Source
}

// ListShared is the open frame payload that asks for SharedList.
const ListShared = "?"

// ServeShared serves local viewers from accept until it fails. A viewer
// sends its size, then which shell to open (WriteOpen), then input and
// size frames; it receives the terminal's output. allowed false refuses
// viewers ("Allow remote terminal" is off).
func ServeShared(accept func() (io.ReadWriteCloser, error), h SharedHost, allowed func() bool, log *slog.Logger) error {
	for {
		c, err := accept()
		if err != nil {
			return err
		}
		go func() {
			if err := serveShared(c, h, allowed); err != nil {
				log.Info("shared terminal viewer refused", "err", err)
			}
		}()
	}
}

func serveShared(c io.ReadWriteCloser, h SharedHost, allowed func() bool) error {
	defer c.Close()
	br := bufio.NewReader(c)
	cols, rows := 80, 24
	var id string
	for opened := false; !opened; {
		f, err := ReadFrame(br)
		if err != nil {
			return err
		}
		switch {
		case f.IsOpen():
			id, opened = string(f.Payload), true
		default:
			if cl, rw, ok := f.Size(); ok {
				cols, rows = cl, rw
			}
		}
	}
	refuse := func(err error) error {
		fmt.Fprintf(c, "remote-agent: %v\r\n", err)
		return err
	}
	if allowed != nil && !allowed() {
		return refuse(errors.New("remote terminals are turned off on this device"))
	}
	if id == ListShared {
		for _, s := range h.SharedList() {
			fmt.Fprintf(c, "%s\t%s\t%d attached\n", s.ID, strings.ReplaceAll(s.Title, "\t", " "), s.Attached)
		}
		return nil
	}
	var st Stream
	var err error
	if id == "" {
		st, _, err = h.NewShared(cols, rows)
	} else {
		st, _, err = h.JoinShared(id, cols, rows)
	}
	if err != nil {
		return refuse(err)
	}
	defer st.Detach()
	go func() {
		_, _ = io.Copy(c, st)
		_ = c.Close() // the shell ended
	}()
	return ServeFrames(br, st)
}

package capproc

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// WorkerOptions configures Serve.
type WorkerOptions struct {
	// Memory reports this process's memory in bytes (sysinfo.SelfMemory).
	Memory func() uint64
	// StatusEvery is how often the encoder name and memory are reported.
	// Default 1 s.
	StatusEvery time.Duration
}

// Serve is the capture worker: it runs the agent's capture commands from r
// on b and writes frames, cursor updates and replies to w. It returns when r
// ends (the agent closed it or exited), after stopping capture.
func Serve(r io.Reader, w io.Writer, b platform.Backend, o WorkerOptions) error {
	if o.StatusEvery <= 0 {
		o.StatusEvery = time.Second
	}
	out := &workerOut{w: bufio.NewWriterSize(w, 256<<10)}
	done := make(chan struct{})
	defer close(done)
	go out.status(b, o, done)

	br := bufio.NewReaderSize(r, 64<<10)
	for {
		kind, body, err := readMsg(br)
		if err != nil {
			b.StopCapture()
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
				return nil
			}
			return err
		}
		var c ctl
		if len(body) > 0 && json.Unmarshal(body, &c) != nil {
			continue
		}
		switch kind {
		case kindStart:
			if c.Config == nil {
				out.reply(c.Seq, errors.New("start without a configuration"), "")
				continue
			}
			err := b.StartCapture(*c.Config, out)
			out.reply(c.Seq, err, b.EncoderName())
		case kindStop:
			b.StopCapture() // no callbacks after it returns, so the reply is last
			out.reply(c.Seq, nil, "")
		case kindConfigure:
			b.ConfigureEncoder(c.Kbps, c.FPS)
		case kindKeyframe:
			b.RequestKeyframe()
		case kindRefine:
			out.reply(c.Seq, b.RefineEncoder(c.QP), "")
		}
	}
}

// workerOut serializes the worker's messages; it is also the CaptureSink, so
// frames are written from the native capture thread.
type workerOut struct {
	mu  sync.Mutex
	w   *bufio.Writer
	err error
}

func (o *workerOut) send(kind byte, body []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return
	}
	if o.err = writeMsg(o.w, kind, body); o.err == nil {
		o.err = o.w.Flush()
	}
}

func (o *workerOut) sendCtl(kind byte, c ctl) {
	body, err := json.Marshal(c)
	if err == nil {
		o.send(kind, body)
	}
}

func (o *workerOut) reply(seq uint32, err error, encoder string) {
	code, msg := errCode(err)
	o.sendCtl(kindReply, ctl{Seq: seq, Code: code, Msg: msg, Encoder: encoder})
}

func (o *workerOut) Frame(data []byte, pts int64, flags platform.FrameFlags) {
	o.send(kindFrame, frameBody(pts, flags, data))
}

func (o *workerOut) CursorShape(id uint32, w, h, hotX, hotY uint16, rgba []byte) {
	o.send(kindShape, shapeBody(id, w, h, hotX, hotY, rgba))
}

func (o *workerOut) CursorPos(x, y uint16, visible bool) {
	o.send(kindPos, posBody(x, y, visible))
}

// status sends the encoder name when it changes (the native side can fall
// back to another encoder mid-stream) and the worker's memory.
func (o *workerOut) status(b platform.Backend, opts WorkerOptions, done <-chan struct{}) {
	t := time.NewTicker(opts.StatusEvery)
	defer t.Stop()
	last := ""
	for {
		st := ctl{}
		if name := b.EncoderName(); name != last {
			st.Encoder, last = name, name
		}
		if opts.Memory != nil {
			st.Mem = opts.Memory()
		}
		if st.Encoder != "" || st.Mem != 0 {
			o.sendCtl(kindStatus, st)
		}
		select {
		case <-done:
			return
		case <-t.C:
		}
	}
}

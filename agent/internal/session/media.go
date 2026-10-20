package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/ruolez/remotectl/agent/internal/inputproto"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

const (
	floorKbps      = 1000
	initialKbps    = 3000
	maxCeilKbps    = 16000
	relayCeilKbps  = 6000
	ceil1080pKbps  = 8000
	keyframeGOPms  = 2000
	statsInterval  = 2 * time.Second
	cursorInterval = time.Second / 120
	clipboardPoll  = time.Second
	shapeResend    = 3 * time.Second
)

// Quality tiers from the input-channel Control op 3.
const (
	TierBackground = 0
	TierBalanced   = 1
	TierHigh       = 2
)

// CeilingKbps scales the 8 Mbps @1080p ceiling by pixel count; relay paths
// are capped at 6 Mbps.
func CeilingKbps(width, height uint32, relay bool) int {
	px := float64(width) * float64(height)
	c := int(ceil1080pKbps * px / (1920 * 1080))
	c = max(2000, min(c, maxCeilKbps))
	if relay {
		c = min(c, relayCeilKbps)
	}
	return c
}

// EncoderTarget turns a bandwidth estimate into encoder settings for a tier.
func EncoderTarget(bweKbps, ceilKbps, tier, maxFPS int) (kbps, fps int) {
	kbps = max(floorKbps, min(bweKbps, ceilKbps))
	switch tier {
	case TierBackground:
		return floorKbps, min(5, maxFPS)
	case TierBalanced:
		return kbps, min(30, maxFPS)
	default:
		return kbps, maxFPS
	}
}

// ScaledSize applies MaxWidth/MaxHeight (0 = unbounded) keeping aspect,
// rounded down to even dimensions for H.264.
func ScaledSize(w, h uint32, maxW, maxH int) (uint32, uint32) {
	scale := 1.0
	if maxW > 0 && int(w) > maxW {
		scale = float64(maxW) / float64(w)
	}
	if maxH > 0 && float64(h)*scale > float64(maxH) {
		scale = float64(maxH) / float64(h)
	}
	sw, sh := uint32(float64(w)*scale), uint32(float64(h)*scale)
	return sw &^ 1, sh &^ 1
}

type mediaPipe struct {
	s       *sess
	b       platform.Backend
	track   *webrtc.TrackLocalStaticSample
	maxFPS  int
	encoder string

	mu        sync.Mutex
	monitors  []platform.Monitor
	active    int
	tier      int
	bweKbps   int
	relay     bool
	applied   [2]int // kbps, fps last sent to the encoder
	running   bool
	stopped   bool
	control   *webrtc.DataChannel
	cursor    *webrtc.DataChannel
	lastPTS   int64
	sentShape map[uint32]bool
	curShape  []byte // full CursorShape message for periodic resend
	clipSeen  string
	clipSet   string // last value we wrote from the browser (avoid echo)
	clipXfer  uint16

	frames, keyframes, bytes atomic.Int64
	posX, posY               atomic.Uint32
	posVis                   atomic.Bool
	posDirty                 atomic.Bool
	quit                     chan struct{}
}

func newMediaPipe(s *sess, track *webrtc.TrackLocalStaticSample) *mediaPipe {
	b := s.m.o.Backend
	p := &mediaPipe{s: s, b: b, track: track, tier: TierHigh, bweKbps: initialKbps,
		sentShape: map[uint32]bool{}, quit: make(chan struct{})}
	p.encoder = b.EncoderName()
	p.maxFPS = s.m.o.MaxFPS
	if p.maxFPS == 0 {
		p.maxFPS = 60
		if p.encoder == "x264" || p.encoder == "openh264" || p.encoder == "mf-sw" {
			p.maxFPS = 30 // software fallback per the plan
		}
	}
	mons, err := b.Monitors()
	if err != nil {
		s.log.Warn("monitor list failed", "err", err)
	}
	p.monitors = mons
	for i, m := range mons {
		if m.Primary {
			p.active = i
		}
	}
	return p
}

func (p *mediaPipe) dims() (uint32, uint32) {
	if len(p.monitors) == 0 {
		return 1920, 1080
	}
	m := p.monitors[p.active]
	return ScaledSize(m.Width, m.Height, p.s.m.o.MaxWidth, p.s.m.o.MaxHeight)
}

// ---- encoder control ----

func (p *mediaPipe) attachEstimator(e cc.BandwidthEstimator) {
	e.OnTargetBitrateChange(func(bps int) {
		p.mu.Lock()
		p.bweKbps = bps / 1000
		p.mu.Unlock()
		p.applyEncoder(false)
	})
}

func (p *mediaPipe) setPath(path string) {
	p.mu.Lock()
	p.relay = path == proto.PathRelay
	p.mu.Unlock()
	p.applyEncoder(false)
}

// target computes current settings; caller holds p.mu.
func (p *mediaPipe) targetLocked() (int, int) {
	w, h := p.dims()
	return EncoderTarget(p.bweKbps, CeilingKbps(w, h, p.relay), p.tier, p.maxFPS)
}

func (p *mediaPipe) applyEncoder(force bool) {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	kbps, fps := p.targetLocked()
	prev := p.applied
	changed := force || fps != prev[1] || abs(kbps-prev[0])*10 > prev[0]
	if changed {
		p.applied = [2]int{kbps, fps}
	}
	p.mu.Unlock()
	if changed {
		p.b.ConfigureEncoder(kbps, fps)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---- lifecycle ----

func (p *mediaPipe) start() {
	if err := p.startCapture(); err != nil {
		code := "capture_failed"
		reason := proto.EndError
		if errors.Is(err, platform.ErrPermission) {
			code, reason = "permission_missing", proto.EndPermission
		}
		p.sendControl(map[string]any{"t": "error", "code": code, "detail": err.Error()})
		p.s.log.Error("capture start failed", "err", err)
		go p.s.end(reason, true)
		return
	}
	go p.loop()
}

func (p *mediaPipe) startCapture() error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return errors.New("session ended")
	}
	kbps, fps := p.targetLocked()
	cfg := platform.CaptureConfig{FPS: fps, BitrateKbps: kbps, KeyframeIntervalMS: keyframeGOPms,
		MaxWidth: p.s.m.o.MaxWidth, MaxHeight: p.s.m.o.MaxHeight}
	if len(p.monitors) > 0 {
		cfg.MonitorID = p.monitors[p.active].ID
	}
	p.applied = [2]int{kbps, fps}
	p.lastPTS = 0
	p.mu.Unlock()
	if err := p.b.StartCapture(cfg, p); err != nil {
		return err
	}
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	return nil
}

func (p *mediaPipe) stop(reason string) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	running := p.running
	p.running = false
	p.mu.Unlock()
	close(p.quit)
	p.sendControl(map[string]any{"t": "bye", "reason": reason})
	if running {
		p.b.StopCapture()
	}
}

func (p *mediaPipe) selectMonitor(idx int) error {
	p.mu.Lock()
	if idx < 0 || idx >= len(p.monitors) {
		p.mu.Unlock()
		return inputproto.ErrRange
	}
	if idx == p.active {
		p.mu.Unlock()
		p.b.RequestKeyframe()
		return nil
	}
	running := p.running
	p.active = idx
	p.running = false
	p.mu.Unlock()
	if running {
		p.b.StopCapture()
	}
	if err := p.startCapture(); err != nil {
		p.sendControl(map[string]any{"t": "error", "code": "capture_failed", "detail": err.Error()})
		return nil
	}
	w, h := p.dims()
	p.sendControl(map[string]any{"t": "monitor", "active": idx, "width": w, "height": h})
	return nil
}

func (p *mediaPipe) setTier(t int) {
	p.mu.Lock()
	p.tier = t
	p.mu.Unlock()
	p.applyEncoder(true)
}

// loop runs the periodic work while the session lives.
func (p *mediaPipe) loop() {
	stats := time.NewTicker(statsInterval)
	cursor := time.NewTicker(cursorInterval)
	clip := time.NewTicker(clipboardPoll)
	shape := time.NewTicker(shapeResend)
	defer stats.Stop()
	defer cursor.Stop()
	defer clip.Stop()
	defer shape.Stop()
	if t, err := p.b.ClipboardText(); err == nil {
		p.mu.Lock()
		p.clipSeen = t
		p.mu.Unlock()
	}
	var lastBytes, lastFrames int64
	for {
		select {
		case <-p.quit:
			return
		case <-cursor.C:
			if p.posDirty.Swap(false) {
				p.sendCursor(inputproto.EncodeCursorPos(uint16(p.posX.Load()), uint16(p.posY.Load()), p.posVis.Load()))
			}
		case <-stats.C:
			b, f := p.bytes.Load(), p.frames.Load()
			secs := statsInterval.Seconds()
			w, h := p.dims()
			p.sendControl(map[string]any{"t": "stats", "bitrate_kbps": int(float64(b-lastBytes) * 8 / 1000 / secs),
				"fps": float64(f-lastFrames) / secs, "encoder": p.encoder, "width": w, "height": h,
				"keyframes": p.keyframes.Load()})
			lastBytes, lastFrames = b, f
		case <-shape.C:
			p.mu.Lock()
			msg := p.curShape
			p.mu.Unlock()
			if msg != nil {
				p.sendCursor(msg)
			}
		case <-clip.C:
			p.pollClipboard()
		}
	}
}

func (p *mediaPipe) pollClipboard() {
	t, err := p.b.ClipboardText()
	if err != nil {
		return
	}
	p.mu.Lock()
	if t == p.clipSeen || t == p.clipSet || len(t) > inputproto.MaxClipboard {
		p.clipSeen = t
		p.mu.Unlock()
		return
	}
	p.clipSeen = t
	p.clipXfer++
	xfer := p.clipXfer
	dc := p.control
	p.mu.Unlock()
	if dc == nil {
		return
	}
	for _, f := range inputproto.ClipboardChunks(xfer, inputproto.MimeText, []byte(t)) {
		if dc.Send(f) != nil {
			return
		}
	}
}

// setRemoteClipboard is called with text from the browser.
func (p *mediaPipe) setRemoteClipboard(t string) {
	p.mu.Lock()
	p.clipSet, p.clipSeen = t, t
	p.mu.Unlock()
	if err := p.b.SetClipboardText(t); err != nil {
		p.s.log.Warn("set clipboard failed", "err", err)
	}
}

// ---- channels ----

func (p *mediaPipe) setControlChannel(dc *webrtc.DataChannel) {
	p.mu.Lock()
	p.control = dc
	mons := make([]proto.Monitor, len(p.monitors))
	for i, m := range p.monitors {
		w, h := ScaledSize(m.Width, m.Height, p.s.m.o.MaxWidth, p.s.m.o.MaxHeight)
		mons[i] = proto.Monitor{ID: m.ID, Name: m.Name, X: m.X, Y: m.Y, Width: w, Height: h, Scale: m.Scale, Primary: m.Primary}
	}
	_, fps := p.targetLocked()
	active := p.active
	p.mu.Unlock()
	perm := p.b.Permissions()
	p.sendControl(map[string]any{"t": "hello", "monitors": mons, "active": active, "encoder": p.encoder,
		"codec": "h264", "fps": fps, "permissions": proto.Permissions{Screen: perm.Screen, Input: perm.Input},
		"agent_version": p.s.m.o.AgentVersion, "os": runtime.GOOS})
}

func (p *mediaPipe) setCursorChannel(dc *webrtc.DataChannel) {
	dc.OnOpen(func() {
		p.mu.Lock()
		p.cursor = dc
		msg := p.curShape
		p.mu.Unlock()
		if msg != nil {
			p.sendCursor(msg)
		}
	})
}

func (p *mediaPipe) sendControl(v any) {
	p.mu.Lock()
	dc := p.control
	p.mu.Unlock()
	if dc == nil || dc.ReadyState() != webrtc.DataChannelStateOpen {
		return
	}
	b, err := json.Marshal(v)
	if err == nil {
		_ = dc.SendText(string(b))
	}
}

func (p *mediaPipe) sendCursor(b []byte) {
	p.mu.Lock()
	dc := p.cursor
	p.mu.Unlock()
	if dc != nil && dc.ReadyState() == webrtc.DataChannelStateOpen {
		_ = dc.Send(b)
	}
}

type controlMsg struct {
	T  string `json:"t"`
	ID int64  `json:"id"`
	TS int64  `json:"ts"`
}

func (p *mediaPipe) onControlText(b []byte) {
	var m controlMsg
	if json.Unmarshal(b, &m) != nil {
		return
	}
	if m.T == "ping" {
		p.sendControl(map[string]any{"t": "pong", "id": m.ID, "ts": m.TS})
	}
}

// ---- platform.CaptureSink (native threads) ----

func (p *mediaPipe) Frame(data []byte, pts int64, keyframe bool) {
	p.mu.Lock()
	dur := time.Second / time.Duration(max(p.applied[1], 1))
	if p.lastPTS != 0 && pts > p.lastPTS {
		if d := time.Duration(pts-p.lastPTS) * time.Microsecond; d < time.Second {
			dur = d
		}
	}
	p.lastPTS = pts
	p.mu.Unlock()
	p.frames.Add(1)
	p.bytes.Add(int64(len(data)))
	if keyframe {
		p.keyframes.Add(1)
	}
	_ = p.track.WriteSample(media.Sample{Data: data, Duration: dur})
}

func (p *mediaPipe) CursorShape(id uint32, w, h, hotX, hotY uint16, rgba []byte) {
	full := encodeShape(id, w, h, hotX, hotY, rgba)
	if full == nil {
		return
	}
	p.mu.Lock()
	known := p.sentShape[id]
	p.sentShape[id] = true
	p.curShape = full // re-sent periodically so a lost copy on the lossy channel heals
	p.mu.Unlock()
	if known {
		p.sendCursor(inputproto.EncodeCursorShape(id, w, h, hotX, hotY, nil))
		return
	}
	p.sendCursor(full)
}

func encodeShape(id uint32, w, h, hotX, hotY uint16, rgba []byte) []byte {
	if w == 0 || h == 0 || len(rgba) < int(w)*int(h)*4 {
		return nil
	}
	img := &image.NRGBA{Pix: append([]byte(nil), rgba[:int(w)*int(h)*4]...), Stride: int(w) * 4,
		Rect: image.Rect(0, 0, int(w), int(h))}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		return nil
	}
	return inputproto.EncodeCursorShape(id, w, h, hotX, hotY, buf.Bytes())
}

func (p *mediaPipe) CursorPos(x, y uint16, visible bool) {
	p.posX.Store(uint32(x))
	p.posY.Store(uint32(y))
	p.posVis.Store(visible)
	p.posDirty.Store(true)
}

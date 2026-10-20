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
	maxCeilKbps    = proto.StreamMaxKbps // manual caps; also the GCC maximum
	autoCeilKbps   = 16000               // automatic ceiling (pixel-scaled)
	relayCeilKbps  = 6000
	ceil1080pKbps  = 8000
	keyframeGOPms  = 2000
	keyGuardAge    = 2 * time.Second // re-request a keyframe when the encoder's own GOP runs longer
	keyGuardFrames = 10
	statsInterval  = 2 * time.Second
	autoInterval   = time.Second
	cursorInterval = time.Second / 120
	clipboardPoll  = time.Second
	shapeResend    = 3 * time.Second
)

// Quality tiers from the input-channel Control op 3 (legacy browsers).
const (
	TierBackground = 0
	TierBalanced   = 1
	TierHigh       = 2
)

// CeilingKbps scales the 8 Mbps @1080p ceiling by pixel count (at most
// 16 Mbps); relay paths are capped at 6 Mbps.
func CeilingKbps(width, height uint32, relay bool) int {
	px := float64(width) * float64(height)
	c := int(ceil1080pKbps * px / (1920 * 1080))
	c = max(2000, min(c, autoCeilKbps))
	if relay {
		c = min(c, relayCeilKbps)
	}
	return c
}

// EncoderTarget turns a bandwidth estimate into the encoder bitrate.
func EncoderTarget(bweKbps, ceilKbps int) int {
	return max(floorKbps, min(bweKbps, ceilKbps))
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

// encoderState is what the encoder was last told.
type encoderState struct {
	kbps, fps int
	w, h      uint32
}

type mediaPipe struct {
	s     *sess
	b     platform.Backend
	track *webrtc.TrackLocalStaticSample
	now   func() time.Time

	capMu  sync.Mutex // serializes capture start / stop / restart
	encMu  sync.Mutex // keeps ConfigureEncoder calls in plan order
	kickCh chan struct{}

	mu         sync.Mutex
	encoder    string
	maxFPS     int
	monitors   []platform.Monitor
	active     int
	settings   StreamSettings
	background bool
	wallpaper  bool // the browser asked to see the wallpaper
	canHideWP  bool // the backend can hide it
	auto       AutoState
	ladder     []Rung
	est        cc.BandwidthEstimator
	bweKbps    int
	relay      bool
	applied    encoderState
	running    bool
	stopped    bool
	reply      bool    // a `stream` request awaits its state reply
	replyID    *uint32 // id of the latest request
	lastState  []byte  // last stream state sent (without id)
	control    *webrtc.DataChannel
	cursor     *webrtc.DataChannel
	lastPTS    int64
	lastKey    time.Time
	sinceKey   int
	keyPending bool
	sentShape  map[uint32]bool
	curShape   []byte // full CursorShape message for periodic resend
	clipSeen   string
	clipSet    string // last value we wrote from the browser (avoid echo)
	clipXfer   uint16

	wpHidden bool // hidden on the device right now; guarded by capMu

	frames, keyframes, bytes atomic.Int64
	posX, posY               atomic.Uint32
	posVis                   atomic.Bool
	posDirty                 atomic.Bool
	quit                     chan struct{}
}

func newMediaPipe(s *sess, track *webrtc.TrackLocalStaticSample) *mediaPipe {
	b := s.m.o.Backend
	p := &mediaPipe{s: s, b: b, track: track, now: time.Now, settings: DefaultStreamSettings(), bweKbps: initialKbps,
		sentShape: map[uint32]bool{}, kickCh: make(chan struct{}, 1), quit: make(chan struct{})}
	p.encoder = b.EncoderName()
	p.maxFPS = p.fpsCap(p.encoder)
	p.canHideWP = b.HideWallpaper(false) == nil
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
	p.resetAutoLocked()
	go p.reconcileLoop()
	return p
}

// fpsCap is Options.MaxFPS when set, else the encoder's cap (live: Windows
// can fall back to a software encoder mid-session).
func (p *mediaPipe) fpsCap(encoder string) int {
	if p.s.m.o.MaxFPS > 0 {
		return p.s.m.o.MaxFPS
	}
	return EncoderMaxFPS(encoder)
}

// nativeLocked is the active monitor's size in physical pixels.
func (p *mediaPipe) nativeLocked() (uint32, uint32) {
	if len(p.monitors) == 0 {
		return 1920, 1080
	}
	m := p.monitors[p.active]
	return m.Width, m.Height
}

func (p *mediaPipe) inputLocked() StreamInput {
	nw, nh := p.nativeLocked()
	in := StreamInput{Settings: p.settings, Background: p.background, Wallpaper: p.wallpaper, MaxFPS: p.maxFPS, NativeW: nw, NativeH: nh,
		MaxW: p.s.m.o.MaxWidth, MaxH: p.s.m.o.MaxHeight, Relay: p.relay, BweKbps: p.bweKbps}
	if p.settings.Preset == proto.PresetAuto && len(p.ladder) > 0 {
		r := p.ladder[max(0, min(p.auto.Rung, len(p.ladder)-1))]
		in.Rung = &r
	}
	return in
}

func (p *mediaPipe) planLocked() StreamPlan { return PlanStream(p.inputLocked()) }

// resetAutoLocked rebuilds Auto's ladder from the current ceilings and starts
// over at the top (settings, monitor or encoder cap changed).
func (p *mediaPipe) resetAutoLocked() {
	r := ResolveSettings(p.settings, p.maxFPS)
	nw, nh := p.nativeLocked()
	p.ladder = AutoLadder(r.FPS, r.Res, nw, nh, p.s.m.o.MaxWidth, p.s.m.o.MaxHeight)
	p.auto = NewAutoState(p.now())
}

// ---- encoder control ----

func (p *mediaPipe) attachEstimator(e cc.BandwidthEstimator) {
	p.mu.Lock()
	p.est = e
	p.mu.Unlock()
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
	p.kick()
}

func (p *mediaPipe) applyEncoder(force bool) {
	p.encMu.Lock()
	defer p.encMu.Unlock()
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	plan := p.planLocked()
	prev := p.applied
	changed := force || plan.FPS != prev.fps || abs(plan.Kbps-prev.kbps)*10 > prev.kbps
	if changed {
		p.applied.kbps, p.applied.fps = plan.Kbps, plan.FPS
	}
	p.mu.Unlock()
	if changed {
		p.b.ConfigureEncoder(plan.Kbps, plan.FPS)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// kick schedules a reconcile; kicks coalesce.
func (p *mediaPipe) kick() {
	select {
	case p.kickCh <- struct{}{}:
	default:
	}
}

func (p *mediaPipe) reconcileLoop() {
	for {
		select {
		case <-p.quit:
			return
		case <-p.kickCh:
			p.reconcile()
		}
	}
}

// reconcile brings the encoder in line with the current plan: a new size
// restarts capture, anything else is a reconfigure. Then it reports the
// stream state if it changed (or a request awaits its reply).
func (p *mediaPipe) reconcile() {
	p.capMu.Lock()
	p.mu.Lock()
	plan := p.planLocked()
	restart := p.running && (plan.W != p.applied.w || plan.H != p.applied.h)
	p.mu.Unlock()
	var err error
	if restart {
		err = p.restartLocked()
	} else {
		p.applyEncoder(true)
	}
	p.mu.Lock()
	running := p.running
	p.mu.Unlock()
	p.syncWallpaperLocked(running)
	p.capMu.Unlock()
	if err != nil {
		p.s.log.Error("capture restart failed", "err", err)
		p.sendControl(map[string]any{"t": "error", "code": "capture_failed", "detail": err.Error()})
	}
	p.sendStreamState(false)
}

// ---- stream settings ----

// setStream applies a desired state; reply asks for a `stream` answer.
func (p *mediaPipe) setStream(s StreamSettings, background, wallpaper bool, id *uint32, reply bool) {
	p.mu.Lock()
	if s != p.settings {
		p.settings = s
		p.resetAutoLocked()
	} else if background != p.background {
		p.auto.Since = p.now() // back from background: warm up again
	}
	p.background = background
	p.wallpaper = wallpaper
	if reply {
		p.reply, p.replyID = true, id
	}
	p.mu.Unlock()
	p.kick()
}

func (p *mediaPipe) setTier(t int) {
	p.mu.Lock()
	s, bg := LegacyTierSettings(t, p.settings)
	wp := p.wallpaper
	p.mu.Unlock()
	p.setStream(s, bg, wp, nil, false)
}

func (p *mediaPipe) streamStateLocked() StreamState {
	in := p.inputLocked()
	var auto *AutoInfo
	if p.settings.Preset == proto.PresetAuto {
		auto = &AutoInfo{Rung: p.auto.Rung, Rungs: len(p.ladder), Reason: p.auto.Reason}
	}
	st := BuildStreamState(in, PlanStream(in), auto)
	st.OK = true
	return st
}

// sendStreamState sends the state when it changed since the last one, when a
// request awaits its reply, or when force is set (after hello).
func (p *mediaPipe) sendStreamState(force bool) {
	p.mu.Lock()
	st := p.streamStateLocked()
	b, _ := json.Marshal(st)
	send := force || p.reply || !bytes.Equal(b, p.lastState)
	st.ID = p.replyID
	p.lastState, p.reply, p.replyID = b, false, nil
	p.mu.Unlock()
	if send {
		p.sendControl(st)
	}
}

// rejectStream answers an invalid request with the unchanged state.
func (p *mediaPipe) rejectStream(id *uint32) {
	p.mu.Lock()
	st := p.streamStateLocked()
	p.mu.Unlock()
	st.ID, st.OK = id, false
	p.sendControl(st)
}

// refreshEncoder follows the encoder name (and so max_fps) during the session.
func (p *mediaPipe) refreshEncoder() {
	name := p.b.EncoderName()
	p.mu.Lock()
	if name == p.encoder {
		p.mu.Unlock()
		return
	}
	p.encoder = name
	fps := p.fpsCap(name)
	changed := fps != p.maxFPS
	if changed {
		p.maxFPS = fps
		p.resetAutoLocked()
	}
	p.mu.Unlock()
	if changed {
		p.kick()
	}
}

// estimatorStats reads loss (0..1) and the delay detector from GCC.
func estimatorStats(e cc.BandwidthEstimator) (loss float64, overuse bool) {
	if e == nil {
		return 0, false
	}
	st := e.GetStats()
	loss, _ = st["averageLoss"].(float64)
	return loss, st["usage"] == "overuse"
}

func (p *mediaPipe) sampleAuto(sendKbps int) {
	p.mu.Lock()
	est := p.est
	p.mu.Unlock()
	loss, overuse := estimatorStats(est)
	p.mu.Lock()
	if !p.running || p.background || p.settings.Preset != proto.PresetAuto {
		p.mu.Unlock()
		return
	}
	prev := p.auto
	p.auto = NextAuto(p.auto, p.now(), AutoSample{SendKbps: sendKbps, TargetKbps: p.applied.kbps, Loss: loss, Overuse: overuse}, p.ladder)
	changed := prev.Rung != p.auto.Rung || prev.Reason != p.auto.Reason
	p.mu.Unlock()
	if changed {
		p.kick()
	}
}

// ---- lifecycle ----

// syncWallpaperLocked hides the wallpaper while capture is active unless the
// browser asked to see it, and shows it otherwise; caller holds capMu.
func (p *mediaPipe) syncWallpaperLocked(active bool) {
	p.mu.Lock()
	hide := active && p.canHideWP && !p.wallpaper
	p.mu.Unlock()
	if hide == p.wpHidden {
		return
	}
	if err := p.b.HideWallpaper(hide); err != nil {
		p.s.log.Warn("wallpaper change failed", "hide", hide, "err", err)
		return
	}
	p.wpHidden = hide
}

func (p *mediaPipe) start() {
	p.capMu.Lock()
	p.syncWallpaperLocked(true) // before the first (most expensive) keyframe
	err := p.startCaptureLocked()
	p.capMu.Unlock()
	if err != nil {
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

// startCaptureLocked starts capture with the current plan; caller holds capMu.
func (p *mediaPipe) startCaptureLocked() error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return errors.New("session ended")
	}
	plan := p.planLocked()
	cfg := platform.CaptureConfig{FPS: plan.FPS, BitrateKbps: plan.Kbps, KeyframeIntervalMS: keyframeGOPms,
		MaxWidth: plan.BoxW, MaxHeight: plan.BoxH}
	if len(p.monitors) > 0 {
		cfg.MonitorID = p.monitors[p.active].ID
	}
	p.applied = encoderState{plan.Kbps, plan.FPS, plan.W, plan.H}
	p.lastPTS = 0
	p.lastKey, p.sinceKey, p.keyPending = p.now(), 0, false
	p.mu.Unlock()
	if err := p.b.StartCapture(cfg, p); err != nil {
		return err
	}
	p.mu.Lock()
	p.running = true
	p.mu.Unlock()
	p.refreshEncoder()
	return nil
}

// restartLocked stops and starts capture; caller holds capMu.
func (p *mediaPipe) restartLocked() error {
	p.mu.Lock()
	running := p.running
	p.running = false
	p.mu.Unlock()
	if running {
		p.b.StopCapture()
	}
	return p.startCaptureLocked()
}

func (p *mediaPipe) stop(reason string) {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()
	close(p.quit)
	p.sendControl(map[string]any{"t": "bye", "reason": reason})
	p.capMu.Lock()
	defer p.capMu.Unlock()
	p.mu.Lock()
	running := p.running
	p.running = false
	p.mu.Unlock()
	if running {
		p.b.StopCapture()
	}
	p.syncWallpaperLocked(false)
}

func (p *mediaPipe) selectMonitor(idx int) error {
	p.capMu.Lock()
	p.mu.Lock()
	if idx < 0 || idx >= len(p.monitors) {
		p.mu.Unlock()
		p.capMu.Unlock()
		return inputproto.ErrRange
	}
	if idx == p.active {
		p.mu.Unlock()
		p.capMu.Unlock()
		p.b.RequestKeyframe()
		return nil
	}
	p.active = idx
	p.resetAutoLocked()
	p.mu.Unlock()
	err := p.restartLocked()
	p.capMu.Unlock()
	if err != nil {
		p.sendControl(map[string]any{"t": "error", "code": "capture_failed", "detail": err.Error()})
		return nil
	}
	p.mu.Lock()
	w, h := p.nativeLocked()
	p.mu.Unlock()
	p.sendControl(map[string]any{"t": "monitor", "active": idx, "width": w, "height": h})
	p.sendStreamState(false)
	return nil
}

// loop runs the periodic work while the session lives.
func (p *mediaPipe) loop() {
	stats := time.NewTicker(statsInterval)
	auto := time.NewTicker(autoInterval)
	cursor := time.NewTicker(cursorInterval)
	clip := time.NewTicker(clipboardPoll)
	shape := time.NewTicker(shapeResend)
	defer stats.Stop()
	defer auto.Stop()
	defer cursor.Stop()
	defer clip.Stop()
	defer shape.Stop()
	if t, err := p.b.ClipboardText(); err == nil {
		p.mu.Lock()
		p.clipSeen = t
		p.mu.Unlock()
	}
	var lastBytes, lastFrames int64
	autoBytes := p.bytes.Load()
	for {
		select {
		case <-p.quit:
			return
		case <-cursor.C:
			if p.posDirty.Swap(false) {
				p.sendCursor(inputproto.EncodeCursorPos(uint16(p.posX.Load()), uint16(p.posY.Load()), p.posVis.Load()))
			}
		case <-auto.C:
			p.refreshEncoder()
			b := p.bytes.Load()
			p.sampleAuto(int(float64(b-autoBytes) * 8 / 1000 / autoInterval.Seconds()))
			autoBytes = b
		case <-stats.C:
			b, f := p.bytes.Load(), p.frames.Load()
			secs := statsInterval.Seconds()
			p.mu.Lock()
			a, bwe, enc, est := p.applied, p.bweKbps, p.encoder, p.est
			p.mu.Unlock()
			loss, _ := estimatorStats(est)
			p.sendControl(map[string]any{"t": "stats", "bitrate_kbps": int(float64(b-lastBytes) * 8 / 1000 / secs),
				"fps": float64(f-lastFrames) / secs, "encoder": enc, "width": a.w, "height": a.h,
				"keyframes": p.keyframes.Load(), "target_fps": a.fps, "target_kbps": a.kbps, "bwe_kbps": bwe, "loss": loss})
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
		mons[i] = proto.Monitor{ID: m.ID, Name: m.Name, X: m.X, Y: m.Y, Width: m.Width, Height: m.Height, Scale: m.Scale, Primary: m.Primary}
	}
	plan := p.planLocked()
	active, enc, maxFPS := p.active, p.encoder, p.maxFPS
	p.mu.Unlock()
	perm := p.b.Permissions()
	p.sendControl(map[string]any{"t": "hello", "monitors": mons, "active": active, "encoder": enc,
		"codec": "h264", "fps": plan.FPS, "max_fps": maxFPS, "features": helloFeatures(p.s.m.o, p.canHideWP),
		"permissions":   proto.Permissions{Screen: perm.Screen, Input: perm.Input},
		"agent_version": p.s.m.o.AgentVersion, "os": runtime.GOOS})
	p.sendStreamState(true)
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
	switch m.T {
	case "ping":
		p.sendControl(map[string]any{"t": "pong", "id": m.ID, "ts": m.TS})
	case "stream":
		r, err := proto.DecodeStreamRequest(b)
		if err != nil {
			p.rejectStream(r.ID) // not an input reject: no abuse count
			return
		}
		p.setStream(SettingsFromRequest(r), r.Background, r.Wallpaper, r.ID, true)
	}
}

// ---- platform.CaptureSink (native threads) ----

func (p *mediaPipe) Frame(data []byte, pts int64, keyframe bool) {
	p.mu.Lock()
	dur := time.Second / time.Duration(max(p.applied.fps, 1))
	if p.lastPTS != 0 && pts > p.lastPTS {
		if d := time.Duration(pts-p.lastPTS) * time.Microsecond; d < time.Second {
			dur = d
		}
	}
	p.lastPTS = pts
	// The native GOP is sized for 60 fps; at lower or damage-driven frame
	// rates it runs long, so ask for a keyframe to bound loss recovery.
	now, wantKey := p.now(), false
	if keyframe {
		p.lastKey, p.sinceKey, p.keyPending = now, 0, false
	} else {
		p.sinceKey++
		if !p.keyPending && p.sinceKey > keyGuardFrames && now.Sub(p.lastKey) > keyGuardAge {
			p.keyPending, wantKey = true, true
		}
	}
	p.mu.Unlock()
	if wantKey {
		go p.b.RequestKeyframe()
	}
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

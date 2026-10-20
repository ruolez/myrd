package session

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/inputproto"
	"github.com/ruolez/remotectl/agent/internal/keymap"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/platform/fake"
	"github.com/ruolez/remotectl/proto"
)

func TestPathFor(t *testing.T) {
	H, S, P, R := webrtc.ICECandidateTypeHost, webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypePrflx, webrtc.ICECandidateTypeRelay
	cases := []struct {
		l, r webrtc.ICECandidateType
		want string
	}{
		{H, H, proto.PathHost}, {H, S, proto.PathSrflx}, {P, H, proto.PathSrflx},
		{R, H, proto.PathRelay}, {S, R, proto.PathRelay}, {P, R, proto.PathRelay},
	}
	for _, c := range cases {
		if got := PathFor(c.l, c.r); got != c.want {
			t.Errorf("PathFor(%v,%v) = %s, want %s", c.l, c.r, got, c.want)
		}
	}
}

func TestCeilingKbps(t *testing.T) {
	cases := []struct {
		w, h  uint32
		relay bool
		want  int
	}{
		{1920, 1080, false, 8000},
		{1920, 1080, true, 6000},
		{3840, 2160, false, 16000}, // 4× pixels capped at 16 Mbps
		{1280, 720, false, 3555},
		{640, 480, false, 2000}, // floor of the ceiling
	}
	for _, c := range cases {
		if got := CeilingKbps(c.w, c.h, c.relay); got != c.want {
			t.Errorf("CeilingKbps(%d,%d,%v) = %d, want %d", c.w, c.h, c.relay, got, c.want)
		}
	}
}

func TestEncoderTarget(t *testing.T) {
	cases := []struct{ bwe, ceil, kbps int }{
		{5000, 8000, 5000},
		{12000, 8000, 8000}, // clamped to the ceiling
		{200, 8000, 1000},   // floor 1 Mbps
		{25000, 20000, 20000},
	}
	for _, c := range cases {
		if k := EncoderTarget(c.bwe, c.ceil); k != c.kbps {
			t.Errorf("EncoderTarget(%d, %d) = %d, want %d", c.bwe, c.ceil, k, c.kbps)
		}
	}
}

func TestScaledSize(t *testing.T) {
	cases := []struct {
		w, h       uint32
		maxW, maxH int
		ww, wh     uint32
	}{
		{1920, 1080, 0, 0, 1920, 1080},
		{3840, 2160, 1920, 0, 1920, 1080},
		{3840, 2160, 0, 720, 1280, 720},
		{2560, 1600, 1920, 1080, 1728, 1080},
		{1366, 769, 0, 0, 1366, 768}, // forced even
	}
	for _, c := range cases {
		if w, h := ScaledSize(c.w, c.h, c.maxW, c.maxH); w != c.ww || h != c.wh {
			t.Errorf("ScaledSize(%d,%d,%d,%d) = %dx%d, want %dx%d", c.w, c.h, c.maxW, c.maxH, w, h, c.ww, c.wh)
		}
	}
}

func TestDecideConsent(t *testing.T) {
	if decideConsent(true, true) != consentAuto || decideConsent(true, false) != consentLight ||
		decideConsent(false, true) != consentNewKey || decideConsent(false, false) != consentNewKey {
		t.Fatal("consent decision table wrong")
	}
}

type fakeMedia struct {
	monitors []uint32
	tier     int
	keyReqs  int
	clip     string
	selected int
}

func (f *fakeMedia) monitorID(i int) (uint32, bool) {
	if i < 0 || i >= len(f.monitors) {
		return 0, false
	}
	return f.monitors[i], true
}
func (f *fakeMedia) selectMonitor(i int) error {
	if _, ok := f.monitorID(i); !ok {
		return inputproto.ErrRange
	}
	f.selected = i
	return nil
}
func (f *fakeMedia) setTier(t int)               { f.tier = t }
func (f *fakeMedia) requestKeyframe()            { f.keyReqs++ }
func (f *fakeMedia) setRemoteClipboard(t string) { f.clip = t }

func TestInputRejectsCountTowardAbuse(t *testing.T) {
	b := fake.New()
	in := newInputState(b, keymap.X11)
	fm := &fakeMedia{monitors: []uint32{1}}
	bad := [][]byte{
		{0x7f}, // unknown type
		inputproto.Encode(inputproto.Event{Type: inputproto.TypeMouseMove, Monitor: 3}), // no such monitor
		inputproto.Encode(inputproto.Event{Type: inputproto.TypeKey, Usage: 0x00FF}),    // unmapped usage
		inputproto.Encode(inputproto.Event{Type: inputproto.TypeClipboardData, Xfer: 9, Data: []byte("x")}),
	}
	for _, m := range bad {
		if err := in.handle(m, fm); err == nil {
			t.Errorf("accepted %x", m)
		}
	}
	if in.rejects() != len(bad) || len(b.Injected()) != 0 {
		t.Fatalf("rejects=%d injected=%d", in.rejects(), len(b.Injected()))
	}
}

func TestInputControlOps(t *testing.T) {
	b := fake.New()
	in := newInputState(b, keymap.X11)
	fm := &fakeMedia{monitors: []uint32{1, 2}}
	for _, ev := range []inputproto.Event{
		{Type: inputproto.TypeControl, Op: inputproto.OpSelectMonitor, Arg: 1},
		{Type: inputproto.TypeControl, Op: inputproto.OpSetQuality, Arg: TierBackground},
		{Type: inputproto.TypeControl, Op: inputproto.OpRequestKeyframe},
	} {
		if err := in.handle(inputproto.Encode(ev), fm); err != nil {
			t.Fatal(err)
		}
	}
	if fm.selected != 1 || fm.tier != TierBackground || fm.keyReqs != 1 {
		t.Fatalf("media state %+v", fm)
	}
}

func TestReleaseAllReleasesHeldKeysAndButtons(t *testing.T) {
	b := fake.New()
	in := newInputState(b, keymap.X11)
	fm := &fakeMedia{monitors: []uint32{1}}
	_ = in.handle(inputproto.Encode(inputproto.Event{Type: inputproto.TypeKey, Usage: 0xE1, Down: true}), fm) // ShiftLeft
	_ = in.handle(inputproto.Encode(inputproto.Event{Type: inputproto.TypeMouseButton, Button: 0, Down: true}), fm)
	_ = in.handle(inputproto.Encode(inputproto.Event{Type: inputproto.TypeControl, Op: inputproto.OpReleaseAll}), fm)
	got := b.Injected()[2:]
	want := []platform.InputEvent{
		{Type: platform.EvKey, Keycode: 50, Down: false},
		{Type: platform.EvMouseButton, Button: 0, Down: false},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("released %+v", got)
	}
	in.releaseAll()
	if len(b.Injected()) != 4 {
		t.Fatal("second releaseAll should be a no-op")
	}
}

func TestClipboardFromBrowserReachesBackend(t *testing.T) {
	b := fake.New()
	in := newInputState(b, keymap.X11)
	fm := &fakeMedia{monitors: []uint32{1}}
	for _, f := range inputproto.ClipboardChunks(1, inputproto.MimeText, []byte("copied text")) {
		if err := in.handle(f, fm); err != nil {
			t.Fatal(err)
		}
	}
	if fm.clip != "copied text" {
		t.Fatalf("clip = %q", fm.clip)
	}
}

func TestFrameKeyframeGuard(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		frames   int
		want     int
	}{
		{"60 fps inside the GOP", 16 * time.Millisecond, 90, 0},
		{"idle frames once a second", time.Second, 10, 0},
		{"5 fps past 2 s", 200 * time.Millisecond, 12, 1},
		{"one request until the next keyframe", 200 * time.Millisecond, 40, 1},
	}
	for _, c := range cases {
		b := fake.New()
		track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "test")
		if err != nil {
			t.Fatal(err)
		}
		clock := t0
		p := &mediaPipe{b: b, track: track, now: func() time.Time { return clock }, applied: encoderState{fps: 5}}
		frame := func(key bool) {
			p.Frame([]byte{0, 0, 0, 1, 0x41}, clock.Sub(t0).Microseconds()+1, key)
		}
		frame(true)
		for i := 0; i < c.frames; i++ {
			clock = clock.Add(c.interval)
			frame(false)
		}
		time.Sleep(50 * time.Millisecond) // requests run on their own goroutine
		if got := b.KeyframeRequests(); got != c.want {
			t.Errorf("%s: %d keyframe requests, want %d", c.name, got, c.want)
		}
		if c.want == 0 {
			continue
		}
		frame(true) // the keyframe re-arms the guard
		for i := 0; i < c.frames; i++ {
			clock = clock.Add(c.interval)
			frame(false)
		}
		waitFor(t, "second guard request", func() bool { return b.KeyframeRequests() == c.want+1 })
	}
}

package session

import (
	"slices"
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
		fps   int
		relay bool
		want  int
	}{
		{1920, 1080, 30, false, 8000},
		{1920, 1080, 30, true, 6000},
		{1920, 1080, 60, false, 12125}, // × 2^0.6
		{1920, 1080, 15, false, 5278},  // × 0.5^0.6
		{3840, 2160, 30, false, 32000}, // 4× pixels
		{3840, 2160, 60, false, 40000}, // 48.5 Mbps capped at 40
		{1280, 720, 30, false, 3555},
		{640, 480, 30, false, 2000}, // floor of the ceiling
	}
	for _, c := range cases {
		if got := CeilingKbps(c.w, c.h, c.fps, c.relay); got != c.want {
			t.Errorf("CeilingKbps(%d,%d,%d,%v) = %d, want %d", c.w, c.h, c.fps, c.relay, got, c.want)
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

func TestMoveChannelDropsMovesOlderThanTheNewest(t *testing.T) {
	b := fake.New()
	in := newInputState(b, keymap.X11)
	fm := &fakeMedia{monitors: []uint32{1}}
	// Sequence numbers wrap at 65535; 65535 arriving after 1 is late, not new.
	for _, seq := range []uint16{65534, 65535, 0, 65535, 1, 1, 0, 5} {
		ev := inputproto.Event{Type: inputproto.TypeMouseMoveSeq, Seq: seq, X: seq, Y: 7}
		if err := in.handleMove(inputproto.Encode(ev), fm); err != nil {
			t.Fatal(err)
		}
	}
	var xs []uint16
	for _, ev := range b.Injected() {
		xs = append(xs, ev.X)
	}
	if want := []uint16{65534, 65535, 0, 1, 5}; !slices.Equal(xs, want) {
		t.Fatalf("injected x = %v, want %v", xs, want)
	}
	if in.rejects() != 0 {
		t.Fatalf("late moves counted as abuse: %d", in.rejects())
	}
}

func TestSequencedMovesBelongOnlyToTheMoveChannel(t *testing.T) {
	b := fake.New()
	in := newInputState(b, keymap.X11)
	fm := &fakeMedia{monitors: []uint32{1}}
	seq := inputproto.Encode(inputproto.Event{Type: inputproto.TypeMouseMoveSeq, Seq: 1})
	plain := inputproto.Encode(inputproto.Event{Type: inputproto.TypeMouseMove})
	if in.handle(seq, fm) == nil || in.handleMove(plain, fm) == nil {
		t.Fatal("a move type was accepted on the wrong channel")
	}
	if in.rejects() != 2 || len(b.Injected()) != 0 {
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

func TestCtrlAltDeleteBecomesASecureAttentionRequest(t *testing.T) {
	type key struct {
		usage uint16
		down  bool
	}
	press := func(us ...uint16) []key {
		var ks []key
		for _, u := range us {
			ks = append(ks, key{u, true})
		}
		for i := len(us) - 1; i >= 0; i-- {
			ks = append(ks, key{us[i], false})
		}
		return ks
	}
	cases := []struct {
		name     string
		hook     bool
		keys     []key
		wantSAS  int
		injected []uint16 // usages injected, in order (downs and ups)
	}{
		{"left Ctrl+Alt+Del", true, press(0xE0, 0xE2, 0x4C), 1, []uint16{0xE0, 0xE2, 0xE2, 0xE0}},
		{"right Ctrl+Alt with keypad Del", true, press(0xE4, 0xE6, 0x63), 1, []uint16{0xE4, 0xE6, 0xE6, 0xE4}},
		{"Ctrl+Del without Alt stays keys", true, press(0xE0, 0x4C), 0, []uint16{0xE0, 0x4C, 0x4C, 0xE0}},
		{"no hook: injected like any other keys", false, press(0xE0, 0xE2, 0x4C), 0,
			[]uint16{0xE0, 0xE2, 0x4C, 0x4C, 0xE2, 0xE0}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := fake.New()
			in := newInputState(b, keymap.X11)
			sas := 0
			if c.hook {
				in.sas = func() { sas++ }
			}
			fm := &fakeMedia{monitors: []uint32{1}}
			for _, k := range c.keys {
				if err := in.handle(inputproto.Encode(inputproto.Event{Type: inputproto.TypeKey, Usage: k.usage, Down: k.down}), fm); err != nil {
					t.Fatal(err)
				}
			}
			var want []platform.InputEvent
			down := map[uint16]bool{}
			for _, u := range c.injected {
				down[u] = !down[u]
				want = append(want, platform.InputEvent{Type: platform.EvKey, Keycode: keymap.X11[u].Code, Flags: keymap.X11[u].Flags, Down: down[u]})
			}
			if got := b.Injected(); sas != c.wantSAS || !slices.Equal(got, want) {
				t.Fatalf("secure attention %d times (want %d), injected %+v, want %+v", sas, c.wantSAS, got, want)
			}
		})
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

func TestFrameLeavesKeyframesToTheBrowser(t *testing.T) {
	// 5 fps for 12 s: the old 2 s guard asked for five keyframes here.
	b := fake.New()
	track, err := newVideoTrack()
	if err != nil {
		t.Fatal(err)
	}
	p := &mediaPipe{b: b, track: track, now: time.Now, applied: encoderState{fps: 5}}
	for i := 0; i < 60; i++ {
		var fl platform.FrameFlags
		if i == 0 {
			fl = platform.FrameKey
		}
		p.Frame([]byte{0, 0, 0, 1, 0x41}, int64(i)*200_000, fl)
	}
	time.Sleep(50 * time.Millisecond)
	if got := b.KeyframeRequests(); got != 0 {
		t.Fatalf("%d keyframe requests, want 0 (keyframes come from PLI)", got)
	}
}

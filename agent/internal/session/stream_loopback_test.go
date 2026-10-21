package session

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/inputproto"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

// waitStreamReply returns the `stream` state answering request id.
func (h *harness) waitStreamReply(t *testing.T, id int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := h.waitControl(t, "stream"); m["id"] == float64(id) {
			return m
		}
	}
	t.Fatalf("no stream reply for id %d", id)
	return nil
}

func (h *harness) sendStream(t *testing.T, id int, preset, fps, res, maxKbps string) {
	t.Helper()
	msg := fmt.Sprintf(`{"t":"stream","id":%d,"preset":"%s","fps":%s,"res":%s,"max_kbps":%s,"background":false}`,
		id, preset, fps, res, maxKbps)
	if err := h.control.SendText(msg); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) sendWallpaper(t *testing.T, id int, show bool) {
	t.Helper()
	msg := fmt.Sprintf(`{"t":"stream","id":%d,"preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false,"wallpaper":%v}`,
		id, show)
	if err := h.control.SendText(msg); err != nil {
		t.Fatal(err)
	}
}

func expectFields(t *testing.T, what string, m map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if fmt.Sprint(m[k]) != fmt.Sprint(v) {
			t.Errorf("%s: %s = %v, want %v", what, k, m[k], v)
		}
	}
}

func TestLoopbackStreamSettings(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}

	hello := h.waitControl(t, "hello")
	if fmt.Sprint(hello["features"]) != "[stream move]" || hello["max_fps"] != float64(60) {
		t.Fatalf("hello features=%v max_fps=%v", hello["features"], hello["max_fps"])
	}
	if mon := hello["monitors"].([]any)[0].(map[string]any); mon["width"] != float64(1920) || mon["height"] != float64(1080) {
		t.Fatalf("hello monitor = %v", mon)
	}
	st := h.waitControl(t, "stream")
	expectFields(t, "initial state", st, map[string]any{"ok": true, "preset": "auto", "fps": nil, "width": 1920, "height": 1080,
		"native_width": 1920, "native_height": 1080, "max_fps": 60, "background": false})
	if auto, ok := st["auto"].(map[string]any); !ok || auto["rungs"] != float64(6) {
		t.Fatalf("auto = %v", st["auto"])
	}
	waitFor(t, "capture", func() bool { return len(h.backend.Starts()) == 1 })
	if calls := h.backend.WallpaperCalls(); !reflect.DeepEqual(calls, []bool{false}) {
		t.Fatalf("wallpaper calls = %v, want only the support probe on a backend that cannot hide it", calls)
	}

	h.sendStream(t, 3, "custom", "15", `"720p"`, "5000")
	st = h.waitStreamReply(t, 3)
	expectFields(t, "custom 720p", st, map[string]any{"ok": true, "preset": "custom", "fps": 15, "res": "720p",
		"width": 1280, "height": 720, "target_fps": 15, "ceil_kbps": 5000, "auto": nil})
	starts := h.backend.Starts()
	if len(starts) != 2 || starts[1].MaxHeight != 720 || starts[1].FPS != 15 || starts[1].BitrateKbps > 5000 {
		t.Fatalf("starts = %+v", starts)
	}

	h.sendStream(t, 4, "custom", "10", `"720p"`, "5000")
	st = h.waitStreamReply(t, 4)
	expectFields(t, "fps 10", st, map[string]any{"ok": true, "fps": 10, "target_fps": 10, "width": 1280})
	if _, fps := h.backend.Configured(); fps != 10 || len(h.backend.Starts()) != 2 {
		t.Fatalf("configured fps %d, %d starts: want a reconfigure only", fps, len(h.backend.Starts()))
	}

	waitFor(t, "input channel open", func() bool { return h.input.ReadyState() == webrtc.DataChannelStateOpen })
	_ = h.input.Send(inputproto.Encode(inputproto.Event{Type: inputproto.TypeControl, Op: inputproto.OpSetQuality, Arg: TierBackground}))
	waitFor(t, "legacy background tier", func() bool { k, f := h.backend.Configured(); return k == 1000 && f == 5 })
	if n := len(h.backend.Starts()); n != 2 {
		t.Fatalf("background restarted capture: %d starts", n)
	}

	h.sendStream(t, 9, "custom", "0", "null", "null")
	st = h.waitStreamReply(t, 9)
	expectFields(t, "invalid", st, map[string]any{"ok": false, "preset": "custom", "fps": 10, "res": "720p",
		"background": true, "target_fps": 5})
	h.mgr.mu.Lock()
	cur := h.mgr.cur
	h.mgr.mu.Unlock()
	if n := cur.in.rejects(); n != 0 {
		t.Fatalf("invalid stream request counted as input abuse: %d", n)
	}
}

func TestLoopbackWallpaperHiddenWhileSessionRuns(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.backend.SetCanHideWallpaper(true)
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	if hello := h.waitControl(t, "hello"); fmt.Sprint(hello["features"]) != "[stream move wallpaper]" {
		t.Fatalf("hello features = %v", hello["features"])
	}
	waitFor(t, "capture", func() bool { return len(h.backend.Starts()) == 1 })
	if !h.backend.WallpaperHidden() {
		t.Fatal("wallpaper visible although the browser never asked for it")
	}

	h.sendWallpaper(t, 5, true)
	expectFields(t, "show", h.waitStreamReply(t, 5), map[string]any{"ok": true, "wallpaper": true})
	waitFor(t, "wallpaper shown", func() bool { return !h.backend.WallpaperHidden() })

	h.sendWallpaper(t, 6, false)
	expectFields(t, "hide", h.waitStreamReply(t, 6), map[string]any{"ok": true, "wallpaper": false})
	waitFor(t, "wallpaper hidden", h.backend.WallpaperHidden)
	if n := len(h.backend.Starts()); n != 1 {
		t.Fatalf("toggling the wallpaper restarted capture: %d starts", n)
	}

	end, _ := proto.Encode(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sessionID, Reason: proto.EndUserHangup})
	h.mgr.HandleSignal(proto.TypeSessionEnd, end)
	waitFor(t, "teardown", func() bool { return !h.backend.Capturing() })
	if calls := h.backend.WallpaperCalls(); !reflect.DeepEqual(calls, []bool{false, true, false, true, false}) {
		t.Fatalf("wallpaper calls = %v, want probe, hide, show, hide, restore at the end", calls)
	}
}

func TestLoopbackMoveChannelAndNegotiatedProfile(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	h.waitControl(t, "hello")
	select {
	case <-h.rtpPackets:
	case <-time.After(10 * time.Second):
		t.Fatal("no video")
	}
	h.mu.Lock()
	fmtp := h.videoFmtp
	h.mu.Unlock()
	if got, want := h.backend.Starts()[0].ProfileIDC, ProfileIDC(fmtp); got != want {
		t.Errorf("encoder profile %d, want %d for the negotiated %q", got, want, fmtp)
	}

	waitFor(t, "move channel", func() bool { return h.move.ReadyState() == webrtc.DataChannelStateOpen })
	for _, seq := range []uint16{1, 2} {
		if err := h.move.Send(inputproto.Encode(inputproto.Event{Type: inputproto.TypeMouseMoveSeq, Seq: seq,
			X: 1000 * seq, Y: 500})); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "moves injected", func() bool {
		ev := h.backend.Injected()
		return len(ev) == 2 && ev[1].Type == platform.EvMouseMove && ev[1].X == 2000 && ev[1].MonitorID == 1
	})
}

func TestLoopbackStaticScreenIsRefinedOnce(t *testing.T) {
	for _, supported := range []bool{true, false} {
		h := newHarness(t, platform.ConsentOnce)
		h.backend.SetStatic(true)
		h.backend.SetRefineUnsupported(!supported)
		h.offer(false)
		if err := <-h.answerOK; err != nil {
			t.Fatal(err)
		}
		h.waitControl(t, "hello")
		want := refineQPs
		if !supported {
			want = refineQPs[:1] // asked once, then never again
		}
		waitFor(t, "refinement passes", func() bool { return len(h.backend.Refines()) >= len(want) })
		time.Sleep(500 * time.Millisecond) // nothing more once the finest pass is out
		if got := h.backend.Refines(); !slices.Equal(got, want) {
			t.Fatalf("supported=%v: passes = %v, want %v", supported, got, want)
		}
		h.mgr.Close()
	}
}

func TestLoopbackStreamFollowsTheViewersWindow(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	h.waitControl(t, "hello")
	waitFor(t, "capture", func() bool { return len(h.backend.Starts()) == 1 })
	send := func(id int, view string) map[string]any {
		msg := fmt.Sprintf(`{"t":"stream","id":%d,"preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false%s}`, id, view)
		if err := h.control.SendText(msg); err != nil {
			t.Fatal(err)
		}
		return h.waitStreamReply(t, id)
	}

	st := send(2, `,"view":{"w":1280,"h":800}`)
	expectFields(t, "window", st, map[string]any{"ok": true, "res": nil, "width": 1280, "height": 720})
	if r := st["resolved"].(map[string]any); r["res"] != proto.ResWindow {
		t.Fatalf("resolved = %v, want the window size", r)
	}
	if opt := st["options"].(map[string]any)["res"].([]any)[0]; fmt.Sprint(opt) != "map[height:720 id:window width:1280]" {
		t.Fatalf("first resolution option = %v", opt)
	}
	if s := h.backend.Starts(); len(s) != 2 || s[1].MaxWidth != 1280 || s[1].MaxHeight != 800 {
		t.Fatalf("starts = %+v, want a restart bounded by the view", s)
	}

	st = send(3, `,"view":{"w":961,"h":600}`) // odd widths round down to even
	expectFields(t, "smaller window", st, map[string]any{"width": 960, "height": 540})
	if auto := st["auto"].(map[string]any); auto["reason"] != AutoWarmup {
		t.Fatalf("auto = %v: a resize keeps Auto's state (still warming up)", auto)
	}

	st = send(4, ``) // 1:1 view: no view reported
	expectFields(t, "native", st, map[string]any{"width": 1920, "height": 1080})
	if r := st["resolved"].(map[string]any); r["res"] != proto.ResNative {
		t.Fatalf("resolved = %v, want native without a view", r)
	}
}

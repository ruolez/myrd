package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/inputproto"
	"github.com/ruolez/remotectl/agent/internal/platform"
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
	if fmt.Sprint(hello["features"]) != "[stream]" || hello["max_fps"] != float64(60) {
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

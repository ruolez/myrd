package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

var testModes = []platform.DisplayMode{{Width: 2560, Height: 1440, Scale: 1}, {Width: 1920, Height: 1080, Scale: 1},
	{Width: 1280, Height: 720, Scale: 1}}

func (h *harness) sendDisplay(t *testing.T, id int, body map[string]any) map[string]any {
	t.Helper()
	msg := map[string]any{"t": "display", "id": id}
	for k, v := range body {
		msg[k] = v
	}
	b, _ := json.Marshal(msg)
	if err := h.control.SendText(string(b)); err != nil {
		t.Fatal(err)
	}
	for {
		if m := h.waitControl(t, "display"); m["id"] == float64(id) {
			return m
		}
	}
}

// waitDisplayState returns the first stream state whose display reports cur.
func (h *harness) waitDisplayState(t *testing.T, cur string) map[string]any {
	t.Helper()
	for {
		st := h.waitControl(t, "stream")
		if d, ok := st["display"].(map[string]any); ok && modeString(d["current"]) == cur {
			return st
		}
	}
}

func modeString(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return "none"
	}
	b, _ := json.Marshal([]any{m["width"], m["height"]})
	return string(b)
}

func (h *harness) connect(t *testing.T) map[string]any {
	t.Helper()
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	return h.waitControl(t, "hello")
}

func TestLoopbackDisplayModeSwitchAndRestore(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.backend.SetDisplayModes(testModes)
	hello := h.connect(t)
	if !slices.Contains(hello["features"].([]any), any(proto.FeatureDisplay)) {
		t.Fatalf("hello features %v lack %q", hello["features"], proto.FeatureDisplay)
	}
	st := h.waitDisplayState(t, "[1920,1080]")
	d := st["display"].(map[string]any)
	if len(d["modes"].([]any)) != 3 || d["original"] != nil {
		t.Fatalf("initial display = %v, want 3 modes and no original", d)
	}

	if m := h.sendDisplay(t, 1, map[string]any{"width": 1280, "height": 720}); m["ok"] != true {
		t.Fatalf("switch = %v", m)
	}
	st = h.waitDisplayState(t, "[1280,720]")
	d = st["display"].(map[string]any)
	got := []any{st["native_width"], st["native_height"], modeString(d["original"])}
	if want := []any{float64(1280), float64(720), "[1920,1080]"}; !slices.Equal(got, want) {
		t.Fatalf("after switch native, original = %v, want %v", got, want)
	}
	if !h.backend.Capturing() || len(h.backend.Starts()) != 2 {
		t.Fatalf("capture after the switch: capturing %v, %d starts", h.backend.Capturing(), len(h.backend.Starts()))
	}

	if m := h.sendDisplay(t, 2, map[string]any{"restore": true}); m["ok"] != true {
		t.Fatalf("restore = %v", m)
	}
	st = h.waitDisplayState(t, "[1920,1080]")
	if d := st["display"].(map[string]any); d["original"] != nil {
		t.Fatalf("after restore original = %v, want none", d["original"])
	}
	if calls := h.backend.DisplayCalls(); !slices.Equal(calls, []string{"set 1 1280x720", "restore"}) {
		t.Fatalf("display calls %v", calls)
	}
}

func TestLoopbackDisplayRefusals(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.backend.SetDisplayModes(testModes)
	h.connect(t)
	got := []any{h.sendDisplay(t, 1, map[string]any{"width": 1366, "height": 768})["code"],
		h.sendDisplay(t, 2, map[string]any{"width": 1920, "height": 1080})["ok"],
		h.sendDisplay(t, 3, map[string]any{"restore": true})["ok"]}
	if want := []any{displayUnknownMode, true, true}; !slices.Equal(got, want) {
		t.Fatalf("unknown mode, current mode, restore unchanged = %v, want %v", got, want)
	}
	if calls := h.backend.DisplayCalls(); len(calls) != 0 {
		t.Fatalf("no-op requests reached the backend: %v", calls)
	}

	plain := newHarness(t, platform.ConsentOnce)
	hello := plain.connect(t)
	code := plain.sendDisplay(t, 1, map[string]any{"width": 1280, "height": 720})["code"]
	if slices.Contains(hello["features"].([]any), any(proto.FeatureDisplay)) || code != displayUnsupported {
		t.Fatalf("backend without modes: features %v, code %v", hello["features"], code)
	}
}

func TestLoopbackDisplayRestoredAfterTheSessionEnds(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.mgr.displayDelay = 50 * time.Millisecond
	h.backend.SetDisplayModes(testModes)
	h.connect(t)
	h.sendDisplay(t, 1, map[string]any{"width": 2560, "height": 1440})
	end, _ := proto.Encode(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sessionID, Reason: proto.EndUserHangup})
	h.mgr.HandleSignal(proto.TypeSessionEnd, end)
	waitFor(t, "restore", func() bool { return slices.Contains(h.backend.DisplayCalls(), "restore") })
	mons, _ := h.backend.Monitors()
	if mons[0].Width != 1920 {
		t.Fatalf("monitor width %d after restore, want 1920", mons[0].Width)
	}
}

// A session that starts within the grace period takes over the changed mode;
// it is restored when that session ends.
func TestDisplayHandOverToTheNextSession(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.mgr.displayDelay = 30 * time.Millisecond
	h.backend.SetDisplayModes(testModes)
	if err := h.mgr.setDisplayMode(1, 1280, 720, testModes[1]); err != nil {
		t.Fatal(err)
	}
	h.mgr.displayRelease()
	h.mgr.displayAcquire()
	time.Sleep(4 * h.mgr.displayDelay)
	if slices.Contains(h.backend.DisplayCalls(), "restore") {
		t.Fatal("restored while the next session holds the mode")
	}
	h.mgr.displayRelease()
	waitFor(t, "restore", func() bool { return slices.Contains(h.backend.DisplayCalls(), "restore") })
}

func TestDisplayMarkerRestoresAfterACrash(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "display-changed")
	h := newHarness(t, platform.ConsentOnce, func(o *Options) { o.DisplayMarker = marker })
	h.backend.SetDisplayModes(testModes)
	if err := h.mgr.setDisplayMode(1, 1280, 720, testModes[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no marker while changed: %v", err)
	}

	// A new agent process finds the marker: the last one died before restoring.
	o := h.mgr.o
	m2, err := NewManager(o)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	_, statErr := os.Stat(marker)
	if calls := h.backend.DisplayCalls(); !slices.Equal(calls, []string{"set 1 1280x720", "restore"}) || !os.IsNotExist(statErr) {
		t.Fatalf("calls %v, marker %v: want a restore and the marker gone", calls, statErr)
	}
}

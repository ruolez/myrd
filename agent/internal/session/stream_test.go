package session

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

func TestEncoderMaxFPS(t *testing.T) {
	for enc, want := range map[string]int{
		"x264": 30, "openh264": 30, "mf-sw": 30, "videotoolbox-sw": 30, "libvpx-vp9": 30, "none": 30,
		"videotoolbox": 60, "mf-hw": 60, "fake": 60, "": 60,
	} {
		if got := EncoderMaxFPS(enc); got != want {
			t.Errorf("EncoderMaxFPS(%q) = %d, want %d", enc, got, want)
		}
	}
}

func TestResolveSettings(t *testing.T) {
	cases := []struct {
		name   string
		s      StreamSettings
		maxFPS int
		want   Resolved
	}{
		{"auto", StreamSettings{Preset: proto.PresetAuto}, 60, Resolved{30, proto.ResNative, 0}},
		{"auto 60 fps when chosen", StreamSettings{Preset: proto.PresetAuto, FPS: 60}, 60, Resolved{60, proto.ResNative, 0}},
		{"auto software", StreamSettings{Preset: proto.PresetAuto}, 30, Resolved{30, proto.ResNative, 0}},
		{"auto ceilings", StreamSettings{Preset: proto.PresetAuto, FPS: 30, Res: proto.Res1080p, MaxKbps: 5000}, 60,
			Resolved{30, proto.Res1080p, 5000}},
		{"speed", StreamSettings{Preset: proto.PresetSpeed}, 60, Resolved{60, proto.Res720p, 4000}},
		{"speed software", StreamSettings{Preset: proto.PresetSpeed}, 30, Resolved{30, proto.Res720p, 4000}},
		{"balanced", StreamSettings{Preset: proto.PresetBalanced}, 60, Resolved{30, proto.Res1080p, 8000}},
		{"balanced low cap", StreamSettings{Preset: proto.PresetBalanced}, 24, Resolved{24, proto.Res1080p, 8000}},
		{"quality", StreamSettings{Preset: proto.PresetQuality}, 60, Resolved{30, proto.ResNative, 50000}},
		{"quality fps override", StreamSettings{Preset: proto.PresetQuality, FPS: 60}, 60, Resolved{60, proto.ResNative, 50000}},
		{"auto with a view fits the window", StreamSettings{Preset: proto.PresetAuto, View: View{1600, 900}}, 60,
			Resolved{30, proto.ResWindow, 0}},
		{"quality with a view fits the window", StreamSettings{Preset: proto.PresetQuality, View: View{1600, 900}}, 60,
			Resolved{30, proto.ResWindow, 50000}},
		{"speed keeps its size with a view", StreamSettings{Preset: proto.PresetSpeed, View: View{1600, 900}}, 60,
			Resolved{60, proto.Res720p, 4000}},
		{"explicit native beats the view", StreamSettings{Preset: proto.PresetCustom, Res: proto.ResNative, View: View{1600, 900}}, 60,
			Resolved{60, proto.ResNative, 0}},
		{"custom defaults", StreamSettings{Preset: proto.PresetCustom}, 60, Resolved{60, proto.ResNative, 0}},
		{"custom all", StreamSettings{Preset: proto.PresetCustom, FPS: 15, Res: proto.Res720p, MaxKbps: 5000}, 60,
			Resolved{15, proto.Res720p, 5000}},
		{"custom fps above encoder cap", StreamSettings{Preset: proto.PresetCustom, FPS: 60}, 30, Resolved{30, proto.ResNative, 0}},
		{"unknown preset behaves as auto", StreamSettings{Preset: "turbo"}, 60, Resolved{30, proto.ResNative, 0}},
	}
	for _, c := range cases {
		if got := ResolveSettings(c.s, c.maxFPS); got != c.want {
			t.Errorf("%s: ResolveSettings = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestOutputSize(t *testing.T) {
	cases := []struct {
		name       string
		nw, nh     uint32
		res        string
		maxW, maxH int
		view       View
		w, h       uint32
	}{
		{"native", 1920, 1080, proto.ResNative, 0, 0, View{}, 1920, 1080},
		{"1080p on 1080p", 1920, 1080, proto.Res1080p, 0, 0, View{}, 1920, 1080},
		{"1440p never upscales", 1920, 1080, proto.Res1440p, 0, 0, View{}, 1920, 1080},
		{"720p", 1920, 1080, proto.Res720p, 0, 0, View{}, 1280, 720},
		{"half", 1920, 1080, proto.ResHalf, 0, 0, View{}, 960, 540},
		{"ultrawide caps the short edge", 3440, 1440, proto.Res1080p, 0, 0, View{}, 2580, 1080},
		{"portrait caps the short edge", 1080, 1920, proto.Res720p, 0, 0, View{}, 720, 1280},
		{"5K 1440p", 5120, 2880, proto.Res1440p, 0, 0, View{}, 2560, 1440},
		{"5K half", 5120, 2880, proto.ResHalf, 0, 0, View{}, 2560, 1440},
		{"16:10 1080p", 2560, 1600, proto.Res1080p, 0, 0, View{}, 1728, 1080},
		{"odd laptop 720p even dims", 1366, 768, proto.Res720p, 0, 0, View{}, 1280, 720},
		{"odd half even dims", 1366, 769, proto.ResHalf, 0, 0, View{}, 682, 384},
		{"options cap applies to native", 3840, 2160, proto.ResNative, 1920, 0, View{}, 1920, 1080},
		{"options cap and res cap", 3840, 2160, proto.Res720p, 1920, 0, View{}, 1280, 720},
		{"empty res is native", 2560, 1440, "", 0, 0, View{}, 2560, 1440},
		{"window fits the view", 2560, 1440, proto.ResWindow, 0, 0, View{1600, 1000}, 1600, 900},
		{"window keeps the binding edge exact", 2560, 1440, proto.ResWindow, 0, 0, View{1366, 800}, 1366, 768},
		{"window by height", 2560, 1440, proto.ResWindow, 0, 0, View{2000, 768}, 1364, 768},
		{"window never upscales", 1920, 1080, proto.ResWindow, 0, 0, View{2880, 1600}, 1920, 1080},
		{"window without a view is native", 2560, 1440, proto.ResWindow, 0, 0, View{}, 2560, 1440},
		{"view ignored for named sizes", 2560, 1440, proto.Res720p, 0, 0, View{800, 600}, 1280, 720},
		{"options cap bounds the window", 3840, 2160, proto.ResWindow, 1920, 0, View{2880, 1600}, 1920, 1080},
	}
	for _, c := range cases {
		if w, h := OutputSize(c.nw, c.nh, c.res, c.maxW, c.maxH, c.view); w != c.w || h != c.h {
			t.Errorf("%s: OutputSize(%dx%d, %s) = %dx%d, want %dx%d", c.name, c.nw, c.nh, c.res, w, h, c.w, c.h)
		}
	}
}

func TestCaptureBoxReproducesOutputSize(t *testing.T) {
	// The native layers fit the native size into the box with ScaledSize's
	// arithmetic; the box must land exactly on the size the agent reports.
	sizes := [][2]uint32{{1920, 1080}, {3440, 1440}, {1080, 1920}, {5120, 2880}, {1366, 768}, {2560, 1600}, {1280, 1024}}
	views := []View{{}, {1600, 900}, {1366, 768}, {2880, 1600}, {1023, 777}}
	for _, sz := range sizes {
		for _, res := range []string{proto.ResNative, proto.ResWindow, proto.Res1440p, proto.Res1080p, proto.Res720p, proto.ResHalf} {
			for _, v := range views {
				bw, bh := CaptureBox(sz[0], sz[1], res, 0, 0, v)
				gw, gh := ScaledSize(sz[0], sz[1], bw, bh)
				if w, h := OutputSize(sz[0], sz[1], res, 0, 0, v); gw != w || gh != h {
					t.Errorf("%v %s %v: box %dx%d gives %dx%d, OutputSize %dx%d", sz, res, v, bw, bh, gw, gh, w, h)
				}
			}
		}
	}
}

// The window size fills one edge of the view exactly and fits the other, so
// the browser draws it without scaling.
func TestWindowSizeFillsTheView(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 2000; i++ {
		nw, nh := uint32(640+r.IntN(7000)), uint32(480+r.IntN(4000))
		v := View{(2 + r.IntN(int(nw))) &^ 1, (2 + r.IntN(int(nh))) &^ 1}
		v.W, v.H = max(v.W, 2), max(v.H, 2)
		w, h := OutputSize(nw, nh, proto.ResWindow, 0, 0, v)
		if int(w) > v.W || int(h) > v.H {
			t.Fatalf("%dx%d in view %v: %dx%d overflows", nw, nh, v, w, h)
		}
		if int(w) != v.W && int(h) != v.H {
			t.Fatalf("%dx%d in view %v: %dx%d fills neither edge", nw, nh, v, w, h)
		}
	}
}

func TestResolutionOptions(t *testing.T) {
	got := ResolutionOptions(5120, 2880, 0, 0, View{})
	want := []ResOption{
		{proto.ResNative, 5120, 2880}, {proto.Res1440p, 2560, 1440}, {proto.Res1080p, 1920, 1080},
		{proto.Res720p, 1280, 720}, {proto.ResHalf, 2560, 1440},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolutionOptions = %+v, want %+v", got, want)
	}
	got = ResolutionOptions(5120, 2880, 0, 0, View{2880, 1600})
	if want = append([]ResOption{{proto.ResWindow, 2844, 1600}}, want...); !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolutionOptions with a view = %+v, want %+v", got, want)
	}
}

func TestFPSOptions(t *testing.T) {
	if got := FPSOptions(60); !reflect.DeepEqual(got, []int{5, 10, 15, 24, 30, 60}) {
		t.Errorf("FPSOptions(60) = %v", got)
	}
	if got := FPSOptions(30); !reflect.DeepEqual(got, []int{5, 10, 15, 24, 30}) {
		t.Errorf("FPSOptions(30) = %v", got)
	}
}

func TestLegacyTierSettings(t *testing.T) {
	cur := StreamSettings{Preset: proto.PresetQuality, FPS: 24}
	cases := []struct {
		tier int
		want StreamSettings
		bg   bool
	}{
		{TierBackground, cur, true},
		{TierBalanced, StreamSettings{Preset: proto.PresetCustom, FPS: 30, Res: proto.ResNative}, false},
		{TierHigh, StreamSettings{Preset: proto.PresetAuto}, false},
	}
	for _, c := range cases {
		if got, bg := LegacyTierSettings(c.tier, cur); got != c.want || bg != c.bg {
			t.Errorf("LegacyTierSettings(%d) = %+v,%v, want %+v,%v", c.tier, got, bg, c.want, c.bg)
		}
	}
}

func TestSettingsFromRequestVectors(t *testing.T) {
	var v struct {
		StreamRequest []struct {
			Name   string `json:"name"`
			JSON   string `json:"json"`
			Hidden bool   `json:"hidden"`
			View   View   `json:"view"`
			Prefs  struct {
				Preset    string  `json:"preset"`
				FPS       *int    `json:"fps"`
				Res       *string `json:"res"`
				MaxKbps   *int    `json:"max_kbps"`
				Wallpaper bool    `json:"wallpaper"`
				Color     string  `json:"color"`
			} `json:"prefs"`
		} `json:"stream_request"`
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "test-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil || len(v.StreamRequest) == 0 {
		t.Fatalf("vectors: %v", err)
	}
	for _, c := range v.StreamRequest {
		r, err := proto.DecodeStreamRequest([]byte(c.JSON))
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		want := StreamSettings{Preset: c.Prefs.Preset, View: c.View, Color: c.Prefs.Color}
		if c.Prefs.FPS != nil {
			want.FPS = *c.Prefs.FPS
		}
		if c.Prefs.Res != nil {
			want.Res = *c.Prefs.Res
		}
		if c.Prefs.MaxKbps != nil {
			want.MaxKbps = *c.Prefs.MaxKbps
		}
		if got := SettingsFromRequest(r); got != want || r.Background != c.Hidden || r.Wallpaper != c.Prefs.Wallpaper {
			t.Errorf("%s: settings %+v (bg %v, wallpaper %v), want %+v (bg %v, wallpaper %v)",
				c.Name, got, r.Background, r.Wallpaper, want, c.Hidden, c.Prefs.Wallpaper)
		}
	}
}

func TestPlanStream(t *testing.T) {
	top := &Rung{FPS: 30, Res: proto.ResNative, W: 1920, H: 1080}
	low := &Rung{FPS: 15, Res: proto.Res720p, W: 1280, H: 720}
	base := StreamInput{Settings: StreamSettings{Preset: proto.PresetAuto}, MaxFPS: 60, NativeW: 1920, NativeH: 1080, BweKbps: 5000, Rung: top}
	with := func(f func(*StreamInput)) StreamInput { in := base; f(&in); return in }
	cases := []struct {
		name string
		in   StreamInput
		want StreamPlan
	}{
		// 12125 = the 60 fps ceiling: Auto spends it on 30 fps frames.
		{"auto top rung", base,
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 5000, 12125, 1920, 1080, 0, 0, 0}},
		{"auto with 30 fps chosen keeps the 30 fps ceiling", with(func(in *StreamInput) { in.Settings.FPS = 30 }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 5000, 8000, 1920, 1080, 0, 0, 0}},
		{"auto on a 30 fps encoder", with(func(in *StreamInput) { in.MaxFPS = 30 }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 5000, 8000, 1920, 1080, 0, 0, 0}},
		{"auto 60 fps when chosen", with(func(in *StreamInput) {
			in.Settings.FPS, in.Rung = 60, &Rung{FPS: 60, Res: proto.ResNative, W: 1920, H: 1080}
		}), StreamPlan{Resolved{60, proto.ResNative, 0}, 60, 5000, 12125, 1920, 1080, 0, 0, 0}},
		{"auto lower rung restarts smaller", with(func(in *StreamInput) { in.Rung = low }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 15, 2345, 2345, 1280, 720, 0, 720, 0}},
		{"auto without a rung uses the ceilings", with(func(in *StreamInput) { in.Rung = nil }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 5000, 12125, 1920, 1080, 0, 0, 0}},
		{"auto sized to the window", with(func(in *StreamInput) {
			in.Settings.View, in.NativeW, in.NativeH = View{1600, 1000}, 2560, 1440
			in.Rung = &Rung{FPS: 30, Res: proto.ResWindow, W: 1600, H: 900}
		}), StreamPlan{Resolved{30, proto.ResWindow, 0}, 30, 5000, 8420, 1600, 900, 1600, 1000, 0}},
		{"auto relay ceiling", with(func(in *StreamInput) { in.Relay, in.BweKbps = true, 12000 }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 6000, 6000, 1920, 1080, 0, 0, 0}},
		{"estimate below the floor", with(func(in *StreamInput) { in.BweKbps = 200 }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 1000, 12125, 1920, 1080, 0, 0, 0}},
		{"quality manual cap", with(func(in *StreamInput) { in.Settings, in.BweKbps = StreamSettings{Preset: proto.PresetQuality}, 25000 }),
			StreamPlan{Resolved{30, proto.ResNative, 50000}, 30, 25000, 50000, 1920, 1080, 0, 0, 0}},
		{"manual cap clamped on relay", with(func(in *StreamInput) {
			in.Settings, in.Relay, in.BweKbps = StreamSettings{Preset: proto.PresetQuality}, true, 25000
		}), StreamPlan{Resolved{30, proto.ResNative, 50000}, 30, 6000, 6000, 1920, 1080, 0, 0, 0}},
		{"speed on a software encoder", with(func(in *StreamInput) { in.Settings, in.MaxFPS = StreamSettings{Preset: proto.PresetSpeed}, 30 }),
			StreamPlan{Resolved{30, proto.Res720p, 4000}, 30, 4000, 4000, 1280, 720, 0, 720, 0}},
		{"custom 15 fps 720p 5 Mbps", with(func(in *StreamInput) {
			in.Settings, in.BweKbps = StreamSettings{Preset: proto.PresetCustom, FPS: 15, Res: proto.Res720p, MaxKbps: 5000}, 9000
		}), StreamPlan{Resolved{15, proto.Res720p, 5000}, 15, 5000, 5000, 1280, 720, 0, 720, 0}},
		{"background keeps the size", with(func(in *StreamInput) { in.Settings, in.Background = StreamSettings{Preset: proto.PresetQuality}, true }),
			StreamPlan{Resolved{30, proto.ResNative, 50000}, 5, 1000, 50000, 1920, 1080, 0, 0, 0}},
		{"background keeps auto's rung size", with(func(in *StreamInput) { in.Rung, in.Background = low, true }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 5, 1000, 2345, 1280, 720, 0, 720, 0}},
		{"background never raises fps", with(func(in *StreamInput) {
			in.Settings, in.Background = StreamSettings{Preset: proto.PresetCustom, FPS: 3}, true
		}), StreamPlan{Resolved{3, proto.ResNative, 0}, 3, 1000, 2009, 1920, 1080, 0, 0, 0}},
		{"full color raises the automatic ceiling", with(func(in *StreamInput) {
			in.Settings.Color, in.FullColor, in.BweKbps = proto.ColorFull, true, 20000
		}), StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 15156, 15156, 1920, 1080, 0, 0, platform.CodecVP9}},
		{"full color asked for but not negotiated", with(func(in *StreamInput) { in.Settings.Color = proto.ColorFull }),
			StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 5000, 12125, 1920, 1080, 0, 0, platform.CodecH264}},
		{"full color keeps the relay ceiling", with(func(in *StreamInput) {
			in.Settings.Color, in.FullColor, in.Relay, in.BweKbps = proto.ColorFull, true, true, 20000
		}), StreamPlan{Resolved{30, proto.ResNative, 0}, 30, 6000, 6000, 1920, 1080, 0, 0, platform.CodecVP9}},
		{"full color keeps a manual cap", with(func(in *StreamInput) {
			in.Settings = StreamSettings{Preset: proto.PresetCustom, MaxKbps: 9000, Color: proto.ColorFull}
			in.FullColor, in.BweKbps = true, 20000
		}), StreamPlan{Resolved{60, proto.ResNative, 9000}, 60, 9000, 9000, 1920, 1080, 0, 0, platform.CodecVP9}},
	}
	for _, c := range cases {
		if got := PlanStream(c.in); got != c.want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestBuildStreamStateJSON(t *testing.T) {
	id := uint32(7)
	in := StreamInput{Settings: StreamSettings{Preset: proto.PresetAuto, Res: proto.Res1440p}, MaxFPS: 30,
		NativeW: 5120, NativeH: 2880, BweKbps: 5000, Rung: &Rung{FPS: 15, Res: proto.Res1440p, W: 2560, H: 1440}}
	st := BuildStreamState(in, PlanStream(in), &AutoInfo{Rung: 2, Rungs: 6, Reason: AutoCongestion})
	st.ID, st.OK = &id, true
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"t":"stream","id":7,"ok":true,"preset":"auto","fps":null,"res":"1440p","max_kbps":null,"background":false,"wallpaper":false,` +
		`"color":null,"resolved":{"fps":30,"res":"1440p","max_kbps":null,"color":"standard"},` +
		`"target_fps":15,"max_fps":30,"width":2560,"height":1440,"native_width":5120,"native_height":2880,` +
		`"ceil_kbps":9383,"relay":false,"auto":{"rung":2,"rungs":6,"reason":"congestion"},` +
		`"options":{"fps":[5,10,15,24,30],"res":[{"id":"native","width":5120,"height":2880},{"id":"1440p","width":2560,"height":1440},` +
		`{"id":"1080p","width":1920,"height":1080},{"id":"720p","width":1280,"height":720},{"id":"half","width":2560,"height":1440}],` +
		`"color":["standard"]}}`
	if string(b) != want {
		t.Fatalf("state JSON\n got %s\nwant %s", b, want)
	}
}

func TestBuildStreamStateNonAuto(t *testing.T) {
	in := StreamInput{Settings: StreamSettings{Preset: proto.PresetCustom, FPS: 15, MaxKbps: 3000}, MaxFPS: 60,
		NativeW: 1920, NativeH: 1080, Relay: true}
	st := BuildStreamState(in, PlanStream(in), nil)
	b, _ := json.Marshal(st)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	want := map[string]any{"fps": float64(15), "res": nil, "max_kbps": float64(3000), "auto": nil, "relay": true,
		"resolved": map[string]any{"fps": float64(15), "res": "native", "max_kbps": float64(3000), "color": "standard"}}
	for k, v := range want {
		if !reflect.DeepEqual(m[k], v) {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["id"]; ok {
		t.Error("id must be omitted when the request carried none")
	}
}

func TestFullColorState(t *testing.T) {
	cases := []struct {
		name      string
		color     string
		available bool
		asked     any
		resolved  string
		options   []any
	}{
		{"standard", "", true, nil, proto.ColorStandard, []any{"standard", "full"}},
		{"full", proto.ColorFull, true, "full", proto.ColorFull, []any{"standard", "full"}},
		{"full where the browser cannot", proto.ColorFull, false, "full", proto.ColorStandard, []any{"standard"}},
	}
	for _, c := range cases {
		in := StreamInput{Settings: StreamSettings{Preset: proto.PresetAuto, Color: c.color}, MaxFPS: 30,
			NativeW: 1920, NativeH: 1080, FullColor: c.available}
		b, _ := json.Marshal(BuildStreamState(in, PlanStream(in), nil))
		var m struct {
			Color    any            `json:"color"`
			Resolved map[string]any `json:"resolved"`
			Options  map[string]any `json:"options"`
		}
		_ = json.Unmarshal(b, &m)
		if !reflect.DeepEqual(m.Color, c.asked) || m.Resolved["color"] != c.resolved || !reflect.DeepEqual(m.Options["color"], c.options) {
			t.Errorf("%s: color %v resolved %v options %v, want %v %v %v", c.name, m.Color, m.Resolved["color"],
				m.Options["color"], c.asked, c.resolved, c.options)
		}
	}
}

func TestSettingsFromRequestColor(t *testing.T) {
	for _, c := range []struct{ json, want string }{
		{`{"t":"stream","preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false}`, ""},
		{`{"t":"stream","preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false,"color":"standard"}`, ""},
		{`{"t":"stream","preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false,"color":"full"}`, proto.ColorFull},
	} {
		r, err := proto.DecodeStreamRequest([]byte(c.json))
		if err != nil {
			t.Fatal(err)
		}
		if got := SettingsFromRequest(r).Color; got != c.want {
			t.Errorf("%s: Color = %q, want %q", c.json, got, c.want)
		}
	}
}

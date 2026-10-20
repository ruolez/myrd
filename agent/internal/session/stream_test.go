package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func TestEncoderMaxFPS(t *testing.T) {
	for enc, want := range map[string]int{
		"x264": 30, "openh264": 30, "mf-sw": 30, "videotoolbox-sw": 30, "none": 30,
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
		{"auto", StreamSettings{Preset: proto.PresetAuto}, 60, Resolved{60, proto.ResNative, 0}},
		{"auto software", StreamSettings{Preset: proto.PresetAuto}, 30, Resolved{30, proto.ResNative, 0}},
		{"auto ceilings", StreamSettings{Preset: proto.PresetAuto, FPS: 30, Res: proto.Res1080p, MaxKbps: 5000}, 60,
			Resolved{30, proto.Res1080p, 5000}},
		{"speed", StreamSettings{Preset: proto.PresetSpeed}, 60, Resolved{60, proto.Res720p, 4000}},
		{"speed software", StreamSettings{Preset: proto.PresetSpeed}, 30, Resolved{30, proto.Res720p, 4000}},
		{"balanced", StreamSettings{Preset: proto.PresetBalanced}, 60, Resolved{30, proto.Res1080p, 8000}},
		{"balanced low cap", StreamSettings{Preset: proto.PresetBalanced}, 24, Resolved{24, proto.Res1080p, 8000}},
		{"quality", StreamSettings{Preset: proto.PresetQuality}, 60, Resolved{30, proto.ResNative, 20000}},
		{"quality fps override", StreamSettings{Preset: proto.PresetQuality, FPS: 60}, 60, Resolved{60, proto.ResNative, 20000}},
		{"custom defaults", StreamSettings{Preset: proto.PresetCustom}, 60, Resolved{60, proto.ResNative, 0}},
		{"custom all", StreamSettings{Preset: proto.PresetCustom, FPS: 15, Res: proto.Res720p, MaxKbps: 5000}, 60,
			Resolved{15, proto.Res720p, 5000}},
		{"custom fps above encoder cap", StreamSettings{Preset: proto.PresetCustom, FPS: 60}, 30, Resolved{30, proto.ResNative, 0}},
		{"unknown preset behaves as auto", StreamSettings{Preset: "turbo"}, 60, Resolved{60, proto.ResNative, 0}},
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
		w, h       uint32
	}{
		{"native", 1920, 1080, proto.ResNative, 0, 0, 1920, 1080},
		{"1080p on 1080p", 1920, 1080, proto.Res1080p, 0, 0, 1920, 1080},
		{"1440p never upscales", 1920, 1080, proto.Res1440p, 0, 0, 1920, 1080},
		{"720p", 1920, 1080, proto.Res720p, 0, 0, 1280, 720},
		{"half", 1920, 1080, proto.ResHalf, 0, 0, 960, 540},
		{"ultrawide caps the short edge", 3440, 1440, proto.Res1080p, 0, 0, 2580, 1080},
		{"portrait caps the short edge", 1080, 1920, proto.Res720p, 0, 0, 720, 1280},
		{"5K 1440p", 5120, 2880, proto.Res1440p, 0, 0, 2560, 1440},
		{"5K half", 5120, 2880, proto.ResHalf, 0, 0, 2560, 1440},
		{"16:10 1080p", 2560, 1600, proto.Res1080p, 0, 0, 1728, 1080},
		{"odd laptop 720p even dims", 1366, 768, proto.Res720p, 0, 0, 1280, 720},
		{"odd half even dims", 1366, 769, proto.ResHalf, 0, 0, 682, 384},
		{"options cap applies to native", 3840, 2160, proto.ResNative, 1920, 0, 1920, 1080},
		{"options cap and res cap", 3840, 2160, proto.Res720p, 1920, 0, 1280, 720},
		{"empty res is native", 2560, 1440, "", 0, 0, 2560, 1440},
	}
	for _, c := range cases {
		if w, h := OutputSize(c.nw, c.nh, c.res, c.maxW, c.maxH); w != c.w || h != c.h {
			t.Errorf("%s: OutputSize(%dx%d, %s) = %dx%d, want %dx%d", c.name, c.nw, c.nh, c.res, w, h, c.w, c.h)
		}
	}
}

func TestCaptureBoxReproducesOutputSize(t *testing.T) {
	// The native layers fit the native size into the box with ScaledSize's
	// arithmetic; the box must land exactly on the size the agent reports.
	sizes := [][2]uint32{{1920, 1080}, {3440, 1440}, {1080, 1920}, {5120, 2880}, {1366, 768}, {2560, 1600}, {1280, 1024}}
	for _, sz := range sizes {
		for _, res := range []string{proto.ResNative, proto.Res1440p, proto.Res1080p, proto.Res720p, proto.ResHalf} {
			bw, bh := CaptureBox(sz[0], sz[1], res, 0, 0)
			gw, gh := ScaledSize(sz[0], sz[1], bw, bh)
			if w, h := OutputSize(sz[0], sz[1], res, 0, 0); gw != w || gh != h {
				t.Errorf("%v %s: box %dx%d gives %dx%d, OutputSize %dx%d", sz, res, bw, bh, gw, gh, w, h)
			}
		}
	}
}

func TestResolutionOptions(t *testing.T) {
	got := ResolutionOptions(5120, 2880, 0, 0)
	want := []ResOption{
		{proto.ResNative, 5120, 2880}, {proto.Res1440p, 2560, 1440}, {proto.Res1080p, 1920, 1080},
		{proto.Res720p, 1280, 720}, {proto.ResHalf, 2560, 1440},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolutionOptions = %+v, want %+v", got, want)
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
			Prefs  struct {
				Preset  string  `json:"preset"`
				FPS     *int    `json:"fps"`
				Res     *string `json:"res"`
				MaxKbps *int    `json:"max_kbps"`
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
		want := StreamSettings{Preset: c.Prefs.Preset}
		if c.Prefs.FPS != nil {
			want.FPS = *c.Prefs.FPS
		}
		if c.Prefs.Res != nil {
			want.Res = *c.Prefs.Res
		}
		if c.Prefs.MaxKbps != nil {
			want.MaxKbps = *c.Prefs.MaxKbps
		}
		if got := SettingsFromRequest(r); got != want || r.Background != c.Hidden {
			t.Errorf("%s: settings %+v (bg %v), want %+v (bg %v)", c.Name, got, r.Background, want, c.Hidden)
		}
	}
}

func TestPlanStream(t *testing.T) {
	top := &Rung{FPS: 60, Res: proto.ResNative, W: 1920, H: 1080}
	low := &Rung{FPS: 15, Res: proto.Res720p, W: 1280, H: 720}
	base := StreamInput{Settings: StreamSettings{Preset: proto.PresetAuto}, MaxFPS: 60, NativeW: 1920, NativeH: 1080, BweKbps: 5000, Rung: top}
	with := func(f func(*StreamInput)) StreamInput { in := base; f(&in); return in }
	cases := []struct {
		name string
		in   StreamInput
		want StreamPlan
	}{
		{"auto top rung", base,
			StreamPlan{Resolved{60, proto.ResNative, 0}, 60, 5000, 8000, 1920, 1080, 0, 0}},
		{"auto lower rung restarts smaller", with(func(in *StreamInput) { in.Rung = low }),
			StreamPlan{Resolved{60, proto.ResNative, 0}, 15, 3555, 3555, 1280, 720, 0, 720}},
		{"auto without a rung uses the ceilings", with(func(in *StreamInput) { in.Rung = nil }),
			StreamPlan{Resolved{60, proto.ResNative, 0}, 60, 5000, 8000, 1920, 1080, 0, 0}},
		{"auto relay ceiling", with(func(in *StreamInput) { in.Relay, in.BweKbps = true, 12000 }),
			StreamPlan{Resolved{60, proto.ResNative, 0}, 60, 6000, 6000, 1920, 1080, 0, 0}},
		{"estimate below the floor", with(func(in *StreamInput) { in.BweKbps = 200 }),
			StreamPlan{Resolved{60, proto.ResNative, 0}, 60, 1000, 8000, 1920, 1080, 0, 0}},
		{"quality manual cap", with(func(in *StreamInput) { in.Settings, in.BweKbps = StreamSettings{Preset: proto.PresetQuality}, 25000 }),
			StreamPlan{Resolved{30, proto.ResNative, 20000}, 30, 20000, 20000, 1920, 1080, 0, 0}},
		{"manual cap clamped on relay", with(func(in *StreamInput) {
			in.Settings, in.Relay, in.BweKbps = StreamSettings{Preset: proto.PresetQuality}, true, 25000
		}), StreamPlan{Resolved{30, proto.ResNative, 20000}, 30, 6000, 6000, 1920, 1080, 0, 0}},
		{"speed on a software encoder", with(func(in *StreamInput) { in.Settings, in.MaxFPS = StreamSettings{Preset: proto.PresetSpeed}, 30 }),
			StreamPlan{Resolved{30, proto.Res720p, 4000}, 30, 4000, 4000, 1280, 720, 0, 720}},
		{"custom 15 fps 720p 5 Mbps", with(func(in *StreamInput) {
			in.Settings, in.BweKbps = StreamSettings{Preset: proto.PresetCustom, FPS: 15, Res: proto.Res720p, MaxKbps: 5000}, 9000
		}), StreamPlan{Resolved{15, proto.Res720p, 5000}, 15, 5000, 5000, 1280, 720, 0, 720}},
		{"background keeps the size", with(func(in *StreamInput) { in.Settings, in.Background = StreamSettings{Preset: proto.PresetQuality}, true }),
			StreamPlan{Resolved{30, proto.ResNative, 20000}, 5, 1000, 20000, 1920, 1080, 0, 0}},
		{"background keeps auto's rung size", with(func(in *StreamInput) { in.Rung, in.Background = low, true }),
			StreamPlan{Resolved{60, proto.ResNative, 0}, 5, 1000, 3555, 1280, 720, 0, 720}},
		{"background never raises fps", with(func(in *StreamInput) {
			in.Settings, in.Background = StreamSettings{Preset: proto.PresetCustom, FPS: 3}, true
		}), StreamPlan{Resolved{3, proto.ResNative, 0}, 3, 1000, 8000, 1920, 1080, 0, 0}},
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
	want := `{"t":"stream","id":7,"ok":true,"preset":"auto","fps":null,"res":"1440p","max_kbps":null,"background":false,` +
		`"resolved":{"fps":30,"res":"1440p","max_kbps":null},` +
		`"target_fps":15,"max_fps":30,"width":2560,"height":1440,"native_width":5120,"native_height":2880,` +
		`"ceil_kbps":14222,"relay":false,"auto":{"rung":2,"rungs":6,"reason":"congestion"},` +
		`"options":{"fps":[5,10,15,24,30],"res":[{"id":"native","width":5120,"height":2880},{"id":"1440p","width":2560,"height":1440},` +
		`{"id":"1080p","width":1920,"height":1080},{"id":"720p","width":1280,"height":720},{"id":"half","width":2560,"height":1440}]}}`
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
		"resolved": map[string]any{"fps": float64(15), "res": "native", "max_kbps": float64(3000)}}
	for k, v := range want {
		if !reflect.DeepEqual(m[k], v) {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["id"]; ok {
		t.Error("id must be omitted when the request carried none")
	}
}

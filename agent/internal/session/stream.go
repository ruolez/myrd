package session

import "github.com/ruolez/remotectl/proto"

// Stream settings (docs/PROTOCOL.md §5, "Stream settings"): what the browser
// asked for, what that resolves to on this encoder and monitor, and the
// encoder plan derived from it. Everything here is pure; mediaPipe applies it.

const (
	speedKbps     = 4000
	balancedKbps  = 8000
	qualityKbps   = proto.StreamMaxKbps
	backgroundFPS = 5
)

// StreamSettings is the browser's desired state. Zero values mean "preset
// default": FPS 0, Res "" and MaxKbps 0 (automatic ceiling).
type StreamSettings struct {
	Preset  string
	FPS     int
	Res     string
	MaxKbps int
	View    View
}

// View is the viewer's video area in device pixels, even; zero when the
// viewer shows the stream 1:1 or does not report it.
type View struct{ W, H int }

func (v View) set() bool { return v.W > 0 && v.H > 0 }

// DefaultStreamSettings applies until the browser sends `stream`.
func DefaultStreamSettings() StreamSettings { return StreamSettings{Preset: proto.PresetAuto} }

// SettingsFromRequest maps a validated B→A `stream` message.
func SettingsFromRequest(r proto.StreamRequest) StreamSettings {
	s := StreamSettings{Preset: r.Preset}
	if r.FPS != nil {
		s.FPS = *r.FPS
	}
	if r.Res != nil {
		s.Res = *r.Res
	}
	if r.MaxKbps != nil {
		s.MaxKbps = *r.MaxKbps
	}
	if r.View != nil {
		s.View = View{r.View.W &^ 1, r.View.H &^ 1}
	}
	return s
}

// fullRes is the size of presets that do not cap it: the viewer's window,
// so the browser shows the stream pixel for pixel, else native.
func fullRes(s StreamSettings) string {
	if s.View.set() {
		return proto.ResWindow
	}
	return proto.ResNative
}

// LegacyTierSettings maps the input channel's op 3 (old browsers): 0 keeps the
// settings and goes to background, 1 is 30 fps at native size, 2 is Auto.
func LegacyTierSettings(tier int, cur StreamSettings) (StreamSettings, bool) {
	switch tier {
	case TierBackground:
		return cur, true
	case TierBalanced:
		return StreamSettings{Preset: proto.PresetCustom, FPS: 30, Res: proto.ResNative}, false
	default:
		return StreamSettings{Preset: proto.PresetAuto}, false
	}
}

// EncoderMaxFPS is the frame-rate cap for an encoder: software encoders
// cannot keep up with 60 fps at desktop sizes.
func EncoderMaxFPS(encoder string) int {
	switch encoder {
	case "x264", "openh264", "mf-sw", "videotoolbox-sw", "none":
		return 30
	}
	return 60
}

// Resolved is what a preset plus overrides comes to. MaxKbps 0 = automatic.
type Resolved struct {
	FPS     int
	Res     string
	MaxKbps int
}

func ResolveSettings(s StreamSettings, maxFPS int) Resolved {
	var r Resolved
	switch s.Preset {
	case proto.PresetSpeed:
		r = Resolved{maxFPS, proto.Res720p, speedKbps}
	case proto.PresetBalanced:
		r = Resolved{min(30, maxFPS), proto.Res1080p, balancedKbps}
	case proto.PresetQuality:
		r = Resolved{min(30, maxFPS), fullRes(s), qualityKbps}
	default: // auto, custom
		r = Resolved{maxFPS, fullRes(s), 0}
	}
	if s.FPS > 0 {
		r.FPS = s.FPS
	}
	if s.Res != "" {
		r.Res = s.Res
	}
	if s.MaxKbps > 0 {
		r.MaxKbps = s.MaxKbps
	}
	r.FPS = max(1, min(r.FPS, maxFPS))
	return r
}

func shortEdgeCap(res string) int {
	switch res {
	case proto.Res1440p:
		return 1440
	case proto.Res1080p:
		return 1080
	case proto.Res720p:
		return 720
	}
	return 0
}

func minBound(a, b int) int {
	if a == 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

// CaptureBox is the MaxWidth/MaxHeight bound handed to the native layer for
// res: named sizes cap the short edge, half halves the width, window fits
// the view (ScaledSize keeps the aspect). maxW/maxH are Options caps (0 = none).
func CaptureBox(nw, nh uint32, res string, maxW, maxH int, view View) (int, int) {
	var bw, bh int
	if c := shortEdgeCap(res); c > 0 {
		if nh <= nw {
			bh = c
		} else {
			bw = c
		}
	} else if res == proto.ResHalf {
		bw = max(2, int(nw)/2)
	} else if res == proto.ResWindow && view.set() {
		bw, bh = view.W, view.H
	}
	return minBound(bw, maxW), minBound(bh, maxH)
}

// OutputSize is the encoded size for res: never upscaled, even dimensions.
func OutputSize(nw, nh uint32, res string, maxW, maxH int, view View) (uint32, uint32) {
	bw, bh := CaptureBox(nw, nh, res, maxW, maxH, view)
	return ScaledSize(nw, nh, bw, bh)
}

type ResOption struct {
	ID     string `json:"id"`
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
}

var resOrder = []string{proto.ResWindow, proto.ResNative, proto.Res1440p, proto.Res1080p, proto.Res720p, proto.ResHalf}

// ResolutionOptions lists every resolution choice with the size it gives on
// this monitor; window only for a viewer that reports its view.
func ResolutionOptions(nw, nh uint32, maxW, maxH int, view View) []ResOption {
	out := make([]ResOption, 0, len(resOrder))
	for _, id := range resOrder {
		if id == proto.ResWindow && !view.set() {
			continue
		}
		w, h := OutputSize(nw, nh, id, maxW, maxH, view)
		out = append(out, ResOption{id, w, h})
	}
	return out
}

var fpsChoices = []int{5, 10, 15, 24, 30, 60}

func FPSOptions(maxFPS int) []int {
	var out []int
	for _, f := range fpsChoices {
		if f <= maxFPS {
			out = append(out, f)
		}
	}
	return out
}

// StreamInput is everything a plan depends on. Rung is Auto's current step
// (nil when the preset is not auto).
type StreamInput struct {
	Settings         StreamSettings
	Background       bool
	Wallpaper        bool // shown; does not change the plan
	MaxFPS           int
	NativeW, NativeH uint32
	MaxW, MaxH       int // Options caps
	Relay            bool
	BweKbps          int
	Rung             *Rung
}

// StreamPlan is what the encoder runs: BoxW/BoxH go to CaptureConfig and give
// W×H; a size change needs a capture restart, the rest is ConfigureEncoder.
type StreamPlan struct {
	Resolved            Resolved
	FPS, Kbps, CeilKbps int
	W, H                uint32
	BoxW, BoxH          int
}

func PlanStream(in StreamInput) StreamPlan {
	r := ResolveSettings(in.Settings, in.MaxFPS)
	p := StreamPlan{Resolved: r, FPS: r.FPS}
	res := r.Res
	if in.Rung != nil && in.Settings.Preset == proto.PresetAuto {
		p.FPS, res = in.Rung.FPS, in.Rung.Res
	}
	p.BoxW, p.BoxH = CaptureBox(in.NativeW, in.NativeH, res, in.MaxW, in.MaxH, in.Settings.View)
	p.W, p.H = ScaledSize(in.NativeW, in.NativeH, p.BoxW, p.BoxH)
	if r.MaxKbps > 0 {
		p.CeilKbps = r.MaxKbps
		if in.Relay {
			p.CeilKbps = min(p.CeilKbps, relayCeilKbps)
		}
	} else {
		p.CeilKbps = CeilingKbps(p.W, p.H, p.FPS, in.Relay)
	}
	p.Kbps = EncoderTarget(in.BweKbps, p.CeilKbps)
	if in.Background {
		p.FPS, p.Kbps = min(backgroundFPS, p.FPS), floorKbps
	}
	return p
}

// AutoInfo is the `auto` object of the stream state.
type AutoInfo struct {
	Rung   int    `json:"rung"`
	Rungs  int    `json:"rungs"`
	Reason string `json:"reason"`
}

type streamResolved struct {
	FPS     int    `json:"fps"`
	Res     string `json:"res"`
	MaxKbps *int   `json:"max_kbps"`
}

type streamOptions struct {
	FPS []int       `json:"fps"`
	Res []ResOption `json:"res"`
}

// StreamState is the A→B `stream` message.
type StreamState struct {
	T            string         `json:"t"`
	ID           *uint32        `json:"id,omitempty"`
	OK           bool           `json:"ok"`
	Preset       string         `json:"preset"`
	FPS          *int           `json:"fps"`
	Res          *string        `json:"res"`
	MaxKbps      *int           `json:"max_kbps"`
	Background   bool           `json:"background"`
	Wallpaper    bool           `json:"wallpaper"`
	Resolved     streamResolved `json:"resolved"`
	TargetFPS    int            `json:"target_fps"`
	MaxFPS       int            `json:"max_fps"`
	Width        uint32         `json:"width"`
	Height       uint32         `json:"height"`
	NativeWidth  uint32         `json:"native_width"`
	NativeHeight uint32         `json:"native_height"`
	CeilKbps     int            `json:"ceil_kbps"`
	Relay        bool           `json:"relay"`
	Auto         *AutoInfo      `json:"auto"`
	Options      streamOptions  `json:"options"`
}

func nonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// BuildStreamState describes in/plan; the caller sets ID and OK.
func BuildStreamState(in StreamInput, plan StreamPlan, auto *AutoInfo) StreamState {
	s := in.Settings
	return StreamState{T: "stream", Preset: s.Preset, FPS: nonZero(s.FPS), Res: nonZero(s.Res), MaxKbps: nonZero(s.MaxKbps),
		Background: in.Background, Wallpaper: in.Wallpaper,
		Resolved:  streamResolved{plan.Resolved.FPS, plan.Resolved.Res, nonZero(plan.Resolved.MaxKbps)},
		TargetFPS: plan.FPS, MaxFPS: in.MaxFPS, Width: plan.W, Height: plan.H, NativeWidth: in.NativeW, NativeHeight: in.NativeH,
		CeilKbps: plan.CeilKbps, Relay: in.Relay, Auto: auto,
		Options: streamOptions{FPSOptions(in.MaxFPS), ResolutionOptions(in.NativeW, in.NativeH, in.MaxW, in.MaxH, s.View)}}
}

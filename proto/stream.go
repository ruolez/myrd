package proto

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Stream settings on the `control` data channel (docs/PROTOCOL.md §5,
// "Stream settings"). Shared by the agent and its tests; the browser mirrors
// the encoding in web/src/lib/stream.ts against docs/test-vectors.json.

const (
	PresetAuto     = "auto"
	PresetSpeed    = "speed"
	PresetBalanced = "balanced"
	PresetQuality  = "quality"
	PresetCustom   = "custom"

	ResNative = "native"
	ResWindow = "window" // the viewer's window (StreamRequest.View); native without one
	Res1440p  = "1440p"
	Res1080p  = "1080p"
	Res720p   = "720p"
	ResHalf   = "half"

	ColorStandard = "standard" // 4:2:0 H.264
	ColorFull     = "full"     // 4:4:4 (VP9 profile 1) where the agent and browser can

	StreamMinFPS  = 1
	StreamMaxFPS  = 60
	StreamMinKbps = 1000
	StreamMaxKbps = 50000
	StreamMaxView = 16384
)

// StreamRequest is the browser's full desired state (B→A `stream`).
type StreamRequest struct {
	T          string  `json:"t"`
	ID         *uint32 `json:"id,omitempty"`
	Preset     string  `json:"preset"`
	FPS        *int    `json:"fps"`
	Res        *string `json:"res"`
	MaxKbps    *int    `json:"max_kbps"`
	Background bool    `json:"background"`
	Wallpaper  bool    `json:"wallpaper"` // show the desktop wallpaper; absent = hidden
	Color      string  `json:"color,omitempty"` // ColorStandard (also absent) or ColorFull
	// View is the viewer's video area in device pixels. Absent: the viewer
	// shows the stream at its own size (1:1) or predates the field.
	View *StreamView `json:"view,omitempty"`
}

type StreamView struct {
	W int `json:"w"`
	H int `json:"h"`
}

var errNotStream = errors.New("not a stream message")

// DecodeStreamRequest parses and validates a B→A `stream` message. Wrong
// field types fail in decoding, out-of-range values in Validate.
func DecodeStreamRequest(b []byte) (StreamRequest, error) {
	var r StreamRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	return r, r.Validate()
}

func (r StreamRequest) Validate() error {
	if r.T != "stream" {
		return errNotStream
	}
	switch r.Preset {
	case PresetAuto, PresetSpeed, PresetBalanced, PresetQuality, PresetCustom:
	default:
		return fmt.Errorf("unknown preset %q", r.Preset)
	}
	if r.FPS != nil && (*r.FPS < StreamMinFPS || *r.FPS > StreamMaxFPS) {
		return fmt.Errorf("fps %d out of range", *r.FPS)
	}
	if r.Res != nil {
		switch *r.Res {
		case ResNative, ResWindow, Res1440p, Res1080p, Res720p, ResHalf:
		default:
			return fmt.Errorf("unknown resolution %q", *r.Res)
		}
	}
	if r.MaxKbps != nil && (*r.MaxKbps < StreamMinKbps || *r.MaxKbps > StreamMaxKbps) {
		return fmt.Errorf("max_kbps %d out of range", *r.MaxKbps)
	}
	if r.Color != "" && r.Color != ColorStandard && r.Color != ColorFull {
		return fmt.Errorf("unknown color %q", r.Color)
	}
	if v := r.View; v != nil && (v.W < 2 || v.H < 2 || v.W > StreamMaxView || v.H > StreamMaxView) {
		return fmt.Errorf("view %dx%d out of range", v.W, v.H)
	}
	return nil
}

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
	Res1440p  = "1440p"
	Res1080p  = "1080p"
	Res720p   = "720p"
	ResHalf   = "half"

	StreamMinFPS  = 1
	StreamMaxFPS  = 60
	StreamMinKbps = 1000
	StreamMaxKbps = 20000
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
		case ResNative, Res1440p, Res1080p, Res720p, ResHalf:
		default:
			return fmt.Errorf("unknown resolution %q", *r.Res)
		}
	}
	if r.MaxKbps != nil && (*r.MaxKbps < StreamMinKbps || *r.MaxKbps > StreamMaxKbps) {
		return fmt.Errorf("max_kbps %d out of range", *r.MaxKbps)
	}
	return nil
}

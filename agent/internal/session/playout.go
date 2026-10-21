package session

import (
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

const playoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"

// playoutDelayFactory tags every outgoing video packet with playout-delay
// min=max=0 when the browser negotiated the extension (Chrome does). Chrome
// then renders each frame as soon as it is decoded and switches to its
// low-latency renderer; jitterBufferTarget=0 alone leaves the maximum at its
// default, which keeps the smoothing path.
type playoutDelayFactory struct{}

func (playoutDelayFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &playoutDelay{}, nil
}

type playoutDelay struct{ interceptor.NoOp }

func (*playoutDelay) BindLocalStream(info *interceptor.StreamInfo, w interceptor.RTPWriter) interceptor.RTPWriter {
	var id uint8
	for _, e := range info.RTPHeaderExtensions {
		if e.URI == playoutDelayURI {
			id = uint8(e.ID)
		}
	}
	ext, err := rtp.PlayoutDelayExtension{}.Marshal()
	if id == 0 || err != nil {
		return w
	}
	return interceptor.RTPWriterFunc(func(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
		if err := h.SetExtension(id, ext); err != nil {
			return 0, err
		}
		return w.Write(h, payload, a)
	})
}

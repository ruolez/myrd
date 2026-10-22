// Package preview serves dashboard previews (PROTOCOL §3 "Screen previews"):
// on the server's request it takes one small picture of the screen and seals
// it for each browser that may see it (proto.SealPreview). The device
// decides: its own switch, pause, "Ask every time", permissions and the
// pinned keys all apply here, whatever the server asks.
package preview

import "github.com/ruolez/remotectl/proto"

// DeviceState is what applies to every recipient of a request.
type DeviceState struct {
	PreviewsOff      bool // the device's "Show screen previews" is off
	Paused           bool
	Updating         bool
	AskEveryTime     bool // the owner wants a prompt for every session
	ScreenPermission bool
	InSession        bool
}

// deviceReason is why no recipient gets a preview now, or "".
func deviceReason(s DeviceState) string {
	switch {
	case s.PreviewsOff:
		return proto.PreviewOff
	case s.Paused:
		return proto.PreviewPaused
	case s.Updating:
		return proto.PreviewUpdating
	case s.AskEveryTime:
		return proto.PreviewAskEveryTime
	case !s.ScreenPermission:
		return proto.PreviewNoPermission
	case s.InSession:
		return proto.PreviewInSession
	}
	return ""
}

// KeyState is what applies to one recipient.
type KeyState struct {
	Revoked  bool
	Pinned   bool // this exact key is pinned for the recipient's user
	KeySigOK bool // the client key vouches for the preview key
}

func keyReason(k KeyState) string {
	switch {
	case k.Revoked || !k.Pinned:
		return proto.PreviewUntrusted
	case !k.KeySigOK:
		return proto.PreviewBadKey
	}
	return ""
}

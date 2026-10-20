package session

import (
	"sync"

	"github.com/ruolez/remotectl/agent/internal/inputproto"
	"github.com/ruolez/remotectl/agent/internal/keymap"
	"github.com/ruolez/remotectl/agent/internal/platform"
)

// maxRejects ends the session with input_abuse when exceeded.
const maxRejects = 200

// mediaControl is what input needs from the media pipe (monitors, quality,
// keyframes, clipboard); an interface so input handling is testable alone.
type mediaControl interface {
	monitorID(index int) (uint32, bool)
	selectMonitor(index int) error
	setTier(tier int)
	requestKeyframe()
	setRemoteClipboard(text string)
}

func (p *mediaPipe) monitorID(i int) (uint32, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.monitors) == 0 && i == 0 {
		return 0, true
	}
	if i < 0 || i >= len(p.monitors) {
		return 0, false
	}
	return p.monitors[i].ID, true
}

func (p *mediaPipe) requestKeyframe() { p.b.RequestKeyframe() }

// inputState decodes input-channel messages and injects them, tracking held
// keys so nothing stays stuck when the session ends.
type inputState struct {
	b  platform.Backend
	km keymap.Table

	mu      sync.Mutex
	pressed map[uint16]keymap.Entry
	buttons map[uint8]bool
	nrej    int
	clip    inputproto.ClipboardAssembler
}

func newInputState(b platform.Backend, km keymap.Table) *inputState {
	return &inputState{b: b, km: km, pressed: map[uint16]keymap.Entry{}, buttons: map[uint8]bool{}}
}

func (in *inputState) rejects() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.nrej
}

func (in *inputState) reject(err error) error {
	in.mu.Lock()
	in.nrej++
	in.mu.Unlock()
	return err
}

// handle processes one message; a non-nil error means it was rejected.
func (in *inputState) handle(raw []byte, mc mediaControl) error {
	ev, err := inputproto.Decode(raw)
	if err != nil {
		return in.reject(err)
	}
	switch ev.Type {
	case inputproto.TypeMouseMove:
		id, ok := mc.monitorID(int(ev.Monitor))
		if !ok {
			return in.reject(inputproto.ErrRange)
		}
		return in.b.Inject(platform.InputEvent{Type: platform.EvMouseMove, MonitorID: id, X: ev.X, Y: ev.Y})
	case inputproto.TypeMouseMoveRel:
		return in.b.Inject(platform.InputEvent{Type: platform.EvMouseMoveRel, DX: ev.DX, DY: ev.DY})
	case inputproto.TypeMouseButton:
		in.mu.Lock()
		if ev.Down {
			in.buttons[ev.Button] = true
		} else {
			delete(in.buttons, ev.Button)
		}
		in.mu.Unlock()
		return in.b.Inject(platform.InputEvent{Type: platform.EvMouseButton, Button: ev.Button, Down: ev.Down})
	case inputproto.TypeMouseWheel:
		return in.b.Inject(platform.InputEvent{Type: platform.EvMouseWheel, DX: ev.DX, DY: ev.DY})
	case inputproto.TypeKey:
		e, ok := in.km[ev.Usage]
		if !ok {
			return in.reject(inputproto.ErrRange)
		}
		in.mu.Lock()
		if ev.Down {
			in.pressed[ev.Usage] = e
		} else {
			delete(in.pressed, ev.Usage)
		}
		in.mu.Unlock()
		return in.b.Inject(platform.InputEvent{Type: platform.EvKey, Keycode: e.Code, Flags: e.Flags, Down: ev.Down})
	case inputproto.TypeText:
		return in.b.InjectText(ev.Text)
	case inputproto.TypeClipboardBegin, inputproto.TypeClipboardData:
		in.mu.Lock()
		done, mime, data, err := in.clip.Feed(ev)
		in.mu.Unlock()
		if err != nil {
			return in.reject(err)
		}
		if done && mime == inputproto.MimeText {
			mc.setRemoteClipboard(string(data))
		}
		return nil
	case inputproto.TypeControl:
		switch ev.Op {
		case inputproto.OpSelectMonitor:
			if err := mc.selectMonitor(int(ev.Arg)); err != nil {
				return in.reject(err)
			}
		case inputproto.OpRequestKeyframe:
			mc.requestKeyframe()
		case inputproto.OpSetQuality:
			mc.setTier(int(ev.Arg))
		case inputproto.OpReleaseAll:
			in.releaseAll()
		}
		return nil
	}
	return in.reject(inputproto.ErrUnknownType)
}

// releaseAll sends key-up for every held key and button-up for every held
// button (session end, blur, explicit op 4).
func (in *inputState) releaseAll() {
	in.mu.Lock()
	keys := in.pressed
	buttons := in.buttons
	in.pressed = map[uint16]keymap.Entry{}
	in.buttons = map[uint8]bool{}
	in.mu.Unlock()
	for _, e := range keys {
		_ = in.b.Inject(platform.InputEvent{Type: platform.EvKey, Keycode: e.Code, Flags: e.Flags, Down: false})
	}
	for btn := range buttons {
		_ = in.b.Inject(platform.InputEvent{Type: platform.EvMouseButton, Button: btn, Down: false})
	}
}

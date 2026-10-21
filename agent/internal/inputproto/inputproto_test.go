package inputproto

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type vectorEvent struct {
	Type    string `json:"type"`
	Seq     uint16 `json:"seq"`
	X       uint16 `json:"x"`
	Y       uint16 `json:"y"`
	Monitor uint8  `json:"monitor"`
	DX      int16  `json:"dx"`
	DY      int16  `json:"dy"`
	Button  uint8  `json:"button"`
	Down    bool   `json:"down"`
	Usage   uint16 `json:"usage"`
	Mods    uint8  `json:"mods"`
	Text    string `json:"text"`
	Xfer    uint16 `json:"xfer"`
	Mime    uint8  `json:"mime"`
	Total   uint32 `json:"total"`
	Op      uint8  `json:"op"`
	Arg     uint8  `json:"arg"`
	Visible bool   `json:"visible"`
	ID      uint32 `json:"id"`
	W       uint16 `json:"w"`
	H       uint16 `json:"h"`
	HotX    uint16 `json:"hot_x"`
	HotY    uint16 `json:"hot_y"`
	PNGHex  string `json:"png_hex"`
}

type vectors struct {
	Input []struct {
		Name  string      `json:"name"`
		Event vectorEvent `json:"event"`
		Hex   string      `json:"hex"`
	} `json:"input"`
	InputInvalid []struct {
		Name string `json:"name"`
		Hex  string `json:"hex"`
	} `json:"input_invalid"`
	Cursor []struct {
		Name  string      `json:"name"`
		Event vectorEvent `json:"event"`
		Hex   string      `json:"hex"`
	} `json:"cursor"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "test-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func toEvent(v vectorEvent) Event {
	types := map[string]uint8{"move": TypeMouseMove, "move_seq": TypeMouseMoveSeq, "move_rel": TypeMouseMoveRel, "button": TypeMouseButton,
		"wheel": TypeMouseWheel, "key": TypeKey, "text": TypeText, "clip_begin": TypeClipboardBegin,
		"clip_data": TypeClipboardData, "control": TypeControl}
	ev := Event{Type: types[v.Type], Seq: v.Seq, X: v.X, Y: v.Y, Monitor: v.Monitor, DX: v.DX, DY: v.DY, Button: v.Button,
		Down: v.Down, Usage: v.Usage, Mods: v.Mods, Xfer: v.Xfer, Mime: v.Mime, Total: v.Total, Op: v.Op, Arg: v.Arg}
	if v.Type == "text" {
		ev.Text = v.Text
	}
	if v.Type == "clip_data" {
		ev.Data = []byte(v.Text)
	}
	return ev
}

func TestDecodeVectors(t *testing.T) {
	for _, tc := range loadVectors(t).Input {
		t.Run(tc.Name, func(t *testing.T) {
			raw, _ := hex.DecodeString(tc.Hex)
			got, err := Decode(raw)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			want := toEvent(tc.Event)
			if got.Type != want.Type || got.Seq != want.Seq || got.X != want.X || got.Y != want.Y || got.Monitor != want.Monitor ||
				got.DX != want.DX || got.DY != want.DY || got.Button != want.Button || got.Down != want.Down ||
				got.Usage != want.Usage || got.Mods != want.Mods || got.Text != want.Text || got.Xfer != want.Xfer ||
				got.Mime != want.Mime || got.Total != want.Total || got.Op != want.Op || got.Arg != want.Arg ||
				!bytes.Equal(got.Data, want.Data) {
				t.Fatalf("got %+v want %+v", got, want)
			}
		})
	}
}

func TestEncodeVectors(t *testing.T) {
	for _, tc := range loadVectors(t).Input {
		t.Run(tc.Name, func(t *testing.T) {
			if got := hex.EncodeToString(Encode(toEvent(tc.Event))); got != tc.Hex {
				t.Fatalf("got %s want %s", got, tc.Hex)
			}
		})
	}
}

func TestInvalidVectorsRejected(t *testing.T) {
	for _, tc := range loadVectors(t).InputInvalid {
		t.Run(tc.Name, func(t *testing.T) {
			raw, _ := hex.DecodeString(tc.Hex)
			if _, err := Decode(raw); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCursorVectors(t *testing.T) {
	for _, tc := range loadVectors(t).Cursor {
		t.Run(tc.Name, func(t *testing.T) {
			var got []byte
			e := tc.Event
			if e.Type == "pos" {
				got = EncodeCursorPos(e.X, e.Y, e.Visible)
			} else {
				png, _ := hex.DecodeString(e.PNGHex)
				got = EncodeCursorShape(e.ID, e.W, e.H, e.HotX, e.HotY, png)
			}
			if hex.EncodeToString(got) != tc.Hex {
				t.Fatalf("got %x want %s", got, tc.Hex)
			}
			typ, shape, x, y, vis, err := DecodeCursor(got)
			if err != nil {
				t.Fatal(err)
			}
			if typ == TypeCursorPos && (x != e.X || y != e.Y || vis != e.Visible) {
				t.Fatalf("pos round trip: %d %d %v", x, y, vis)
			}
			if typ == TypeCursorShape && (shape.ID != e.ID || shape.W != e.W || shape.H != e.H) {
				t.Fatalf("shape round trip: %+v", shape)
			}
		})
	}
}

func TestRangeChecks(t *testing.T) {
	cases := map[string][]byte{
		"key down byte 2":         {TypeKey, 4, 0, 2, 0},
		"quality tier 3":          {TypeControl, OpSetQuality, 3},
		"control op 0":            {TypeControl, 0, 0},
		"control op 5":            {TypeControl, 5, 0},
		"clipboard bad mime":      {TypeClipboardBegin, 1, 0, 9, 1, 0, 0, 0},
		"clipboard over 1MiB":     {TypeClipboardBegin, 1, 0, 1, 1, 0, 0x10, 0},
		"clipboard data no bytes": {TypeClipboardData, 1, 0},
		"text over 1024":          append([]byte{TypeText}, bytes.Repeat([]byte("a"), MaxText+1)...),
	}
	for name, raw := range cases {
		if _, err := Decode(raw); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestClipboardChunkRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789"), 5000) // 50 000 bytes → 4 data frames
	frames := ClipboardChunks(3, MimeText, payload)
	if len(frames) != 5 {
		t.Fatalf("frames = %d, want 5", len(frames))
	}
	var a ClipboardAssembler
	var out []byte
	for i, f := range frames {
		ev, err := Decode(f)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		done, mime, data, err := a.Feed(ev)
		if err != nil {
			t.Fatalf("feed %d: %v", i, err)
		}
		if done {
			if mime != MimeText || i != len(frames)-1 {
				t.Fatalf("done early at %d", i)
			}
			out = data
		}
	}
	if !bytes.Equal(out, payload) {
		t.Fatal("payload mismatch")
	}
}

func TestClipboardDataWithoutBeginRejected(t *testing.T) {
	var a ClipboardAssembler
	if _, _, _, err := a.Feed(Event{Type: TypeClipboardData, Xfer: 1, Data: []byte("x")}); err == nil {
		t.Fatal("expected error")
	}
}

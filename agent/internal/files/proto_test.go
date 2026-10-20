package files

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type vectors struct {
	FileData []struct {
		Name       string `json:"name"`
		Xfer       uint32 `json:"xfer"`
		PayloadHex string `json:"payload_hex"`
		Hex        string `json:"hex"`
	} `json:"file_data"`
	FileDataInvalid []struct {
		Name string `json:"name"`
		Hex  string `json:"hex"`
	} `json:"file_data_invalid"`
	FileMessages []struct {
		Name string          `json:"name"`
		JSON json.RawMessage `json:"json"`
	} `json:"file_messages"`
	FileSHA256 []struct {
		Name      string   `json:"name"`
		ChunksHex []string `json:"chunks_hex"`
		SHA256    string   `json:"sha256"`
	} `json:"file_sha256"`
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
	if len(v.FileData) == 0 || len(v.FileDataInvalid) == 0 || len(v.FileMessages) == 0 || len(v.FileSHA256) == 0 {
		t.Fatal("file vectors missing")
	}
	return v
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDataVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.FileData {
		t.Run(c.Name, func(t *testing.T) {
			payload, frame := mustHex(t, c.PayloadHex), mustHex(t, c.Hex)
			if got := EncodeData(c.Xfer, payload); !bytes.Equal(got, frame) {
				t.Fatalf("encode = %x, want %x", got, frame)
			}
			xfer, p, err := DecodeData(frame)
			if err != nil || xfer != c.Xfer || !bytes.Equal(p, payload) {
				t.Fatalf("decode = %d %x %v", xfer, p, err)
			}
		})
	}
	for _, c := range v.FileDataInvalid {
		t.Run(c.Name, func(t *testing.T) {
			if _, _, err := DecodeData(mustHex(t, c.Hex)); err == nil {
				t.Fatal("decoded an invalid frame")
			}
		})
	}
}

func TestDataMaxChunkAccepted(t *testing.T) {
	frame := EncodeData(1, make([]byte, MaxChunk))
	if _, p, err := DecodeData(frame); err != nil || len(p) != MaxChunk {
		t.Fatalf("max chunk: %d %v", len(p), err)
	}
}

func TestSHA256Vectors(t *testing.T) {
	for _, c := range loadVectors(t).FileSHA256 {
		h := sha256.New()
		for _, ch := range c.ChunksHex {
			h.Write(mustHex(t, ch))
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != c.SHA256 {
			t.Fatalf("%s: %s, want %s", c.Name, got, c.SHA256)
		}
	}
}

// agentFrames maps the agent → browser vectors to the types the service sends.
var agentFrames = map[string]func() any{
	"roots reply":        func() any { return &rootsMsg{} },
	"listing":            func() any { return &listingMsg{} },
	"begin":              func() any { return &beginMsg{} },
	"ready":              func() any { return &readyMsg{} },
	"ack":                func() any { return &ackMsg{} },
	"end download":       func() any { return &endMsg{} },
	"done":               func() any { return &doneMsg{} },
	"error for transfer": func() any { return &errorMsg{} },
	"error for request":  func() any { return &errorMsg{} },
}

// browserFrames are decoded by DecodeMsg.
var browserFrames = map[string]bool{
	"roots request": true, "list": true, "get": true, "put": true, "ack": true, "end upload": true, "cancel": true,
}

func generic(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMessageVectorsRoundTrip(t *testing.T) {
	for _, c := range loadVectors(t).FileMessages {
		t.Run(c.Name, func(t *testing.T) {
			want := generic(t, c.JSON)
			if mk, ok := agentFrames[c.Name]; ok {
				dst := mk()
				dec := json.NewDecoder(bytes.NewReader(c.JSON))
				dec.DisallowUnknownFields()
				if err := dec.Decode(dst); err != nil {
					t.Fatal(err)
				}
				out, _ := json.Marshal(dst)
				if got := generic(t, out); !reflect.DeepEqual(got, want) {
					t.Fatalf("round trip\n got %v\nwant %v", got, want)
				}
				return
			}
			if !browserFrames[c.Name] {
				t.Fatalf("vector %q not mapped to a frame type", c.Name)
			}
			m, err := DecodeMsg(c.JSON)
			if err != nil {
				t.Fatal(err)
			}
			out, _ := json.Marshal(m)
			got := generic(t, out)
			for k, v := range want {
				if !reflect.DeepEqual(got[k], v) {
					t.Fatalf("%s = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

func TestDecodeMsgRejects(t *testing.T) {
	sha := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	cases := map[string]string{
		"not json":         `{"t":`,
		"unknown t":        `{"t":"delete","xfer":1}`,
		"no t":             `{"xfer":1}`,
		"list without id":  `{"t":"list","path":"/"}`,
		"list null path":   `{"t":"list","id":1,"path":null}`,
		"get without path": `{"t":"get","xfer":1}`,
		"put without size": `{"t":"put","xfer":1,"name":"a"}`,
		"negative size":    `{"t":"put","xfer":1,"name":"a","size":-1}`,
		"negative offset":  `{"t":"list","id":1,"path":"/","offset":-5}`,
		"xfer over u32":    `{"t":"cancel","xfer":4294967296}`,
		"xfer negative":    `{"t":"cancel","xfer":-1}`,
		"xfer string":      `{"t":"cancel","xfer":"1"}`,
		"ack negative":     `{"t":"ack","xfer":1,"bytes":-1}`,
		"end short sha":    `{"t":"end","xfer":1,"sha256":"abcd"}`,
		"end upper sha":    `{"t":"end","xfer":1,"sha256":"` + "2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824" + `"}`,
		"end non-hex sha":  `{"t":"end","xfer":1,"sha256":"` + sha[:63] + `g"}`,
		"hidden not bool":  `{"t":"list","id":1,"path":"/","hidden":1}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if m, err := DecodeMsg([]byte(in)); err == nil {
				t.Fatalf("accepted %+v", m)
			}
		})
	}
}

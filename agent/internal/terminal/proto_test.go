package terminal

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type termVectors struct {
	Data []struct {
		Name       string `json:"name"`
		Type       byte   `json:"type"`
		Term       uint16 `json:"term"`
		PayloadHex string `json:"payload_hex"`
		Hex        string `json:"hex"`
	} `json:"term_data"`
	DataInvalid []struct {
		Name string `json:"name"`
		Hex  string `json:"hex"`
	} `json:"term_data_invalid"`
	Messages []struct {
		Name string          `json:"name"`
		JSON json.RawMessage `json:"json"`
	} `json:"term_messages"`
	MessagesInvalid []struct {
		Name string          `json:"name"`
		JSON json.RawMessage `json:"json"`
	} `json:"term_messages_invalid"`
}

func loadVectors(t *testing.T) termVectors {
	t.Helper()
	b, err := os.ReadFile("../../../docs/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v termVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Data) == 0 || len(v.Messages) == 0 {
		t.Fatal("no term vectors")
	}
	return v
}

func TestDataVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Data {
		payload, _ := hex.DecodeString(c.PayloadHex)
		want, _ := hex.DecodeString(c.Hex)
		if got := EncodeData(c.Type, c.Term, payload); !bytes.Equal(got, want) {
			t.Errorf("%s: encode %x, want %x", c.Name, got, want)
		}
		term, p, err := DecodeData(c.Type, want)
		if err != nil || term != c.Term || !bytes.Equal(p, payload) {
			t.Errorf("%s: decode term %d payload %x err %v", c.Name, term, p, err)
		}
	}
	for _, c := range v.DataInvalid {
		b, _ := hex.DecodeString(c.Hex)
		if _, _, err := DecodeData(TypeOutput, b); err == nil {
			t.Errorf("%s: decoded", c.Name)
		}
	}
}

func TestMessageVectors(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Messages {
		m, err := DecodeMsg(c.JSON)
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		var raw map[string]any
		_ = json.Unmarshal(c.JSON, &raw)
		if m.T != raw["t"] {
			t.Errorf("%s: t = %q", c.Name, m.T)
		}
	}
	for _, c := range v.MessagesInvalid {
		if _, err := DecodeMsg(c.JSON); err == nil {
			t.Errorf("%s: accepted", c.Name)
		}
	}
}

func TestDataTooLong(t *testing.T) {
	if _, _, err := DecodeData(TypeInput, EncodeData(TypeInput, 1, make([]byte, MaxChunk+1))); err == nil {
		t.Fatal("accepted a chunk over MaxChunk")
	}
}

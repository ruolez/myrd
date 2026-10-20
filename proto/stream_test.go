package proto

import (
	"encoding/json"
	"os"
	"testing"
)

type streamVectors struct {
	Request []struct {
		Name string `json:"name"`
		JSON string `json:"json"`
	} `json:"stream_request"`
	Invalid []struct {
		Name string `json:"name"`
		JSON string `json:"json"`
	} `json:"stream_request_invalid"`
}

func loadStreamVectors(t *testing.T) streamVectors {
	t.Helper()
	b, err := os.ReadFile("../docs/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v streamVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Request) == 0 || len(v.Invalid) == 0 {
		t.Fatal("stream vectors missing")
	}
	return v
}

func TestStreamRequestVectorsAccepted(t *testing.T) {
	for _, c := range loadStreamVectors(t).Request {
		r, err := DecodeStreamRequest([]byte(c.JSON))
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		// Re-encoding yields the exact wire form the browser produces.
		if out, _ := json.Marshal(r); string(out) != c.JSON {
			t.Errorf("%s: re-encoded\n got %s\nwant %s", c.Name, out, c.JSON)
		}
	}
}

func TestStreamRequestVectorsRejected(t *testing.T) {
	for _, c := range loadStreamVectors(t).Invalid {
		if _, err := DecodeStreamRequest([]byte(c.JSON)); err == nil {
			t.Errorf("%s: accepted", c.Name)
		}
	}
}

func TestStreamRequestRequiresStreamType(t *testing.T) {
	if _, err := DecodeStreamRequest([]byte(`{"t":"ping","preset":"auto"}`)); err == nil {
		t.Fatal("non-stream message accepted")
	}
}

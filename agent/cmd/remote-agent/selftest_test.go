package main

import "testing"

func TestVP9Frame(t *testing.T) {
	cases := []struct {
		first byte
		want  string
	}{
		{0xa2, "VP9 profile 1, keyframe=true"},  // libvpx 4:4:4 keyframe (agent/internal/session/testdata)
		{0xa6, "VP9 profile 1, keyframe=false"}, // and the delta frame after it
		{0x82, "VP9 profile 0, keyframe=true"},
		{0x00, "not a VP9 frame"},
	}
	for _, c := range cases {
		if got := vp9Frame([]byte{c.first}); got != c.want {
			t.Errorf("vp9Frame(%#x) = %q, want %q", c.first, got, c.want)
		}
	}
	if got := vp9Frame(nil); got != "not a VP9 frame" {
		t.Errorf("vp9Frame(nil) = %q", got)
	}
}

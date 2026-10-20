package config

import "testing"

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if st, err := Load(dir); err != nil || st.Enrolled() {
		t.Fatalf("fresh dir: %+v %v", st, err)
	}
	want := State{ServerURL: "http://x", DeviceID: "d1", Name: "box"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || got != want {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := Remove(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(dir); got.Enrolled() {
		t.Fatal("still enrolled after Remove")
	}
}

func TestFromEnvParsesList(t *testing.T) {
	t.Setenv("RC_NAT1TO1_IPS", " 127.0.0.1, 10.0.0.2 ,")
	t.Setenv("RC_UDP_PORT", "5561")
	s := FromEnv()
	if len(s.NAT1To1IPs) != 2 || s.NAT1To1IPs[1] != "10.0.0.2" || s.UDPPort != 5561 {
		t.Fatalf("got %+v", s)
	}
}

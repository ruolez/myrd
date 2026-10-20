package winsvc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Frame{Kind: KindSignal, Type: proto.TypeSessionOffer, Data: json.RawMessage(`{"session_id":"s1"}`)}
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadFrame(&buf)
	if err != nil || !reflect.DeepEqual(out, in) {
		t.Fatalf("got %+v, %v; want %+v", out, err, in)
	}
}

func TestReadFrameRejectsOversizedLength(t *testing.T) {
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], MaxFrameBytes+1)
	if _, err := ReadFrame(bytes.NewReader(hdr[:])); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("got %v, want ErrFrameTooLarge", err)
	}
}

func TestReadFrameTruncatedBody(t *testing.T) {
	var hdr [4]byte
	binary.LittleEndian.PutUint32(hdr[:], 10)
	if _, err := ReadFrame(bytes.NewReader(append(hdr[:], 'x'))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want io.ErrUnexpectedEOF", err)
	}
}

type sent struct {
	Type string
	JSON string
}

type recorder struct {
	mu   sync.Mutex
	msgs []sent
}

func (r *recorder) Send(typ string, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.msgs = append(r.msgs, sent{typ, string(b)})
	r.mu.Unlock()
	return nil
}

func (r *recorder) all() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sent(nil), r.msgs...)
}

func TestProxyRefusesOfferWithoutHelper(t *testing.T) {
	rec := &recorder{}
	p := &Proxy{Send: rec.Send}
	p.HandleSignal(proto.TypeSessionOffer, []byte(`{"v":1,"type":"session.offer","session_id":"s1","sdp":"x"}`))
	want := []sent{{proto.TypeSessionEnd, `{"v":0,"type":"","session_id":"s1","reason":"error"}`}}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// fakeTarget records what the helper side delivers to the session manager.
type fakeTarget struct {
	mu      sync.Mutex
	signals []sent
	ended   []string
	revoked [][]string
	revoke  []string
}

func (f *fakeTarget) HandleSignal(t string, raw []byte) {
	f.mu.Lock()
	f.signals = append(f.signals, sent{t, string(raw)})
	f.mu.Unlock()
}
func (f *fakeTarget) ActiveSessions() []string { return nil }
func (f *fakeTarget) EndAll(reason string) {
	f.mu.Lock()
	f.ended = append(f.ended, reason)
	f.mu.Unlock()
}
func (f *fakeTarget) SetRevoked(s []string) {
	f.mu.Lock()
	f.revoked = append(f.revoked, s)
	f.mu.Unlock()
}
func (f *fakeTarget) Revoke(s string) {
	f.mu.Lock()
	f.revoke = append(f.revoke, s)
	f.mu.Unlock()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// pair wires a Proxy and a helper Serve loop together over two io.Pipes,
// the same topology as the two named pipes on Windows.
func pair(t *testing.T, p *Proxy, target *fakeTarget, ev HelperEvents) (*Link, func()) {
	s2hR, s2hW := io.Pipe()
	h2sR, h2sW := io.Pipe()
	done := make(chan struct{}, 2)
	go func() { p.Attach(h2sR, s2hW); done <- struct{}{} }()
	go func() { Serve(s2hR, target, ev); done <- struct{}{} }()
	return NewLink(h2sW), func() {
		h2sW.Close()
		s2hW.Close()
		<-done
		<-done
	}
}

func TestProxyAndHelperEndToEnd(t *testing.T) {
	rec := &recorder{}
	p := &Proxy{Send: rec.Send}
	p.SetRevoked([]string{"k1"}) // before attach: must be replayed
	target := &fakeTarget{}
	var gotDevice proto.DeviceInfo
	var mu sync.Mutex
	link, stop := pair(t, p, target, HelperEvents{OnDevice: func(d proto.DeviceInfo) {
		mu.Lock()
		gotDevice = d
		mu.Unlock()
	}})

	offer := `{"v":1,"type":"session.offer","session_id":"s1"}`
	waitFor(t, func() bool { target.mu.Lock(); defer target.mu.Unlock(); return len(target.revoked) == 1 })
	p.HandleSignal(proto.TypeSessionOffer, []byte(offer))
	p.Revoke("k2")
	p.SetDevice(proto.DeviceInfo{ID: "d1", AutoAccept: true})
	p.EndAll(proto.EndAgentKill)

	if err := link.Send(proto.TypeSessionAnswer, proto.SessionAnswer{SessionID: "s1", SDP: "a", FPSig: "sig"}); err != nil {
		t.Fatal(err)
	}
	if err := link.Active([]string{"s1"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(p.ActiveSessions()) == 1 && len(rec.all()) == 1 })
	waitFor(t, func() bool { target.mu.Lock(); defer target.mu.Unlock(); return len(target.ended) == 1 })

	target.mu.Lock()
	if !reflect.DeepEqual(target.revoked, [][]string{{"k1"}}) || !reflect.DeepEqual(target.revoke, []string{"k2"}) ||
		!reflect.DeepEqual(target.signals, []sent{{proto.TypeSessionOffer, offer}}) ||
		!reflect.DeepEqual(target.ended, []string{proto.EndAgentKill}) {
		t.Fatalf("helper saw revoked=%v revoke=%v signals=%v ended=%v", target.revoked, target.revoke, target.signals, target.ended)
	}
	target.mu.Unlock()
	mu.Lock()
	if gotDevice != (proto.DeviceInfo{ID: "d1", AutoAccept: true}) {
		t.Fatalf("device = %+v", gotDevice)
	}
	mu.Unlock()
	wantAnswer := sent{proto.TypeSessionAnswer, `{"v":0,"type":"","session_id":"s1","sdp":"a","fp_sig":"sig"}`}
	if got := rec.all(); !reflect.DeepEqual(got, []sent{wantAnswer}) {
		t.Fatalf("server got %+v", got)
	}

	// Helper dies with s1 live → the service ends it towards the server.
	stop()
	wantEnd := sent{proto.TypeSessionEnd, `{"v":0,"type":"","session_id":"s1","reason":"error"}`}
	if got := rec.all(); !reflect.DeepEqual(got, []sent{wantAnswer, wantEnd}) {
		t.Fatalf("after helper exit server got %+v", got)
	}
	if got := p.ActiveSessions(); len(got) != 0 {
		t.Fatalf("active after detach = %v", got)
	}
}

func TestServeStopsOnShutdown(t *testing.T) {
	var buf bytes.Buffer
	WriteFrame(&buf, Frame{Kind: KindShutdown})
	WriteFrame(&buf, Frame{Kind: KindEndAll, Data: json.RawMessage(`"x"`)})
	target := &fakeTarget{}
	if err := Serve(&buf, target, HelperEvents{}); err != nil {
		t.Fatalf("Serve = %v, want nil on shutdown", err)
	}
	if len(target.ended) != 0 {
		t.Fatalf("frames after shutdown were delivered: %v", target.ended)
	}
}

// A helper that loaded the device key before a re-enrollment signs answers
// with the old identity, which browsers reject as bad_signature; the
// service must make it exit so a fresh helper loads the new key.
func TestProxyRestartsHelperWithStaleDeviceKey(t *testing.T) {
	const current, previous = "key-after-enroll", "key-before-enroll"
	cases := []struct {
		name        string
		helperKey   string
		keyFirst    bool
		wantRestart bool
	}{
		{"caps then stale key", previous, false, true},
		{"key then stale caps", previous, true, true},
		{"matching key", current, true, false},
		{"helper without key report", "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Proxy{}
			if tc.keyFirst {
				p.SetDeviceKey(current)
			}
			s2hR, s2hW := io.Pipe()
			h2sR, h2sW := io.Pipe()
			defer h2sW.Close()
			defer s2hW.Close()
			go p.Attach(h2sR, s2hW)
			statuses := make(chan ServiceStatus, 1)
			served := make(chan error, 1)
			go func() {
				err := Serve(s2hR, &fakeTarget{}, HelperEvents{OnStatus: func(s ServiceStatus) { statuses <- s }})
				s2hR.Close() // the helper process is gone: later writes fail instead of blocking
				served <- err
			}()
			caps := HelperCaps{SessionID: 1, DeviceKey: tc.helperKey}
			if err := NewLink(h2sW).Caps(caps); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { c, ok := p.Caps(); return ok && reflect.DeepEqual(c, caps) })
			if !tc.keyFirst {
				p.SetDeviceKey(current)
			}
			p.SetStatus(ServiceStatus{Status: "probe"})
			select {
			case err := <-served:
				if !tc.wantRestart || err != nil {
					t.Fatalf("helper loop returned %v, want restart %v", err, tc.wantRestart)
				}
			case <-statuses:
				if tc.wantRestart {
					t.Fatal("helper with a stale key kept running")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("helper neither stopped nor received status")
			}
		})
	}
}

func TestStatusKeepsTheUpdateLine(t *testing.T) {
	p := &Proxy{}
	p.SetStatus(ServiceStatus{Enrolled: true, Status: "Connecting…"})
	p.SetUpdateLine("Updating to 0.3.1…")
	p.SetStatus(ServiceStatus{Enrolled: true, Online: true, Status: "Online"})

	s2hR, s2hW := io.Pipe()
	h2sR, h2sW := io.Pipe()
	defer h2sW.Close()
	defer s2hW.Close()
	go p.Attach(h2sR, s2hW)
	statuses := make(chan ServiceStatus, 1)
	go Serve(s2hR, &fakeTarget{}, HelperEvents{OnStatus: func(s ServiceStatus) { statuses <- s }})
	select {
	case got := <-statuses:
		want := ServiceStatus{Enrolled: true, Online: true, Status: "Online", Update: "Updating to 0.3.1…"}
		if got != want {
			t.Fatalf("replayed status %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no status replayed to the helper")
	}
}

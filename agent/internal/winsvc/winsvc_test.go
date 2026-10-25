package winsvc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/vdd"
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

// While msiexec replaces the exe the helper must be gone and stay gone:
// a respawned helper locks the file and turns the update into a rollback.
func TestHoldHelperStopsItUntilReleased(t *testing.T) {
	p := &Proxy{}
	s2hR, s2hW := io.Pipe()
	h2sR, h2sW := io.Pipe()
	defer h2sW.Close()
	go p.Attach(h2sR, s2hW)
	served := make(chan error, 1)
	go func() {
		err := Serve(s2hR, &fakeTarget{}, HelperEvents{})
		s2hR.Close()
		h2sW.Close() // the helper process exits, closing its pipe
		served <- err
	}()
	waitFor(t, func() bool { return p.attached() })
	release := p.HoldHelper(2 * time.Second)
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("helper loop returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("helper kept running")
	}
	if !p.Held() || p.attached() {
		t.Fatalf("held = %v attached = %v, want true false", p.Held(), p.attached())
	}
	release()
	if p.Held() {
		t.Fatal("still held after release")
	}
}

func TestSoftwareSASGenerationAllowsServicesKeepingEaseOfAccess(t *testing.T) {
	cases := []struct {
		cur    uint64
		exists bool
		want   uint32
		change bool
	}{
		{0, false, 1, true}, // unset: services only
		{0, true, 1, true},  // disabled
		{2, true, 3, true},  // Ease of Access apps only: add services
		{1, true, 1, false},
		{3, true, 3, false},
	}
	for _, c := range cases {
		if got, change := softwareSASGeneration(c.cur, c.exists); got != c.want || change != c.change {
			t.Errorf("softwareSASGeneration(%d, %v) = %d, %v; want %d, %v", c.cur, c.exists, got, change, c.want, c.change)
		}
	}
}

func TestHelperCtrlAltDeleteReachesTheService(t *testing.T) {
	sas := make(chan struct{}, 1)
	p := &Proxy{Send: (&recorder{}).Send, OnSAS: func() { sas <- struct{}{} }}
	link, stop := pair(t, p, &fakeTarget{}, HelperEvents{})
	defer stop()
	if err := link.SAS(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sas:
	case <-time.After(2 * time.Second):
		t.Fatal("the service never got the Ctrl+Alt+Delete request")
	}
}

func TestProxyForwardsDeviceInfoOnlyWhenTheServerTakesIt(t *testing.T) {
	rec := &recorder{}
	p := &Proxy{Send: rec.Send}
	link, stop := pair(t, p, &fakeTarget{}, HelperEvents{})
	defer stop()
	report := proto.HostInfoReport{Info: proto.HostInfo{OSName: "Windows 11 Pro"}}
	countInfo := func() int {
		n := 0
		for _, m := range rec.all() {
			if m.Type == proto.TypeDeviceInfo {
				n++
			}
		}
		return n
	}

	// Before hello_ack, and from a server without the feature: dropped.
	link.Send(proto.TypeDeviceInfo, report)
	p.SetServerFeatures(nil)
	link.Send(proto.TypeDeviceInfo, report)
	link.Send(proto.TypePinsSync, proto.PinsSync{}) // a marker that does get through
	waitFor(t, func() bool { return len(rec.all()) == 1 })
	if countInfo() != 0 {
		t.Fatalf("device.info forwarded to a server without the feature: %+v", rec.all())
	}

	p.SetServerFeatures([]string{proto.FeatureDeviceInfo})
	link.Send(proto.TypeDeviceInfo, report)
	waitFor(t, func() bool { return countInfo() == 1 })
}

// The service's virtual display job shows in the helper's reports: at once
// (the last report sent again), on every report while it runs, and a
// finished job's result only until the helper's next report.
func TestProxyLaysTheVirtualDisplayJobOverDeviceInfo(t *testing.T) {
	rec := &recorder{}
	p := &Proxy{Send: rec.Send}
	p.SetServerFeatures([]string{proto.FeatureDeviceInfo})
	link, stop := pair(t, p, &fakeTarget{}, HelperEvents{})
	defer stop()
	report := proto.HostInfoReport{Info: proto.HostInfo{OSName: "Windows Server 2019",
		VirtualDisplay: &proto.VirtualDisplayInfo{Supported: true, Headless: true, Driver: proto.VDriverAbsent}}}
	drivers := func() []string {
		var out []string
		for _, m := range rec.all() {
			var r proto.HostInfoReport
			if m.Type == proto.TypeDeviceInfo && json.Unmarshal([]byte(m.JSON), &r) == nil && r.Info.VirtualDisplay != nil {
				out = append(out, r.Info.VirtualDisplay.Driver)
			}
		}
		return out
	}
	link.Send(proto.TypeDeviceInfo, report)
	waitFor(t, func() bool { return len(drivers()) == 1 })
	p.SetVirtualDisplayJob(vdd.Job{Driver: proto.VDriverInstalling}, false)
	link.Send(proto.TypeDeviceInfo, report)
	waitFor(t, func() bool { return len(drivers()) == 3 })
	p.SetVirtualDisplayJob(vdd.Job{Driver: proto.VDriverInstalled}, true)
	link.Send(proto.TypeDeviceInfo, report)
	waitFor(t, func() bool { return len(drivers()) == 5 })
	want := []string{proto.VDriverAbsent, proto.VDriverInstalling, proto.VDriverInstalling, proto.VDriverInstalled,
		proto.VDriverAbsent}
	if got := drivers(); !slices.Equal(got, want) {
		t.Fatalf("drivers reported %v, want %v", got, want)
	}
}

// The service adds its own memory to the helper's report of the agent's.
func TestProxyAddsServiceMemoryToDeviceInfo(t *testing.T) {
	const helper, service = 60 << 20, 18 << 20
	rec := &recorder{}
	p := &Proxy{Send: rec.Send, ServiceMemory: func() uint64 { return service }}
	p.SetServerFeatures([]string{proto.FeatureDeviceInfo})
	link, stop := pair(t, p, &fakeTarget{}, HelperEvents{})
	defer stop()
	link.Send(proto.TypeDeviceInfo, proto.HostInfoReport{Info: proto.HostInfo{Agent: &proto.AgentMemory{Main: helper}}})
	var got *proto.AgentMemory
	waitFor(t, func() bool {
		for _, m := range rec.all() {
			var r proto.HostInfoReport
			if m.Type == proto.TypeDeviceInfo && json.Unmarshal([]byte(m.JSON), &r) == nil {
				got = r.Info.Agent
				return true
			}
		}
		return false
	})
	if want := (proto.AgentMemory{Main: helper, Service: service}); got == nil || *got != want {
		t.Fatalf("agent memory %+v, want %+v", got, want)
	}
}

type previewSink struct{ got [][]byte }

func (p *previewSink) Handle(raw []byte) { p.got = append(p.got, raw) }

func TestPreviewRouterSendsPreviewRequestsToPreviews(t *testing.T) {
	target, sink := &fakeTarget{}, &previewSink{}
	r := PreviewRouter{HelperTarget: target, Previews: sink}
	r.HandleSignal(proto.TypePreviewRequest, []byte(`{"req":"p1"}`))
	r.HandleSignal(proto.TypeSessionOffer, []byte(`{"session_id":"s1"}`))
	if len(sink.got) != 1 || string(sink.got[0]) != `{"req":"p1"}` {
		t.Fatalf("previews got %q", sink.got)
	}
	if len(target.signals) != 1 || target.signals[0].Type != proto.TypeSessionOffer {
		t.Fatalf("session manager got %+v", target.signals)
	}
}

func TestProxyAnswersPreviewRequestsWithoutHelper(t *testing.T) {
	rec := &recorder{}
	p := &Proxy{Send: rec.Send}
	p.HandleSignal(proto.TypePreviewRequest, []byte(`{"v":1,"type":"preview.request","req":"r9"}`))
	want := []sent{{proto.TypePreviewUnavailable, `{"v":0,"type":"","req":"r9","reason":"no_console"}`}}
	if got := rec.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

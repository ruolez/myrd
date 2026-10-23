package session

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

// startTURN runs a TURN server on loopback that accepts one user.
func startTURN(t *testing.T, user, pass string) string {
	t.Helper()
	const realm = "remotectl.test"
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	key := turn.GenerateAuthKey(user, realm, pass)
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, key, ra.Username == user
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: conn,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return "turn:127.0.0.1:" + strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port) + "?transport=udp"
}

func (h *harness) relayCandidates() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.cands {
		if strings.Contains(c, "typ relay") {
			n++
		}
	}
	return n
}

// TURN credentials expire (TURN_TTL), so every offer brings fresh ones: an
// ICE restart late in a session must gather relay candidates with the new
// credentials, not the ones the peer connection was created with.
func TestICERestartGathersRelayWithTheOffersCredentials(t *testing.T) {
	url := startTURN(t, "fresh", "secret")
	h := newHarness(t, platform.ConsentOnce)
	h.agentICE = []proto.ICEServer{{URLs: []string{url}, Username: "expired", Credential: "secret"}}
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "connected", func() bool { _, st, _ := h.snapshot(); return len(st) > 0 })
	time.Sleep(2 * time.Second) // the refused allocation has had its chance
	if n := h.relayCandidates(); n != 0 {
		t.Fatalf("%d relay candidates with refused credentials", n)
	}

	off, err := h.browser.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.browser.SetLocalDescription(off); err != nil {
		t.Fatal(err)
	}
	fp, err := proto.SDPFingerprint(h.browser.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := proto.Encode(proto.TypeSessionOffer, proto.SessionOffer{SessionID: sessionID, SDP: h.browser.LocalDescription().SDP,
		FPSig: h.signFP(fp), ICEServers: []proto.ICEServer{{URLs: []string{url}, Username: "fresh", Credential: "secret"}}})
	h.mgr.HandleSignal(proto.TypeSessionOffer, b)
	if err := <-h.answerOK; err != nil {
		t.Fatalf("restart answer: %v", err)
	}
	waitFor(t, "relay candidate after the restart", func() bool { return h.relayCandidates() > 0 })
	if _, _, ends := h.snapshot(); len(ends) != 0 {
		t.Fatalf("session ended: %+v", ends)
	}
}

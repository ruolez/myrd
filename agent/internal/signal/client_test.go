package signal

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

func TestBackoffBounds(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		ceil := backoffMin << min(attempt, 6)
		if ceil > backoffMax {
			ceil = backoffMax
		}
		for i := 0; i < 200; i++ {
			d := Backoff(attempt)
			if d < 250*time.Millisecond || d > max(ceil, 250*time.Millisecond) {
				t.Fatalf("attempt %d: %v outside [250ms, %v]", attempt, d, ceil)
			}
		}
	}
}

type recHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recHandler) HandleSignal(typ string, _ []byte) {
	r.mu.Lock()
	r.msgs = append(r.msgs, typ)
	r.mu.Unlock()
}
func (r *recHandler) ActiveSessions() []string { return nil }
func (r *recHandler) EndAll(string)            {}
func (r *recHandler) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

// fakeServer implements challenge/auth and a WS that answers hello and then
// pushes one offer, or closes with closeCode when set.
func fakeServer(t *testing.T, pub ed25519.PublicKey, closeCode websocket.StatusCode) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/agent/challenge", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"nonce": "n1"})
	})
	mux.HandleFunc("POST /v1/agent/auth", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ DeviceID, Nonce, Signature string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		sig, _ := base64.StdEncoding.DecodeString(in.Signature)
		if !ed25519.Verify(pub, proto.AuthChallengeMessage("dev1", "n1"), sig) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"bad sig"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "jwt1", "expires_at": time.Now().Add(10 * time.Minute).Format(time.RFC3339)})
	})
	mux.HandleFunc("GET /v1/agent/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jwt1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ctx := r.Context()
		_, data, err := c.Read(ctx)
		var env proto.Envelope
		if err != nil || json.Unmarshal(data, &env) != nil || env.Type != proto.TypeHello {
			c.Close(websocket.StatusPolicyViolation, "expected hello")
			return
		}
		if closeCode != 0 {
			c.Close(closeCode, "disabled")
			return
		}
		ack, _ := proto.Encode(proto.TypeHelloAck, proto.HelloAck{Device: proto.DeviceInfo{ID: "dev1", Name: "box", AutoAccept: true}, RevokedClientKeys: []string{"k"}})
		_ = c.Write(ctx, websocket.MessageText, ack)
		offer, _ := proto.Encode(proto.TypeSessionOffer, proto.SessionOffer{SessionID: "s1", SDP: "v=0"})
		_ = c.Write(ctx, websocket.MessageText, offer)
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestHelloAckAndRouting(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := fakeServer(t, pub, 0)
	h := &recHandler{}
	acks := make(chan proto.HelloAck, 1)
	c := New(Options{API: api.New(srv.URL), DeviceID: "dev1", Key: priv, Handler: h,
		Hello:      func() proto.Hello { return proto.Hello{AgentVersion: "t"} },
		OnHelloAck: func(m proto.HelloAck) { acks <- m }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	select {
	case m := <-acks:
		if !m.Device.AutoAccept || m.RevokedClientKeys[0] != "k" {
			t.Fatalf("ack = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no hello_ack")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.got(); len(got) != 1 || got[0] != proto.TypeSessionOffer {
		t.Fatalf("routed = %v", got)
	}
	if !c.Status().Online {
		t.Fatal("status not online")
	}
	if err := c.Send(proto.TypePinsSync, proto.PinsSync{}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestDisabledCloseCodeStopsRun(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	srv := fakeServer(t, pub, CloseDisabled)
	c := New(Options{API: api.New(srv.URL), DeviceID: "dev1", Key: priv, Handler: &recHandler{},
		Hello: func() proto.Hello { return proto.Hello{} }})
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	select {
	case err := <-done:
		if websocket.CloseStatus(err) != CloseDisabled || c.Status().Fatal != "device_disabled" {
			t.Fatalf("err=%v status=%+v", err, c.Status())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept retrying after 4003")
	}
}

func TestFatalReasonFromAPIError(t *testing.T) {
	if fatalReason(&api.Error{Status: 403, Code: "device_removed"}) != "device_removed" ||
		fatalReason(&api.Error{Status: 403, Code: "device_disabled"}) != "device_disabled" ||
		fatalReason(errors.New("dial tcp: refused")) != "" {
		t.Fatal("fatal reason mapping wrong")
	}
}

// Package signal keeps the agent's control-plane WebSocket alive: device
// auth, hello/hello_ack, token renewal and routing of session messages to a
// session.Handler. The socket is never on the media path, so a drop here
// does not end a connected session.
package signal

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/agent/internal/session"
	"github.com/ruolez/remotectl/proto"
)

// ErrNotConnected is returned by Send while the socket is down.
var ErrNotConnected = errors.New("signaling socket not connected")

// Close codes from docs/PROTOCOL.md §3.
const (
	CloseUnauthorized = 4001
	CloseDisabled     = 4003
	CloseReplaced     = 4008
)

const (
	backoffMin     = time.Second
	backoffMax     = 30 * time.Second
	backoffReset   = 60 * time.Second
	pingInterval   = 25 * time.Second
	renewInterval  = 7 * time.Minute
	helloAckWithin = 15 * time.Second
)

// Status is what the tray / device console shows.
type Status struct {
	Online    bool
	LastError string
	Fatal     string // "device_removed" | "device_disabled" when the server refuses us for good
}

type Options struct {
	API      *api.Client
	DeviceID string
	Key      ed25519.PrivateKey
	Handler  session.Handler
	Log      *slog.Logger

	Hello          func() proto.Hello
	OnHelloAck     func(proto.HelloAck)
	OnDeviceUpdate func(proto.DeviceUpdate)
	OnKeyRevoked   func(proto.KeyRevoked)
	OnUpdateCheck  func(proto.UpdateCheck)
	// OnVirtualDisplay gets virtual_display (install / remove the driver).
	OnVirtualDisplay func(proto.VirtualDisplayRequest)
	OnStatus         func(Status)
	// OnPreviewRequest gets preview.request (raw JSON); it must not block.
	OnPreviewRequest func(raw []byte)
}

type Client struct {
	o       Options
	limiter *rate.Limiter

	mu       sync.Mutex
	conn     *websocket.Conn
	token    string
	tokenExp time.Time
	status   Status
}

func New(o Options) *Client {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	// Server allows 20 msg/s per connection; stay under it.
	return &Client{o: o, limiter: rate.NewLimiter(18, 18)}
}

// Run connects and reconnects until ctx ends or the server refuses the device
// for good, in which case it returns an *api.Error / close-code error.
func (c *Client) Run(ctx context.Context) error {
	attempt := 0
	for ctx.Err() == nil {
		start := time.Now()
		err := c.connectOnce(ctx)
		c.setStatus(func(s *Status) { s.Online = false })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if fatal := fatalReason(err); fatal != "" {
			c.setStatus(func(s *Status) { s.Fatal = fatal; s.LastError = err.Error() })
			c.o.Log.Error("server refused device; stopping", "reason", fatal, "err", err)
			return err
		}
		if err != nil {
			c.setStatus(func(s *Status) { s.LastError = err.Error() })
			c.o.Log.Warn("signaling disconnected", "err", err)
		}
		if time.Since(start) > backoffReset {
			attempt = 0
		}
		d := Backoff(attempt)
		attempt++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return ctx.Err()
}

// Backoff is full jitter: uniform in [0, min(max, min*2^attempt)], floored
// at 250 ms so a flapping server is not hammered.
func Backoff(attempt int) time.Duration {
	ceil := backoffMin << min(attempt, 6)
	if ceil > backoffMax {
		ceil = backoffMax
	}
	d := time.Duration(rand.Int64N(int64(ceil) + 1))
	return max(d, 250*time.Millisecond)
}

func fatalReason(err error) string {
	if err == nil {
		return ""
	}
	if api.IsCode(err, "device_removed") || api.IsCode(err, "not_found") {
		return "device_removed"
	}
	if api.IsCode(err, "device_disabled") {
		return "device_disabled"
	}
	if websocket.CloseStatus(err) == CloseDisabled {
		return "device_disabled"
	}
	return ""
}

func (c *Client) bearer(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	tok, exp := c.token, c.tokenExp
	c.mu.Unlock()
	if !force && tok != "" && time.Until(exp) > 30*time.Second {
		return tok, nil
	}
	tok, exp, err := c.o.API.Authenticate(ctx, c.o.DeviceID, c.o.Key)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.token, c.tokenExp = tok, exp
	c.mu.Unlock()
	return tok, nil
}

// Token returns a valid JWT (for update checks and self-delete).
func (c *Client) Token(ctx context.Context) (string, error) { return c.bearer(ctx, false) }

func (c *Client) connectOnce(ctx context.Context) error {
	tok, err := c.bearer(ctx, false)
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	h := http.Header{"Authorization": {"Bearer " + tok}}
	conn, resp, err := websocket.Dial(dctx, c.o.API.WebSocketURL(), &websocket.DialOptions{HTTPHeader: h})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			c.mu.Lock()
			c.token = ""
			c.mu.Unlock()
		}
		if resp != nil && resp.StatusCode == http.StatusForbidden {
			// The server answers 403 for disabled/removed devices on upgrade too;
			// re-authenticate to learn which.
			if _, aerr := c.bearer(ctx, true); aerr != nil {
				return aerr
			}
		}
		return err
	}
	conn.SetReadLimit(proto.MaxMessageBytes)
	defer conn.CloseNow()

	cctx, ccancel := context.WithCancel(ctx)
	defer ccancel()

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
	}()

	hello := c.o.Hello()
	if err := c.write(cctx, conn, proto.TypeHello, hello); err != nil {
		return err
	}

	go c.keepalive(cctx, conn)
	acked := make(chan struct{})
	go func() {
		select {
		case <-acked:
		case <-cctx.Done():
		case <-time.After(helloAckWithin):
			c.o.Log.Warn("no hello_ack; reconnecting")
			conn.Close(websocket.StatusPolicyViolation, "no hello_ack")
		}
	}()

	var ackOnce sync.Once
	for {
		typ, data, err := conn.Read(cctx)
		if err != nil {
			if websocket.CloseStatus(err) == CloseUnauthorized {
				c.mu.Lock()
				c.token = ""
				c.mu.Unlock()
			}
			return err
		}
		if typ != websocket.MessageText {
			continue
		}
		var env proto.Envelope
		if json.Unmarshal(data, &env) != nil || env.V != proto.Version {
			c.o.Log.Warn("dropping malformed signaling message")
			continue
		}
		switch env.Type {
		case proto.TypeHelloAck:
			var m proto.HelloAck
			if json.Unmarshal(data, &m) == nil {
				ackOnce.Do(func() { close(acked) })
				c.setStatus(func(s *Status) { s.Online = true; s.LastError = "" })
				if c.o.OnHelloAck != nil {
					c.o.OnHelloAck(m)
				}
			}
		case proto.TypeToken:
			var m proto.Token
			if json.Unmarshal(data, &m) == nil && m.Token != "" {
				exp, _ := time.Parse(time.RFC3339, m.ExpiresAt)
				c.mu.Lock()
				c.token, c.tokenExp = m.Token, exp
				c.mu.Unlock()
			}
		case proto.TypeDeviceUpdate:
			var m proto.DeviceUpdate
			if json.Unmarshal(data, &m) == nil && c.o.OnDeviceUpdate != nil {
				c.o.OnDeviceUpdate(m)
			}
		case proto.TypeKeyRevoked:
			var m proto.KeyRevoked
			if json.Unmarshal(data, &m) == nil && c.o.OnKeyRevoked != nil {
				c.o.OnKeyRevoked(m)
			}
		case proto.TypeUpdateCheck:
			var m proto.UpdateCheck
			if json.Unmarshal(data, &m) == nil && c.o.OnUpdateCheck != nil {
				c.o.OnUpdateCheck(m)
			}
		case proto.TypeVirtualDisplay:
			var m proto.VirtualDisplayRequest
			if json.Unmarshal(data, &m) == nil && c.o.OnVirtualDisplay != nil {
				c.o.OnVirtualDisplay(m)
			}
		case proto.TypePreviewRequest:
			if c.o.OnPreviewRequest != nil {
				c.o.OnPreviewRequest(data)
			}
		case proto.TypeSessionOffer, proto.TypeICECandidate, proto.TypeSessionEnd:
			if c.o.Handler != nil {
				c.o.Handler.HandleSignal(env.Type, data)
			}
		case proto.TypePong:
		case proto.TypePing:
			_ = c.write(cctx, conn, proto.TypePong, proto.Ping{TS: time.Now().UnixMilli()})
		case proto.TypeError:
			var m proto.Error
			_ = json.Unmarshal(data, &m)
			c.o.Log.Warn("server error", "code", m.Code, "message", m.Message, "session", m.SessionID)
		default:
			c.o.Log.Debug("ignoring signaling message", "type", env.Type)
		}
	}
}

func (c *Client) keepalive(ctx context.Context, conn *websocket.Conn) {
	ping := time.NewTicker(pingInterval)
	renew := time.NewTicker(renewInterval)
	defer ping.Stop()
	defer renew.Stop()
	misses := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				misses++
				if misses >= 2 {
					conn.Close(websocket.StatusGoingAway, "keepalive timeout")
					return
				}
			} else {
				misses = 0
			}
		case <-renew.C:
			_ = c.write(ctx, conn, proto.TypeTokenRenew, struct{}{})
		}
	}
}

func (c *Client) write(ctx context.Context, conn *websocket.Conn, typ string, msg any) error {
	b, err := proto.Encode(typ, msg)
	if err != nil {
		return err
	}
	if len(b) > proto.MaxMessageBytes {
		return errors.New("signaling message too large")
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// Send implements session.Outbound.
func (c *Client) Send(typ string, msg any) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	return c.write(context.Background(), conn, typ, msg)
}

func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Client) setStatus(f func(*Status)) {
	c.mu.Lock()
	f(&c.status)
	s := c.status
	c.mu.Unlock()
	if c.o.OnStatus != nil {
		c.o.OnStatus(s)
	}
}

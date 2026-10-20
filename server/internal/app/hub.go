package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/security"
	"github.com/ruolez/remotectl/server/internal/store"
)

const (
	pingInterval       = 25 * time.Second
	pendingTimeout     = 90 * time.Second
	bootClaimWindow    = 2 * time.Minute
	maxBrowsersPerUser = 5
	sendQueue          = 128

	closeUnauthorized = websocket.StatusCode(4001)
	closeDisabled     = websocket.StatusCode(4003)
	closeReplaced     = websocket.StatusCode(4008)
)

// Origins of a session end, deciding who gets notified and the audit actor.
const (
	fromBrowser = "browser"
	fromAgent   = "agent"
	fromAdmin   = "admin"
	fromSystem  = "system"
)

type wsConn struct {
	c         *websocket.Conn
	send      chan []byte
	ip        string
	ctx       context.Context
	cancel    context.CancelFunc
	limiter   *rate.Limiter
	strikes   int
	closeOnce sync.Once
}

func newWSConn(parent context.Context, c *websocket.Conn, ip string) *wsConn {
	ctx, cancel := context.WithCancel(parent)
	c.SetReadLimit(proto.MaxMessageBytes)
	return &wsConn{c: c, send: make(chan []byte, sendQueue), ip: ip, ctx: ctx, cancel: cancel,
		limiter: rate.NewLimiter(20, 40)}
}

func (w *wsConn) close(code websocket.StatusCode, reason string) {
	w.closeOnce.Do(func() {
		go func() {
			_ = w.c.Close(code, reason)
			w.cancel()
		}()
	})
}

// enqueue never blocks; a slow consumer is disconnected.
func (w *wsConn) enqueue(b []byte) {
	select {
	case w.send <- b:
	case <-w.ctx.Done():
	default:
		w.close(websocket.StatusPolicyViolation, "send queue full")
	}
}

func (w *wsConn) writer() {
	for {
		select {
		case <-w.ctx.Done():
			return
		case b := <-w.send:
			ctx, cancel := context.WithTimeout(w.ctx, 10*time.Second)
			err := w.c.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				w.close(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}

func (w *wsConn) pinger() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	misses := 0
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(w.ctx, pingInterval)
			err := w.c.Ping(ctx)
			cancel()
			if err != nil {
				misses++
				if misses >= 2 {
					w.close(websocket.StatusGoingAway, "ping timeout")
					return
				}
			} else {
				misses = 0
			}
		}
	}
}

// readLoop calls fn for each valid v=1 JSON text message until the socket closes.
func (w *wsConn) readLoop(h *Hub, fn func(typ string, raw []byte)) {
	for {
		mt, data, err := w.c.Read(w.ctx)
		if err != nil {
			return
		}
		if !w.limiter.Allow() {
			w.strikes++
			h.sendErr(w, "", "", proto.ErrRateLimited, "too many messages")
			if w.strikes > 50 {
				w.close(websocket.StatusPolicyViolation, "rate limit")
				return
			}
			continue
		}
		if mt != websocket.MessageText {
			h.sendErr(w, "", "", proto.ErrBadRequest, "text frames only")
			continue
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
			h.sendErr(w, "", "", proto.ErrBadRequest, "invalid message")
			continue
		}
		if env.V != proto.Version {
			h.sendErr(w, "", "", proto.ErrBadRequest, "unsupported protocol version")
			continue
		}
		start := time.Now()
		fn(env.Type, data)
		h.app.metrics.signalLatency.Observe(time.Since(start).Seconds())
	}
}

type agentConn struct {
	*wsConn
	deviceID string
	userID   string
}

type browserConn struct {
	*wsConn
	user *store.User
}

type liveSession struct {
	id          string
	deviceID    string
	userID      string
	userEmail   string
	state       string // pending | active
	createdAt   time.Time
	connectedAt time.Time
	browser     *browserConn
	clientKey   proto.ClientKeyInfo
	agentICE    []proto.ICEServer
}

// Hub owns presence (device → socket), browser sockets and live sessions.
// Nothing here is persisted except through store calls; on restart it is
// rebuilt as agents reconnect.
type Hub struct {
	app      *App
	bootTime time.Time

	mu          sync.Mutex
	agents      map[string]*agentConn
	browsers    map[string]map[*browserConn]struct{}
	sessions    map[string]*liveSession
	creating    map[string]bool // device ids with a session.create in flight
	seen        map[string]bool // devices connected at least once since boot
	reconnects  []time.Time
	bootClaimed bool

	updateTimers map[string][]*time.Timer // pending update.check pushes by batch key
	checkSpread  time.Duration            // per device in a bulk push (tests shorten it)
}

func newHub(a *App) *Hub {
	return &Hub{app: a, bootTime: time.Now(), agents: map[string]*agentConn{},
		browsers: map[string]map[*browserConn]struct{}{}, sessions: map[string]*liveSession{},
		creating: map[string]bool{}, seen: map[string]bool{}, updateTimers: map[string][]*time.Timer{}, checkSpread: checkSpread}
}

func dbctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (h *Hub) sendMsg(w *wsConn, typ string, msg any) {
	if w == nil {
		return
	}
	b, err := proto.Encode(typ, msg)
	if err != nil {
		h.app.log.Error("encode", "type", typ, "err", err)
		return
	}
	w.enqueue(b)
}

func (h *Hub) sendErr(w *wsConn, req, sessionID, code, msg string) {
	h.sendMsg(w, proto.TypeError, proto.Error{Req: req, SessionID: sessionID, Code: code, Message: msg})
}

// ---------- queries used by REST handlers and metrics ----------

func (h *Hub) Counts() (agents, browsers int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.browsers {
		browsers += len(m)
	}
	return len(h.agents), browsers
}

func (h *Hub) ActiveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, s := range h.sessions {
		if s.state == "active" {
			n++
		}
	}
	return n
}

func (h *Hub) IsOnline(deviceID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.agents[deviceID]
	return ok
}

func (h *Hub) OnlineDeviceIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.agents))
	for id := range h.agents {
		ids = append(ids, id)
	}
	return ids
}

func (h *Hub) ReconnectsPerMin() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := time.Now().Add(-time.Minute)
	i := 0
	for i < len(h.reconnects) && h.reconnects[i].Before(cut) {
		i++
	}
	h.reconnects = h.reconnects[i:]
	return float64(len(h.reconnects))
}

// decorate applies presence to a device row.
func (h *Hub) decorate(d *store.Device) {
	d.Online = !d.Disabled && h.IsOnline(d.ID)
	switch {
	case d.Disabled:
		d.Status = "disabled"
	case d.Online:
		d.Status = "online"
	default:
		d.Status = "offline"
	}
}

// ---------- pushes ----------

func (h *Hub) browsersOf(userID string) []*browserConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*browserConn, 0, len(h.browsers[userID]))
	for b := range h.browsers[userID] {
		out = append(out, b)
	}
	return out
}

func (h *Hub) agent(deviceID string) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[deviceID]
}

func (h *Hub) PushDeviceStatus(userID, deviceID, status string, lastSeen *time.Time) {
	var ls *string
	if lastSeen != nil {
		s := lastSeen.UTC().Format(time.RFC3339)
		ls = &s
	}
	msg := proto.DeviceStatus{DeviceID: deviceID, Online: status == "online", Status: status, LastSeenAt: ls}
	for _, b := range h.browsersOf(userID) {
		h.sendMsg(b.wsConn, proto.TypeDeviceStatus, msg)
	}
}

func (h *Hub) PushDeviceUpdate(deviceID, name string, autoAccept bool) {
	if ac := h.agent(deviceID); ac != nil {
		h.sendMsg(ac.wsConn, proto.TypeDeviceUpdate, proto.DeviceUpdate{Name: name, AutoAccept: autoAccept})
	}
}

func (h *Hub) PushKeyRevoked(userID string, spki []byte) {
	h.mu.Lock()
	var targets []*agentConn
	for _, ac := range h.agents {
		if ac.userID == userID {
			targets = append(targets, ac)
		}
	}
	h.mu.Unlock()
	msg := proto.KeyRevoked{PublicKey: base64.StdEncoding.EncodeToString(spki)}
	for _, ac := range targets {
		h.sendMsg(ac.wsConn, proto.TypeKeyRevoked, msg)
	}
}

// PushUpdateCheck sends update.check to the device; false when it is offline.
func (h *Hub) PushUpdateCheck(deviceID string, explicit, force bool) bool {
	ac := h.agent(deviceID)
	if ac == nil || ac.ctx.Err() != nil {
		return false
	}
	h.sendMsg(ac.wsConn, proto.TypeUpdateCheck, proto.UpdateCheck{Explicit: explicit, Force: force})
	return true
}

// ScheduleUpdateChecks spreads update.check to ids over spreadWindow,
// replacing the checks still pending under key. A device that went offline
// meanwhile is skipped; it asks again on its next hello.
func (h *Hub) ScheduleUpdateChecks(key string, ids []string, explicit bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stopTimers(h.updateTimers[key])
	delete(h.updateTimers, key)
	if len(ids) == 0 || h.app.ctx.Err() != nil {
		return
	}
	delays := spreadDelays(len(ids), spreadWindow(len(ids), h.checkSpread), rand.Int64N)
	timers := make([]*time.Timer, len(ids))
	for i, id := range ids {
		timers[i] = time.AfterFunc(delays[i], func() {
			if h.app.ctx.Err() == nil {
				h.PushUpdateCheck(id, explicit, false)
			}
		})
	}
	h.updateTimers[key] = timers
}

// CancelUpdateChecks drops the pending checks of key ("" = all, at shutdown).
func (h *Hub) CancelUpdateChecks(key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, ts := range h.updateTimers {
		if key == "" || k == key {
			stopTimers(ts)
			delete(h.updateTimers, k)
		}
	}
}

func stopTimers(ts []*time.Timer) {
	for _, t := range ts {
		t.Stop()
	}
}

// SetUpdateStatus stores a device's update state and relays it to the
// owner's browsers.
func (h *Hub) SetUpdateStatus(ctx context.Context, userID, deviceID string, st proto.UpdateStatus) error {
	at, err := store.SetDeviceUpdate(ctx, h.app.pool, deviceID, st.State, st.From, st.Version, st.Error)
	if err != nil {
		return err
	}
	msg := proto.DeviceUpdateStatus{DeviceID: deviceID, State: st.State, From: st.From, Version: st.Version, Error: st.Error,
		At: at.UTC().Format(time.RFC3339)}
	for _, b := range h.browsersOf(userID) {
		h.sendMsg(b.wsConn, proto.TypeDeviceUpdateStatus, msg)
	}
	return nil
}

// KickDevice ends the device's live sessions and closes its socket (4003).
func (h *Hub) KickDevice(ctx context.Context, deviceID, reason, origin string) {
	ids, err := store.LiveSessionIDs(ctx, h.app.pool, deviceID, "")
	if err != nil {
		h.app.log.Error("kick device: list sessions", "err", err)
	}
	for _, id := range ids {
		h.EndSession(id, reason, origin, 0, 0)
	}
	if ac := h.agent(deviceID); ac != nil {
		ac.close(closeDisabled, "device disabled or removed")
	}
}

// EndUserActivity ends a user's live sessions and closes their browser sockets.
func (h *Hub) EndUserActivity(ctx context.Context, userID, reason string) {
	ids, err := store.LiveSessionIDsByUser(ctx, h.app.pool, userID)
	if err != nil {
		h.app.log.Error("end user sessions", "err", err)
	}
	for _, id := range ids {
		h.EndSession(id, reason, fromAdmin, 0, 0)
	}
	for _, b := range h.browsersOf(userID) {
		b.close(closeUnauthorized, "signed out")
	}
}

// ---------- browser socket ----------

func (h *Hub) ServeBrowser(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "sign in required"}})
		return
	}
	if r.Header.Get("Origin") != h.app.cfg.PublicOrigin {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "forbidden", "message": "bad origin"}})
		return
	}
	h.mu.Lock()
	n := len(h.browsers[u.ID])
	h.mu.Unlock()
	if n >= maxBrowsersPerUser {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"code": "rate_limited", "message": "too many open tabs"}})
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	bc := &browserConn{wsConn: newWSConn(h.app.ctx, c, h.app.clientIP(r)), user: u}
	h.mu.Lock()
	if h.browsers[u.ID] == nil {
		h.browsers[u.ID] = map[*browserConn]struct{}{}
	}
	h.browsers[u.ID][bc] = struct{}{}
	h.mu.Unlock()

	go bc.writer()
	go bc.pinger()
	bc.readLoop(h, func(typ string, raw []byte) { h.onBrowser(bc, typ, raw) })
	bc.close(websocket.StatusNormalClosure, "")
	h.browserGone(bc)
}

func (h *Hub) browserGone(bc *browserConn) {
	h.mu.Lock()
	delete(h.browsers[bc.user.ID], bc)
	if len(h.browsers[bc.user.ID]) == 0 {
		delete(h.browsers, bc.user.ID)
	}
	var pending []string
	for _, s := range h.sessions {
		if s.browser != bc {
			continue
		}
		if s.state == "pending" {
			pending = append(pending, s.id)
		} else {
			s.browser = nil // P2P media keeps running without the server
		}
	}
	h.mu.Unlock()
	for _, id := range pending {
		h.EndSession(id, proto.EndPeerGone, fromBrowser, 0, 0)
	}
}

func (h *Hub) onBrowser(bc *browserConn, typ string, raw []byte) {
	switch typ {
	case proto.TypePing:
		var p proto.Ping
		_ = json.Unmarshal(raw, &p)
		h.sendMsg(bc.wsConn, proto.TypePong, proto.Ping{TS: p.TS})
	case proto.TypeSessionCreate:
		var m proto.SessionCreate
		if json.Unmarshal(raw, &m) != nil {
			h.sendErr(bc.wsConn, "", "", proto.ErrBadRequest, "invalid session.create")
			return
		}
		h.createSession(bc, m)
	case proto.TypeSessionOffer:
		var m proto.SessionOffer
		if json.Unmarshal(raw, &m) != nil || m.SDP == "" || m.FPSig == "" {
			h.sendErr(bc.wsConn, "", m.SessionID, proto.ErrBadRequest, "invalid session.offer")
			return
		}
		h.relayOffer(bc, m)
	case proto.TypeICECandidate:
		var m proto.ICECandidateMsg
		if json.Unmarshal(raw, &m) != nil {
			h.sendErr(bc.wsConn, "", "", proto.ErrBadRequest, "invalid ice.candidate")
			return
		}
		s := h.ownedByUser(m.SessionID, bc.user.ID)
		if s == nil {
			h.sendErr(bc.wsConn, "", m.SessionID, proto.ErrNotFound, "unknown session")
			return
		}
		if ac := h.agent(s.deviceID); ac != nil {
			h.sendMsg(ac.wsConn, proto.TypeICECandidate, proto.ICECandidateMsg{SessionID: m.SessionID, Candidate: m.Candidate})
		}
	case proto.TypeSessionEnd:
		var m proto.SessionEnd
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		if s := h.ownedByUser(m.SessionID, bc.user.ID); s == nil {
			// Could be an active session not yet reclaimed after a restart.
			ctx, cancel := dbctx()
			row, err := store.SessionByID(ctx, h.app.pool, m.SessionID)
			cancel()
			if err != nil || row.UserID != bc.user.ID {
				h.sendErr(bc.wsConn, "", m.SessionID, proto.ErrNotFound, "unknown session")
				return
			}
		}
		h.EndSession(m.SessionID, browserEndReason(m.Reason), fromBrowser, 0, 0)
	default:
		h.sendErr(bc.wsConn, "", "", proto.ErrBadRequest, "unknown message type "+typ)
	}
}

// browserEndReason limits what a browser may claim as the end reason.
func browserEndReason(r string) string {
	switch r {
	case proto.EndUserHangup, proto.EndICEFailed, proto.EndTimeout, proto.EndBadSignature, proto.EndError, proto.EndPeerGone:
		return r
	}
	return proto.EndUserHangup
}

func (h *Hub) ownedByUser(sessionID, userID string) *liveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[sessionID]
	if s == nil || s.userID != userID {
		return nil
	}
	return s
}

func (h *Hub) createSession(bc *browserConn, m proto.SessionCreate) {
	fail := func(code, msg string) { h.sendErr(bc.wsConn, m.Req, "", code, msg) }
	ctx, cancel := dbctx()
	defer cancel()
	pool := h.app.pool

	d, err := store.DeviceByID(ctx, pool, m.DeviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (d.UserID != bc.user.ID || d.Removed)) {
		fail(proto.ErrNotFound, "device not found")
		return
	}
	if err != nil {
		fail(proto.ErrInternal, "internal error")
		return
	}
	if d.Disabled {
		fail(proto.ErrDeviceDisabled, "device is disabled")
		return
	}
	ac := h.agent(d.ID)
	if ac == nil {
		fail(proto.ErrDeviceOffline, "device is offline")
		return
	}
	ck, err := store.ClientKeyByID(ctx, pool, m.ClientKeyID)
	if err != nil || ck.UserID != bc.user.ID || ck.RevokedAt != nil {
		fail(proto.ErrClientKeyInvalid, "client key is unknown or revoked")
		return
	}

	h.mu.Lock()
	busy := h.creating[d.ID]
	for _, s := range h.sessions {
		if s.deviceID == d.ID {
			busy = true
		}
	}
	if !busy {
		h.creating[d.ID] = true
	}
	h.mu.Unlock()
	if busy {
		fail(proto.ErrDeviceBusy, "device already has a session")
		return
	}
	defer func() {
		h.mu.Lock()
		delete(h.creating, d.ID)
		h.mu.Unlock()
	}()
	if dbBusy, err := store.DeviceBusy(ctx, pool, d.ID); err != nil || dbBusy {
		fail(proto.ErrDeviceBusy, "device already has a session")
		return
	}

	id, err := store.CreateSession(ctx, pool, d.ID, bc.user.ID, ck.ID, bc.ip)
	if err != nil {
		h.app.log.Error("create session", "err", err)
		fail(proto.ErrInternal, "internal error")
		return
	}
	_ = store.TouchClientKey(ctx, pool, ck.ID)
	h.app.audit(ctx, store.Audit{Actor: "user", Event: "session.created", UserID: bc.user.ID, DeviceID: d.ID, SessionID: id,
		IP: bc.ip, Meta: map[string]any{"device_name": d.Name, "client_key_id": ck.ID, "client_key_label": ck.Label, "force_relay": m.ForceRelay}})

	ls := &liveSession{id: id, deviceID: d.ID, userID: bc.user.ID, userEmail: bc.user.Email, state: "pending",
		createdAt: time.Now(), browser: bc,
		clientKey: proto.ClientKeyInfo{ID: ck.ID, PublicKey: base64.StdEncoding.EncodeToString(ck.PublicKey),
			Label: ck.Label, CreatedAt: ck.CreatedAt.UTC().Format(time.RFC3339)},
		agentICE: h.app.iceServers(id, "a", h.app.cfg.TURNURLsAgent)}
	h.mu.Lock()
	h.sessions[id] = ls
	h.mu.Unlock()

	h.sendMsg(bc.wsConn, proto.TypeSessionCreated, proto.SessionCreated{Req: m.Req, SessionID: id,
		ICEServers: h.app.iceServers(id, "b", h.app.cfg.TURNURLs), ForceRelay: m.ForceRelay,
		Device: proto.SessionDevice{ID: d.ID, Name: d.Name, PublicKey: base64.StdEncoding.EncodeToString(d.PublicKey)}})
}

func (h *Hub) relayOffer(bc *browserConn, m proto.SessionOffer) {
	h.mu.Lock()
	s := h.sessions[m.SessionID]
	if s != nil && s.userID == bc.user.ID {
		s.browser = bc
	}
	h.mu.Unlock()
	if s == nil || s.userID != bc.user.ID {
		h.sendErr(bc.wsConn, "", m.SessionID, proto.ErrNotFound, "unknown session")
		return
	}
	ac := h.agent(s.deviceID)
	if ac == nil {
		h.sendErr(bc.wsConn, "", m.SessionID, proto.ErrDeviceOffline, "device is offline")
		if s.state == "pending" {
			h.EndSession(s.id, proto.EndDeviceOffline, fromSystem, 0, 0)
		}
		return
	}
	ck := s.clientKey
	h.sendMsg(ac.wsConn, proto.TypeSessionOffer, proto.SessionOffer{SessionID: s.id, SDP: m.SDP, FPSig: m.FPSig,
		ClientKey: &ck, User: &proto.UserInfo{Email: s.userEmail}, ICEServers: s.agentICE})
}

// toSessionBrowser sends to the browser that owns the session, or to every
// browser of the user when that socket is gone (reconnected tab).
func (h *Hub) toSessionBrowser(s *liveSession, typ string, msg any) {
	h.mu.Lock()
	b := s.browser
	h.mu.Unlock()
	if b != nil && b.ctx.Err() == nil {
		h.sendMsg(b.wsConn, typ, msg)
		return
	}
	for _, b := range h.browsersOf(s.userID) {
		h.sendMsg(b.wsConn, typ, msg)
	}
}

// ---------- agent socket ----------

func bearer(r *http.Request) string {
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		return strings.TrimPrefix(a, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

func (h *Hub) ServeAgent(w http.ResponseWriter, r *http.Request) {
	a := h.app
	deviceID, err := security.ParseAgentJWT(a.cfg.JWTSecret, bearer(r))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "invalid agent token"}})
		return
	}
	ctx, cancel := dbctx()
	d, err := store.DeviceByID(ctx, a.pool, deviceID)
	cancel()
	if err != nil || d.Removed {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "device_removed", "message": "device removed"}})
		return
	}
	if d.Disabled {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "device_disabled", "message": "device disabled"}})
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ac := &agentConn{wsConn: newWSConn(a.ctx, c, a.clientIP(r)), deviceID: d.ID, userID: d.UserID}

	h.mu.Lock()
	old := h.agents[d.ID]
	h.agents[d.ID] = ac
	if h.seen[d.ID] {
		h.reconnects = append(h.reconnects, time.Now())
		a.metrics.reconnects.Inc()
	}
	h.seen[d.ID] = true
	h.mu.Unlock()
	if old != nil {
		old.close(closeReplaced, "replaced by a newer connection")
	}
	h.PushDeviceStatus(d.UserID, d.ID, "online", nil)

	go ac.writer()
	go ac.pinger()
	ac.readLoop(h, func(typ string, raw []byte) { h.onAgent(ac, typ, raw) })
	ac.close(websocket.StatusNormalClosure, "")
	h.agentGone(ac)
}

func (h *Hub) agentGone(ac *agentConn) {
	h.mu.Lock()
	if h.agents[ac.deviceID] != ac {
		h.mu.Unlock()
		return // replaced; the newer connection owns presence
	}
	delete(h.agents, ac.deviceID)
	var pending []string
	for _, s := range h.sessions {
		if s.deviceID == ac.deviceID && s.state == "pending" {
			pending = append(pending, s.id)
		}
	}
	h.mu.Unlock()
	for _, id := range pending {
		h.EndSession(id, proto.EndDeviceOffline, fromSystem, 0, 0)
	}
	ctx, cancel := dbctx()
	defer cancel()
	if err := store.SetDeviceOffline(ctx, h.app.pool, ac.deviceID); err != nil {
		h.app.log.Error("device offline", "err", err)
	}
	d, err := store.DeviceByID(ctx, h.app.pool, ac.deviceID)
	if err == nil && !d.Removed {
		status := "offline"
		if d.Disabled {
			status = "disabled"
		}
		h.PushDeviceStatus(ac.userID, ac.deviceID, status, d.LastSeenAt)
	}
}

// agentSession returns the live session if it belongs to the agent's device.
func (h *Hub) agentSession(ac *agentConn, id string) *liveSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil || s.deviceID != ac.deviceID {
		return nil
	}
	return s
}

// fileSessionUser returns the user of a session of the agent's device
// that connected. File records are flushed just before session.end, so a
// session that has already ended is accepted from its stored row.
func (h *Hub) fileSessionUser(ac *agentConn, id string) (string, bool) {
	if s := h.agentSession(ac, id); s != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		return s.userID, s.state == "active"
	}
	ctx, cancel := dbctx()
	defer cancel()
	row, err := store.SessionByID(ctx, h.app.pool, id)
	if err != nil || row.DeviceID != ac.deviceID || row.ConnectedAt == nil {
		return "", false
	}
	return row.UserID, true
}

func (h *Hub) onAgent(ac *agentConn, typ string, raw []byte) {
	a := h.app
	switch typ {
	case proto.TypePing:
		var p proto.Ping
		_ = json.Unmarshal(raw, &p)
		h.sendMsg(ac.wsConn, proto.TypePong, proto.Ping{TS: p.TS})
	case proto.TypeHello:
		var m proto.Hello
		if json.Unmarshal(raw, &m) != nil {
			h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "invalid hello")
			return
		}
		h.onHello(ac, m)
	case proto.TypeSessionStatus, proto.TypeSessionAnswer, proto.TypeICECandidate:
		var ref struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &ref)
		s := h.agentSession(ac, ref.SessionID)
		if s == nil {
			h.sendErr(ac.wsConn, "", ref.SessionID, proto.ErrNotFound, "unknown session")
			return
		}
		var msg any
		switch typ {
		case proto.TypeSessionStatus:
			var m proto.SessionStatus
			_ = json.Unmarshal(raw, &m)
			msg = proto.SessionStatus{SessionID: s.id, Status: m.Status}
		case proto.TypeSessionAnswer:
			var m proto.SessionAnswer
			_ = json.Unmarshal(raw, &m)
			msg = proto.SessionAnswer{SessionID: s.id, SDP: m.SDP, FPSig: m.FPSig}
		default:
			var m proto.ICECandidateMsg
			_ = json.Unmarshal(raw, &m)
			msg = proto.ICECandidateMsg{SessionID: s.id, Candidate: m.Candidate}
		}
		h.toSessionBrowser(s, typ, msg)
	case proto.TypeSessionState:
		var m proto.SessionState
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		s := h.agentSession(ac, m.SessionID)
		if s == nil {
			h.sendErr(ac.wsConn, "", m.SessionID, proto.ErrNotFound, "unknown session")
			return
		}
		if m.Connected {
			h.markActive(ac, s, m)
		}
	case proto.TypeSessionEnd:
		var m proto.SessionEnd
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		if h.agentSession(ac, m.SessionID) == nil {
			ctx, cancel := dbctx()
			row, err := store.SessionByID(ctx, a.pool, m.SessionID)
			cancel()
			if err != nil || row.DeviceID != ac.deviceID {
				return
			}
		}
		reason := m.Reason
		if reason == "" {
			reason = proto.EndAgentKill
		}
		h.EndSession(m.SessionID, reason, fromAgent, m.BytesSent, m.BytesRecv)
	case proto.TypeSessionFile:
		var m proto.SessionFile
		if json.Unmarshal(raw, &m) != nil {
			h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "invalid session.file")
			return
		}
		if len(m.Files) > proto.MaxFileRecords {
			h.sendErr(ac.wsConn, "", m.SessionID, proto.ErrBadRequest, "too many file records")
			return
		}
		userID, ok := h.fileSessionUser(ac, m.SessionID)
		if !ok {
			h.sendErr(ac.wsConn, "", m.SessionID, proto.ErrNotFound, "unknown session")
			return
		}
		ctx, cancel := dbctx()
		defer cancel()
		for _, r := range m.Files {
			if !validFileRecord(r) {
				a.log.Warn("invalid file record", "session", m.SessionID, "xfer", r.Xfer)
				continue
			}
			a.audit(ctx, store.Audit{Actor: "agent", Event: fileEvent(r), UserID: userID, DeviceID: ac.deviceID,
				SessionID: m.SessionID, IP: ac.ip, Meta: fileMeta(r)})
		}
	case proto.TypeTokenRenew:
		tok, exp, err := security.IssueAgentJWT(a.cfg.JWTSecret, ac.deviceID, time.Now(), agentJWTTTL)
		if err != nil {
			return
		}
		h.sendMsg(ac.wsConn, proto.TypeToken, proto.Token{Token: tok, ExpiresAt: exp.UTC().Format(time.RFC3339)})
	case proto.TypePermissions:
		var m proto.PermissionsMsg
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		b, _ := json.Marshal(m.Permissions)
		ctx, cancel := dbctx()
		defer cancel()
		if err := store.SetDevicePermissions(ctx, a.pool, ac.deviceID, b); err != nil {
			a.log.Error("set permissions", "err", err)
		}
	case proto.TypePinsSync:
		var m proto.PinsSync
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		h.syncPins(ac, m.PinnedClientKeys)
	case proto.TypeUpdateStatus:
		var m proto.UpdateStatus
		if json.Unmarshal(raw, &m) != nil || !slices.Contains(proto.UpdateStates, m.State) {
			h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "invalid update.status")
			return
		}
		h.onUpdateStatus(ac, proto.UpdateStatus{State: m.State, From: cleanText(m.From, proto.MaxUpdateVersionLen),
			Version: cleanText(m.Version, proto.MaxUpdateVersionLen), Error: cleanText(m.Error, proto.MaxUpdateErrorLen)})
	default:
		h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "unknown message type "+typ)
	}
}

func (h *Hub) onUpdateStatus(ac *agentConn, st proto.UpdateStatus) {
	a := h.app
	ctx, cancel := dbctx()
	defer cancel()
	d, err := store.DeviceByID(ctx, a.pool, ac.deviceID)
	if err != nil {
		a.log.Error("update status device", "err", err)
		return
	}
	if st == (proto.UpdateStatus{State: d.UpdateState, From: d.UpdateFrom, Version: d.UpdateVersion, Error: d.UpdateError}) {
		return
	}
	if err := h.SetUpdateStatus(ctx, d.UserID, d.ID, st); err != nil {
		a.log.Error("update status", "err", err)
		return
	}
	if event, ok := auditEventFor(d.UpdateState, st); ok {
		a.audit(ctx, store.Audit{Actor: "agent", Event: event, UserID: d.UserID, DeviceID: d.ID, IP: ac.ip,
			Meta: map[string]any{"from": st.From, "version": st.Version, "error": st.Error}})
	}
}

func (h *Hub) syncPins(ac *agentConn, pins []proto.PinnedClient) {
	in := make([]store.PinInput, 0, len(pins))
	for _, p := range pins {
		if len(p.PublicKeySHA256) != 64 {
			continue
		}
		pi := store.PinInput{SHA256: strings.ToLower(p.PublicKeySHA256), Label: p.Label}
		if t, err := time.Parse(time.RFC3339, p.ApprovedAt); err == nil {
			pi.ApprovedAt = &t
		}
		in = append(in, pi)
	}
	ctx, cancel := dbctx()
	defer cancel()
	if err := store.ReplacePins(ctx, h.app.pool, ac.deviceID, in); err != nil {
		h.app.log.Error("sync pins", "err", err)
	}
}

func (h *Hub) onHello(ac *agentConn, m proto.Hello) {
	a := h.app
	ctx, cancel := dbctx()
	defer cancel()
	caps, _ := json.Marshal(m.Capabilities)
	perms, _ := json.Marshal(m.Permissions)
	mons := m.Capabilities.Monitors
	if mons == nil {
		mons = []proto.Monitor{}
	}
	monsJSON, _ := json.Marshal(mons)
	if err := store.ApplyHello(ctx, a.pool, ac.deviceID, store.DeviceHello{
		Hostname: trunc(m.Hostname, 255), OS: trunc(m.OS, 32), Arch: trunc(m.Arch, 32), Version: trunc(m.AgentVersion, 64),
		Encoder: trunc(m.Capabilities.Encoder, 64), IP: ac.ip, Capabilities: caps, Permissions: perms, Monitors: monsJSON,
	}); err != nil {
		a.log.Error("apply hello", "err", err)
	}
	if m.PinnedClientKeys != nil {
		h.syncPins(ac, m.PinnedClientKeys)
	}

	// Reconcile active sessions that survived a server restart or a socket flap.
	claimed := map[string]bool{}
	for _, id := range m.ActiveSessions {
		claimed[id] = true
	}
	active, err := store.LiveSessionIDs(ctx, a.pool, ac.deviceID, "active")
	if err != nil {
		a.log.Error("reconcile", "err", err)
	}
	for _, id := range active {
		if !claimed[id] {
			h.EndSession(id, proto.EndPeerGone, fromSystem, 0, 0)
			continue
		}
		h.claim(ctx, id)
	}

	d, err := store.DeviceByID(ctx, a.pool, ac.deviceID)
	if err != nil {
		a.log.Error("hello device", "err", err)
		return
	}
	if st, event, ok := helloOutcome(d.UpdateState, d.UpdateFrom, d.UpdateVersion, d.UpdateAt, d.AgentVersion, time.Now()); ok {
		if err := h.SetUpdateStatus(ctx, d.UserID, d.ID, st); err != nil {
			a.log.Error("hello update outcome", "err", err)
		} else if event != "" {
			a.audit(ctx, store.Audit{Actor: "agent", Event: event, UserID: d.UserID, DeviceID: d.ID, IP: ac.ip,
				Meta: map[string]any{"from": st.From, "version": st.Version, "error": st.Error}})
		}
	}
	ack := proto.HelloAck{ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
		Device:            proto.DeviceInfo{ID: d.ID, Name: d.Name, AutoAccept: d.AutoAccept},
		RevokedClientKeys: []string{}}
	if rels, err := store.ListReleases(ctx, a.pool); err != nil {
		a.log.Error("hello releases", "err", err)
	} else if rel := pickRelease(rels, m.OS, m.Arch, m.AgentVersion, proto.RolloutBucket(d.ID), false); rel != nil {
		ack.Update = proto.UpdateHint{Available: true, Version: rel.Version}
	}
	if keys, err := store.RevokedClientKeys(ctx, a.pool, d.UserID); err == nil {
		for _, k := range keys {
			ack.RevokedClientKeys = append(ack.RevokedClientKeys, base64.StdEncoding.EncodeToString(k))
		}
	}
	h.sendMsg(ac.wsConn, proto.TypeHelloAck, ack)
}

// claim loads an active DB session into the live map (browser unknown).
func (h *Hub) claim(ctx context.Context, id string) {
	h.mu.Lock()
	_, ok := h.sessions[id]
	h.mu.Unlock()
	if ok {
		return
	}
	row, err := store.SessionByID(ctx, h.app.pool, id)
	if err != nil || row.State != "active" {
		return
	}
	s := &liveSession{id: row.ID, deviceID: row.DeviceID, userID: row.UserID, userEmail: row.UserEmail,
		state: "active", createdAt: row.CreatedAt}
	if row.ConnectedAt != nil {
		s.connectedAt = *row.ConnectedAt
	}
	h.mu.Lock()
	if _, ok := h.sessions[id]; !ok {
		h.sessions[id] = s
	}
	h.mu.Unlock()
}

func validPath(p string) string {
	switch p {
	case proto.PathHost, proto.PathSrflx, proto.PathRelay:
		return p
	}
	return ""
}

func (h *Hub) markActive(ac *agentConn, s *liveSession, m proto.SessionState) {
	a := h.app
	ctx, cancel := dbctx()
	defer cancel()
	path := validPath(m.Path)
	ok, err := store.MarkSessionActive(ctx, a.pool, s.id, path, ac.ip)
	if err != nil {
		a.log.Error("mark active", "err", err)
		return
	}
	if ok {
		h.mu.Lock()
		s.state = "active"
		s.connectedAt = time.Now()
		h.mu.Unlock()
		row, _ := store.SessionByID(ctx, a.pool, s.id)
		clientIP := ""
		if row != nil && row.ClientIP != nil {
			clientIP = *row.ClientIP
		}
		a.audit(ctx, store.Audit{Actor: "agent", Event: "session.connected", UserID: s.userID, DeviceID: s.deviceID, SessionID: s.id,
			IP: ac.ip, Meta: map[string]any{"path": path, "client_ip": clientIP, "agent_ip": ac.ip,
				"local_candidate": trunc(m.LocalCandidate, 256), "remote_candidate": trunc(m.RemoteCandidate, 256)}})
	} else if err := store.SetSessionPath(ctx, a.pool, s.id, path); err != nil {
		a.log.Error("set path", "err", err)
	}
	h.toSessionBrowser(s, proto.TypeSessionState, proto.SessionState{SessionID: s.id, Connected: true, Path: path})
}

// EndSession closes a session in the DB, audits, notifies the other side(s)
// and drops it from the live map. Safe to call more than once.
func (h *Hub) EndSession(id, reason, origin string, sent, recv int64) {
	a := h.app
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()

	ctx, cancel := dbctx()
	defer cancel()
	row, ok, err := store.EndSession(ctx, a.pool, id, reason, sent, recv)
	if err != nil {
		a.log.Error("end session", "id", id, "err", err)
		return
	}
	if ok {
		actor := map[string]string{fromBrowser: "user", fromAgent: "agent", fromAdmin: "admin", fromSystem: "system"}[origin]
		event := "session.ended"
		if row.State == "failed" {
			event = "session.failed"
		}
		meta := map[string]any{"reason": reason, "bytes_sent": row.BytesSent, "bytes_recv": row.BytesRecv}
		if row.DurationS != nil {
			meta["duration_s"] = *row.DurationS
		}
		a.audit(ctx, store.Audit{Actor: actor, Event: event, UserID: row.UserID, DeviceID: row.DeviceID, SessionID: id, Meta: meta})
		if reason == proto.EndAbuse {
			a.audit(ctx, store.Audit{Actor: "agent", Event: "input.rejected", UserID: row.UserID, DeviceID: row.DeviceID, SessionID: id,
				Meta: map[string]any{"reason": reason}})
		}
		path := "none"
		if row.ICEPath != nil {
			path = *row.ICEPath
		}
		result := "ended"
		if row.State == "failed" {
			result = "failed"
		}
		a.metrics.sessionsTotal.WithLabelValues(path, result).Inc()
	}
	if !ok && s == nil {
		return
	}
	msg := proto.SessionEnd{SessionID: id, Reason: reason}
	if origin != fromBrowser {
		if s != nil {
			h.toSessionBrowser(s, proto.TypeSessionEnd, msg)
		} else if row != nil {
			for _, b := range h.browsersOf(row.UserID) {
				h.sendMsg(b.wsConn, proto.TypeSessionEnd, msg)
			}
		}
	}
	if origin != fromAgent && row != nil {
		if ac := h.agent(row.DeviceID); ac != nil {
			h.sendMsg(ac.wsConn, proto.TypeSessionEnd, msg)
		}
	}
}

// ---------- background ----------

// BootSweep fails sessions left pending by a previous process.
func (h *Hub) BootSweep(ctx context.Context) error {
	if err := store.MarkAllOffline(ctx, h.app.pool); err != nil {
		return err
	}
	ids, err := store.LiveSessionIDs(ctx, h.app.pool, "", "pending")
	if err != nil {
		return err
	}
	for _, id := range ids {
		h.EndSession(id, proto.EndServerRestart, fromSystem, 0, 0)
	}
	return nil
}

// sweep enforces the pending timeout, max session length and the post-boot
// claim window.
func (h *Hub) sweep() {
	a := h.app
	now := time.Now()
	maxAge := time.Duration(a.Settings().MaxSessionHours) * time.Hour
	var expired, overlong []string
	h.mu.Lock()
	for _, s := range h.sessions {
		switch {
		case s.state == "pending" && now.Sub(s.createdAt) > pendingTimeout:
			expired = append(expired, s.id)
		case s.state == "active" && maxAge > 0 && !s.connectedAt.IsZero() && now.Sub(s.connectedAt) > maxAge:
			overlong = append(overlong, s.id)
		}
	}
	doClaim := !h.bootClaimed && now.Sub(h.bootTime) > bootClaimWindow
	if doClaim {
		h.bootClaimed = true
	}
	h.mu.Unlock()
	for _, id := range append(expired, overlong...) {
		h.EndSession(id, proto.EndTimeout, fromSystem, 0, 0)
	}
	if doClaim {
		ctx, cancel := dbctx()
		ids, err := store.LiveSessionIDs(ctx, a.pool, "", "active")
		cancel()
		if err != nil {
			a.log.Error("boot claim sweep", "err", err)
			return
		}
		for _, id := range ids {
			h.mu.Lock()
			_, live := h.sessions[id]
			h.mu.Unlock()
			if !live {
				h.EndSession(id, proto.EndPeerGone, fromSystem, 0, 0)
			}
		}
	}
}

// flushSeen updates last_seen_at for online devices (at most once a minute).
func (h *Hub) flushSeen() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.agents))
	for id := range h.agents {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	ctx, cancel := dbctx()
	defer cancel()
	if err := store.TouchDevicesSeen(ctx, h.app.pool, ids); err != nil {
		h.app.log.Error("flush last_seen", "err", err)
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

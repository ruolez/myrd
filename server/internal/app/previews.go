package app

import (
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

// Screen previews (PROTOCOL §3 "Screen previews"): while a dashboard shows
// a device, the server asks its agent for a sealed snapshot about once a
// minute, relays it to the watching browsers and keeps the latest one per
// device and browser key. It can read none of them; it checks the device's
// signature so it never relays or stores a forgery.
const (
	previewEvery        = time.Minute
	previewTimeout      = 20 * time.Second
	previewFirstJitter  = 15 * time.Second
	previewPersistEvery = 10 * time.Minute
	previewKeep         = 7 * 24 * time.Hour
	maxCachedPreviews   = 2000 // about 90 MB at the size limit
)

type previewWatcher struct {
	key     *store.ClientKey
	devices map[string]bool
}

type cachedPreview struct {
	msg       proto.Preview // device_id and received_at set
	pkSHA     string
	received  time.Time
	persisted time.Time
}

type previewFlight struct {
	req     string
	at      time.Time
	devPub  []byte
	keys    map[string]*store.ClientKey // client key id → recipient
	userIDs map[string]proto.UserInfo
}

type previewHub struct {
	mu       sync.Mutex
	every    time.Duration // tests shorten it
	jitter   time.Duration // spread of first requests (tests set 0)
	seq      uint64
	watch    map[*browserConn]*previewWatcher
	next     map[string]time.Time // device → when its next request is due
	inflight map[string]*previewFlight
	cache    map[string]map[string]*cachedPreview // device → client key → latest
	audited  map[string]string                    // device|key → UTC day of the last preview.served
}

func newPreviewHub() *previewHub {
	return &previewHub{every: previewEvery, jitter: previewFirstJitter, watch: map[*browserConn]*previewWatcher{}, next: map[string]time.Time{},
		inflight: map[string]*previewFlight{}, cache: map[string]map[string]*cachedPreview{}, audited: map[string]string{}}
}

// ---------- browser ----------

func (h *Hub) onPreviewWatch(bc *browserConn, raw []byte) {
	var m proto.PreviewWatch
	if json.Unmarshal(raw, &m) != nil || len(m.DeviceIDs) > proto.MaxPreviewWatch {
		h.sendErr(bc.wsConn, "", "", proto.ErrBadRequest, "invalid preview.watch")
		return
	}
	p := h.prev
	if len(m.DeviceIDs) == 0 || !h.app.Settings().ScreenPreviews {
		p.mu.Lock()
		delete(p.watch, bc)
		p.mu.Unlock()
		return
	}
	ctx, cancel := dbctx()
	defer cancel()
	key, err := store.ClientKeyByID(ctx, h.app.pool, m.ClientKeyID)
	if err != nil || key.UserID != bc.user.ID || key.RevokedAt != nil || key.PreviewKey == nil {
		h.sendErr(bc.wsConn, "", "", proto.ErrClientKeyInvalid, "client key unknown, revoked or without a preview key")
		return
	}
	var ids []string
	for _, id := range m.DeviceIDs {
		if _, err := uuid.Parse(id); err == nil {
			ids = append(ids, strings.ToLower(id))
		}
	}
	ds, err := store.DevicesByIDs(ctx, h.app.pool, ids)
	if err != nil {
		return
	}
	w := &previewWatcher{key: key, devices: map[string]bool{}}
	for _, d := range ds {
		if d.UserID == bc.user.ID {
			w.devices[d.ID] = true
		}
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.watch[bc]
	p.watch[bc] = w
	for id := range w.devices {
		if old != nil && old.devices[id] && old.key.ID == key.ID {
			continue
		}
		// A preview less than a minute old is still current (the browser
		// fetched it over REST); otherwise ask soon, spread out.
		due := now
		if p.jitter > 0 {
			due = now.Add(rand.N(p.jitter))
		}
		if c := p.cache[id][key.ID]; c != nil && c.pkSHA == key.PreviewSHA && now.Sub(c.received) < p.every {
			due = c.received.Add(p.every)
		}
		if n, ok := p.next[id]; !ok || due.Before(n) {
			p.next[id] = due
		}
	}
}

func (h *Hub) previewBrowserGone(bc *browserConn) {
	h.prev.mu.Lock()
	delete(h.prev.watch, bc)
	h.prev.mu.Unlock()
}

// ---------- scheduling ----------

type dueDevice struct {
	id   string
	keys map[string]*store.ClientKey
	user map[string]proto.UserInfo
}

// previewTick sends the preview requests that are due.
func (h *Hub) previewTick() {
	if !h.app.Settings().ScreenPreviews {
		return
	}
	p := h.prev
	now := time.Now()
	p.mu.Lock()
	byDevice := map[string]*dueDevice{}
	for bc, w := range p.watch {
		for id := range w.devices {
			if n, ok := p.next[id]; ok && now.Before(n) {
				continue
			}
			if f := p.inflight[id]; f != nil && now.Sub(f.at) < previewTimeout {
				continue
			}
			d := byDevice[id]
			if d == nil {
				d = &dueDevice{id: id, keys: map[string]*store.ClientKey{}, user: map[string]proto.UserInfo{}}
				byDevice[id] = d
			}
			d.keys[w.key.ID] = w.key
			d.user[w.key.ID] = proto.UserInfo{ID: bc.user.ID, Email: bc.user.Email}
		}
	}
	for id := range byDevice {
		p.next[id] = now.Add(p.every)
		delete(p.inflight, id)
	}
	p.mu.Unlock()
	if len(byDevice) == 0 {
		return
	}
	ids := make([]string, 0, len(byDevice))
	for id := range byDevice {
		ids = append(ids, id)
	}
	ctx, cancel := dbctx()
	defer cancel()
	ds, err := store.DevicesByIDs(ctx, h.app.pool, ids)
	if err != nil {
		h.app.log.Error("preview devices", "err", err)
		return
	}
	for _, d := range ds {
		due := byDevice[d.ID]
		var features []string
		_ = json.Unmarshal(d.Features, &features)
		ac := h.agent(d.ID)
		if ac == nil || d.Disabled || !d.Previews || !slices.Contains(features, proto.FeaturePreview) || h.isBusy(d.ID) {
			continue
		}
		pinned := map[string]bool{}
		for _, pc := range d.PinnedClients {
			if pc.ClientKeyID != nil {
				pinned[*pc.ClientKeyID] = true
			}
		}
		f := &previewFlight{at: now, devPub: d.PublicKey, keys: map[string]*store.ClientKey{}, userIDs: map[string]proto.UserInfo{}}
		var recipients []proto.PreviewRecipient
		for id, k := range due.keys {
			if !pinned[id] {
				// The agent would refuse it: this browser was never approved on
				// the device itself. One session from it fixes that.
				h.previewUnavailableTo(d.ID, id, proto.PreviewUntrusted)
				continue
			}
			if len(recipients) == proto.MaxPreviewRecipients {
				continue
			}
			f.keys[id], f.userIDs[id] = k, due.user[id]
			recipients = append(recipients, proto.PreviewRecipient{
				ClientKey: proto.ClientKeyInfo{ID: k.ID, PublicKey: base64.StdEncoding.EncodeToString(k.PublicKey), Label: k.Label,
					CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339)},
				PreviewKey: base64.StdEncoding.EncodeToString(k.PreviewKey), PreviewKeySig: base64.StdEncoding.EncodeToString(k.PreviewSig),
				User: due.user[id]})
		}
		if len(recipients) == 0 {
			continue
		}
		p.mu.Lock()
		p.seq++
		f.req = "pv" + strconv.FormatUint(p.seq, 10)
		p.inflight[d.ID] = f
		p.mu.Unlock()
		h.sendMsg(ac.wsConn, proto.TypePreviewRequest, proto.PreviewRequest{Req: f.req, Recipients: recipients})
	}
}

// ---------- agent ----------

// takeFlight removes the recipients a reply answers and returns the flight,
// or nil when the reply answers no request of this device.
func (p *previewHub) takeFlight(deviceID, req, clientKeyID string) (*previewFlight, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.inflight[deviceID]
	if f == nil || f.req != req {
		return nil, nil
	}
	var keys []string
	if clientKeyID == "" {
		for id := range f.keys {
			keys = append(keys, id)
		}
	} else if f.keys[clientKeyID] != nil {
		keys = []string{clientKeyID}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	cp := *f
	cp.keys = map[string]*store.ClientKey{}
	for _, id := range keys {
		cp.keys[id] = f.keys[id]
		delete(f.keys, id)
	}
	if len(f.keys) == 0 {
		delete(p.inflight, deviceID)
	}
	return &cp, keys
}

func (h *Hub) onAgentPreview(ac *agentConn, raw []byte) {
	var m proto.Preview
	if json.Unmarshal(raw, &m) != nil {
		h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "invalid preview")
		return
	}
	f, _ := h.prev.takeFlight(ac.deviceID, m.Req, m.ClientKeyID)
	if f == nil {
		h.sendErr(ac.wsConn, "", "", proto.ErrNotFound, "unknown preview request")
		return
	}
	key := f.keys[m.ClientKeyID]
	sealed, err := m.Sealed()
	if err != nil || len(sealed.Ciphertext) > proto.MaxPreviewCiphertext || m.Mime != proto.PreviewMime ||
		m.Width < 1 || m.Width > 640 || m.Height < 1 || m.Height > 640 ||
		proto.VerifyPreviewSig(f.devPub, m.Meta(ac.deviceID, key.SHA256, key.PreviewSHA), sealed) != nil {
		h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "preview rejected")
		return
	}
	now := time.Now()
	m.Req, m.DeviceID, m.ReceivedAt = "", ac.deviceID, now.UTC().Format(time.RFC3339)
	h.storePreview(m, key, now)
	for _, bc := range h.previewWatchers(ac.deviceID, m.ClientKeyID) {
		h.sendMsg(bc.wsConn, proto.TypePreview, m)
	}
	day := now.UTC().Format("2006-01-02")
	p := h.prev
	p.mu.Lock()
	first := p.audited[ac.deviceID+"|"+key.ID] != day
	p.audited[ac.deviceID+"|"+key.ID] = day
	p.mu.Unlock()
	if first {
		ctx, cancel := dbctx()
		defer cancel()
		h.app.audit(ctx, store.Audit{Actor: "agent", Event: "preview.served", UserID: f.userIDs[key.ID].ID, DeviceID: ac.deviceID,
			Meta: map[string]any{"client_key_id": key.ID, "client_key_label": key.Label}})
	}
}

func (h *Hub) onAgentPreviewUnavailable(ac *agentConn, raw []byte) {
	var m proto.PreviewUnavailable
	if json.Unmarshal(raw, &m) != nil || m.Reason == "" {
		return
	}
	f, keys := h.prev.takeFlight(ac.deviceID, m.Req, m.ClientKeyID)
	if f == nil {
		return
	}
	if m.Reason == proto.PreviewTooSoon {
		if m.RetryAfterS > 0 && m.RetryAfterS <= 300 {
			h.prev.mu.Lock()
			h.prev.next[ac.deviceID] = time.Now().Add(time.Duration(m.RetryAfterS) * time.Second)
			h.prev.mu.Unlock()
		}
		return
	}
	for _, k := range keys {
		h.previewUnavailableTo(ac.deviceID, k, cleanText(m.Reason, 32))
	}
}

func (h *Hub) previewUnavailableTo(deviceID, clientKeyID, reason string) {
	msg := proto.PreviewUnavailable{DeviceID: deviceID, Reason: reason, At: time.Now().UTC().Format(time.RFC3339)}
	for _, bc := range h.previewWatchers(deviceID, clientKeyID) {
		h.sendMsg(bc.wsConn, proto.TypePreviewUnavailable, msg)
	}
}

func (h *Hub) previewWatchers(deviceID, clientKeyID string) []*browserConn {
	h.prev.mu.Lock()
	defer h.prev.mu.Unlock()
	var out []*browserConn
	for bc, w := range h.prev.watch {
		if w.key.ID == clientKeyID && w.devices[deviceID] {
			out = append(out, bc)
		}
	}
	return out
}

// ---------- cache ----------

func (h *Hub) storePreview(m proto.Preview, key *store.ClientKey, now time.Time) {
	p := h.prev
	p.mu.Lock()
	byKey := p.cache[m.DeviceID]
	if byKey == nil {
		byKey = map[string]*cachedPreview{}
		p.cache[m.DeviceID] = byKey
	}
	c := byKey[key.ID]
	persist := c == nil || now.Sub(c.persisted) >= previewPersistEvery
	if c == nil {
		c = &cachedPreview{}
		byKey[key.ID] = c
	}
	c.msg, c.pkSHA, c.received = m, key.PreviewSHA, now
	if persist {
		c.persisted = now
	}
	p.evictLocked()
	p.mu.Unlock()
	if !persist {
		return
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	ctx, cancel := dbctx()
	defer cancel()
	if err := store.UpsertPreview(ctx, h.app.pool, store.StoredPreview{DeviceID: m.DeviceID, ClientKeyID: key.ID,
		PreviewSHA: key.PreviewSHA, Payload: b, CapturedAt: time.UnixMilli(m.CapturedAtMS), ReceivedAt: now}); err != nil {
		h.app.log.Error("store preview", "err", err)
	}
}

// evictLocked drops the oldest cached previews beyond the limit.
func (p *previewHub) evictLocked() {
	n := 0
	for _, byKey := range p.cache {
		n += len(byKey)
	}
	for ; n > maxCachedPreviews; n-- {
		var od, ok string
		var oldest time.Time
		for d, byKey := range p.cache {
			for k, c := range byKey {
				if od == "" || c.received.Before(oldest) {
					od, ok, oldest = d, k, c.received
				}
			}
		}
		delete(p.cache[od], ok)
		if len(p.cache[od]) == 0 {
			delete(p.cache, od)
		}
	}
}

// DropPreviews forgets the previews of a device ("" = every device), in
// memory and stored.
func (h *Hub) DropPreviews(deviceID string) {
	h.prev.mu.Lock()
	if deviceID == "" {
		h.prev.cache = map[string]map[string]*cachedPreview{}
	} else {
		delete(h.prev.cache, deviceID)
	}
	h.prev.mu.Unlock()
	ctx, cancel := dbctx()
	defer cancel()
	if err := store.DeletePreviews(ctx, h.app.pool, deviceID); err != nil {
		h.app.log.Error("drop previews", "err", err)
	}
}

// ---------- REST ----------

// getPreviews returns the latest sealed previews of the caller's devices for
// one of its browser keys (the dashboard's first paint).
func (a *App) getPreviews(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	keyID, err := queryUUID(r, "client_key_id")
	if err != nil || keyID == "" {
		return errf(http.StatusBadRequest, "bad_request", "client_key_id is required")
	}
	ids, err := validIDs(strings.Split(r.URL.Query().Get("device_ids"), ","), "device_ids")
	if err != nil || len(ids) == 0 || len(ids) > proto.MaxPreviewWatch {
		return errf(http.StatusBadRequest, "bad_request", "device_ids must list 1 to 100 devices")
	}
	ctx := r.Context()
	key, err := store.ClientKeyByID(ctx, a.pool, keyID)
	if err != nil || key.UserID != u.ID || key.RevokedAt != nil {
		return errNotFound
	}
	items := []proto.Preview{}
	if !a.Settings().ScreenPreviews || key.PreviewKey == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return nil
	}
	ds, err := store.DevicesByIDs(ctx, a.pool, ids)
	if err != nil {
		return err
	}
	owned := []string{}
	for _, d := range ds {
		if d.UserID == u.ID && d.Previews {
			owned = append(owned, d.ID)
		}
	}
	seen := map[string]bool{}
	p := a.hub.prev
	p.mu.Lock()
	for _, id := range owned {
		if c := p.cache[id][key.ID]; c != nil && c.pkSHA == key.PreviewSHA {
			items = append(items, c.msg)
			seen[id] = true
		}
	}
	p.mu.Unlock()
	stored, err := store.PreviewsFor(ctx, a.pool, key.ID, key.PreviewSHA, owned)
	if err != nil {
		return err
	}
	for _, s := range stored {
		var m proto.Preview
		if seen[s.DeviceID] || json.Unmarshal(s.Payload, &m) != nil {
			continue
		}
		items = append(items, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

// putPreviewKey registers a browser's preview key, vouched for by its client key.
func (a *App) putPreviewKey(w http.ResponseWriter, r *http.Request) error {
	u := userFrom(r)
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		PublicKey string `json:"public_key"`
		Signature string `json:"signature"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	ctx := r.Context()
	k, err := store.ClientKeyByID(ctx, a.pool, id)
	if err != nil || k.UserID != u.ID {
		return errNotFound
	}
	if k.RevokedAt != nil {
		return errf(http.StatusConflict, "conflict", "client key was revoked")
	}
	der, err := base64.StdEncoding.DecodeString(body.PublicKey)
	if err != nil {
		return errf(http.StatusBadRequest, "bad_request", "public_key must be base64 SPKI")
	}
	if _, err := proto.ParsePreviewKey(der); err != nil {
		return errf(http.StatusBadRequest, "bad_request", "public_key must be a P-256 key")
	}
	pkSHA := proto.SPKIHash(der)
	if proto.VerifyBrowserSig(base64.StdEncoding.EncodeToString(k.PublicKey), body.Signature, proto.PreviewKeyMessage(k.SHA256, pkSHA)) != nil {
		return errf(http.StatusBadRequest, "bad_signature", "the client key did not sign this preview key")
	}
	sig, _ := base64.StdEncoding.DecodeString(body.Signature)
	if err := store.SetPreviewKey(ctx, a.pool, k.ID, der, pkSHA, sig); err != nil {
		return err
	}
	if k.PreviewSHA != pkSHA {
		a.hub.prev.mu.Lock()
		for _, byKey := range a.hub.prev.cache {
			delete(byKey, k.ID)
		}
		a.hub.prev.mu.Unlock()
	}
	nk, err := store.ClientKeyByID(ctx, a.pool, k.ID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"client_key": nk})
	return nil
}

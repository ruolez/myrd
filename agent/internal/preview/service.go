package preview

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

const (
	// ReuseWithin: a picture this fresh is sealed again instead of captured.
	ReuseWithin = 20 * time.Second
	// MinInterval between two previews to the same browser key.
	MinInterval = 30 * time.Second
)

type Options struct {
	Backend   platform.Backend
	Pins      *pins.Store
	DeviceKey ed25519.PrivateKey
	DeviceID  func() string // from the device's own state, never from the server
	Send      func(typ string, msg any) error
	State     func() DeviceState
	Revoked   func(spkiB64 string) bool
	// OnServed is told about each preview sent (tray line).
	OnServed func(label string, at time.Time)
	Log      *slog.Logger
	Now      func() time.Time
	Rand     io.Reader
}

type picture struct {
	jpg  []byte
	w, h int
	at   time.Time
}

type Service struct {
	o Options

	mu       sync.Mutex
	running  bool
	last     *picture
	sent     map[string]time.Time // ck_sha → last preview
	keyCheck map[string]bool      // ck_sha + pk_sha → preview key signature verifies
}

func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = rand.Reader
	}
	if o.Revoked == nil {
		o.Revoked = func(string) bool { return false }
	}
	return &Service{o: o, sent: map[string]time.Time{}, keyCheck: map[string]bool{}}
}

// Handle takes a preview.request and answers it in the background; a
// request arriving while one is being served is dropped (the server retries).
func (s *Service) Handle(raw []byte) {
	var req proto.PreviewRequest
	if json.Unmarshal(raw, &req) != nil || len(req.Recipients) == 0 {
		return
	}
	if len(req.Recipients) > proto.MaxPreviewRecipients {
		req.Recipients = req.Recipients[:proto.MaxPreviewRecipients]
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}()
		s.Serve(req)
	}()
}

type target struct {
	r          proto.PreviewRecipient
	ckSHA      string
	pkSHA      string
	previewKey []byte
}

// Serve answers one request synchronously (Handle runs it in the background).
func (s *Service) Serve(req proto.PreviewRequest) {
	if reason := deviceReason(s.o.State()); reason != "" {
		s.unavailable(req.Req, "", reason, 0)
		return
	}
	now := s.o.Now()
	var targets []target
	for _, r := range req.Recipients {
		t, reason := s.check(r)
		if reason != "" {
			s.unavailable(req.Req, r.ClientKey.ID, reason, 0)
			continue
		}
		s.mu.Lock()
		last := s.sent[t.ckSHA]
		s.mu.Unlock()
		if wait := MinInterval - now.Sub(last); wait > 0 {
			s.unavailable(req.Req, r.ClientKey.ID, proto.PreviewTooSoon, int(wait/time.Second)+1)
			continue
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return
	}
	pic, reason := s.picture(now)
	if reason != "" {
		s.unavailable(req.Req, "", reason, 0)
		return
	}
	deviceID := s.o.DeviceID()
	for _, t := range targets {
		m := proto.PreviewMeta{DeviceID: deviceID, CKSHA: t.ckSHA, PKSHA: t.pkSHA, CapturedAtMS: pic.at.UnixMilli(),
			Width: pic.w, Height: pic.h, Mime: proto.PreviewMime}
		sealed, err := proto.SealPreview(s.o.Rand, s.o.DeviceKey, t.previewKey, m, pic.jpg)
		if err != nil {
			s.o.Log.Warn("preview: seal failed", "err", err)
			s.unavailable(req.Req, t.r.ClientKey.ID, proto.PreviewBadKey, 0)
			continue
		}
		b64 := base64.StdEncoding.EncodeToString
		msg := proto.Preview{Req: req.Req, ClientKeyID: t.r.ClientKey.ID, CapturedAtMS: m.CapturedAtMS, MonitorID: m.MonitorID,
			Width: m.Width, Height: m.Height, Mime: m.Mime, EphPub: b64(sealed.EphPub), IV: b64(sealed.IV),
			Ciphertext: b64(sealed.Ciphertext), Sig: b64(sealed.Sig)}
		if err := s.o.Send(proto.TypePreview, msg); err != nil {
			s.o.Log.Debug("preview not sent", "err", err)
			continue
		}
		s.mu.Lock()
		s.sent[t.ckSHA] = now
		s.mu.Unlock()
		if s.o.OnServed != nil {
			s.o.OnServed(t.r.ClientKey.Label, now)
		}
	}
}

// check decides whether a recipient may get a preview.
func (s *Service) check(r proto.PreviewRecipient) (target, string) {
	t := target{r: r}
	ck, err := base64.StdEncoding.DecodeString(r.ClientKey.PublicKey)
	if err != nil {
		return t, proto.PreviewUntrusted
	}
	t.ckSHA = proto.SPKIHash(ck)
	t.previewKey, err = base64.StdEncoding.DecodeString(r.PreviewKey)
	if err != nil {
		return t, proto.PreviewBadKey
	}
	t.pkSHA = proto.SPKIHash(t.previewKey)
	key := t.ckSHA + t.pkSHA
	s.mu.Lock()
	ok, known := s.keyCheck[key]
	s.mu.Unlock()
	if !known {
		ok = proto.VerifyBrowserSig(r.ClientKey.PublicKey, r.PreviewKeySig, proto.PreviewKeyMessage(t.ckSHA, t.pkSHA)) == nil
		if ok {
			_, perr := proto.ParsePreviewKey(t.previewKey)
			ok = perr == nil
		}
		s.mu.Lock()
		s.keyCheck[key] = ok
		s.mu.Unlock()
	}
	return t, keyReason(KeyState{
		Revoked:  s.o.Revoked(r.ClientKey.PublicKey),
		Pinned:   s.o.Pins.PinnedFor(r.ClientKey.PublicKey, r.User.ID),
		KeySigOK: ok,
	})
}

// picture returns a fresh enough JPEG of the primary monitor.
func (s *Service) picture(now time.Time) (*picture, string) {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last != nil && now.Sub(last.at) < ReuseWithin {
		return last, ""
	}
	img, err := s.o.Backend.Snapshot(0, MaxSide, MaxSide)
	if err != nil {
		return nil, snapshotReason(err)
	}
	jpg, w, h, err := encodeJPEG(img, proto.MaxPreviewCiphertext-16)
	if err != nil {
		s.o.Log.Warn("preview: encode failed", "err", err)
		return nil, proto.PreviewCaptureFailed
	}
	p := &picture{jpg: jpg, w: w, h: h, at: now}
	s.mu.Lock()
	s.last = p
	s.mu.Unlock()
	return p, ""
}

func snapshotReason(err error) string {
	switch {
	case errors.Is(err, platform.ErrBusy):
		return proto.PreviewInSession
	case errors.Is(err, platform.ErrLocked):
		return proto.PreviewLocked
	case errors.Is(err, platform.ErrAsleep):
		return proto.PreviewDisplayOff
	case errors.Is(err, platform.ErrPermission):
		return proto.PreviewNoPermission
	case errors.Is(err, platform.ErrUnsupported):
		return proto.PreviewUnsupported
	}
	return proto.PreviewCaptureFailed
}

func (s *Service) unavailable(req, clientKeyID, reason string, retry int) {
	_ = s.o.Send(proto.TypePreviewUnavailable, proto.PreviewUnavailable{Req: req, ClientKeyID: clientKeyID,
		Reason: reason, RetryAfterS: retry})
}

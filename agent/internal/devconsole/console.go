// Package devconsole is the Linux demo device's "physical screen": a small
// local web page standing in for the tray menu, the first-run enrollment
// window and the on-device consent prompt. It must only be reachable from
// the machine itself (compose binds it to 127.0.0.1).
package devconsole

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/session"
	"github.com/ruolez/remotectl/proto"
)

//go:embed static
var staticFS embed.FS

// Status is the device state the page renders.
type Status struct {
	Enrolled  bool             `json:"enrolled"`
	DeviceID  string           `json:"device_id"`
	Name      string           `json:"name"`
	ServerURL string           `json:"server_url"`
	Hostname  string           `json:"hostname"`
	Version   string           `json:"version"`
	Online    bool             `json:"online"`
	LastError string           `json:"last_error"`
	Fatal     string           `json:"fatal"`
	Paused    bool             `json:"paused"`
	Encoder   string           `json:"encoder"`
	Session   *session.Info    `json:"session"`
	Transfers []files.Activity `json:"transfers"` // newest first, see TransferLog
}

type Options struct {
	Addr          string
	Log           *slog.Logger
	Pins          *pins.Store
	Status        func() Status
	Enroll        func(ctx context.Context, serverURL, token string) error
	EndSession    func()
	SetPaused     func(bool)
	OnPinsChanged func()
}

type prompt struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Label     string    `json:"label"`
	Email     string    `json:"email"`
	FirstSeen string    `json:"first_seen"`
	NewKey    bool      `json:"new_key"`
	Expires   time.Time `json:"expires"`
	answer    chan platform.Consent
}

type Console struct {
	o       Options
	mu      sync.Mutex
	prompts map[string]*prompt
}

func New(o Options) *Console {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Console{o: o, prompts: map[string]*prompt{}}
}

var _ session.Prompter = (*Console)(nil)

// Prompt implements session.Prompter: it shows the request on the page and
// waits for the owner (or the context deadline → deny).
func (c *Console) Prompt(ctx context.Context, req session.ConsentRequest) (platform.Consent, error) {
	id := randomID()
	p := &prompt{ID: id, Title: req.Title, Message: req.Message, Label: req.Label, Email: req.Email,
		FirstSeen: req.FirstSeen, NewKey: req.NewKey, answer: make(chan platform.Consent, 1)}
	if dl, ok := ctx.Deadline(); ok {
		p.Expires = dl
	}
	c.mu.Lock()
	c.prompts[id] = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.prompts, id)
		c.mu.Unlock()
	}()
	c.o.Log.Info("consent prompt waiting on the device console", "label", req.Label, "email", req.Email)
	select {
	case a := <-p.answer:
		return a, nil
	case <-ctx.Done():
		return platform.ConsentDeny, ctx.Err()
	}
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/state", c.state)
	mux.HandleFunc("POST /api/enroll", c.guard(c.enroll))
	mux.HandleFunc("POST /api/prompts/{id}", c.guard(c.answer))
	mux.HandleFunc("POST /api/session/end", c.guard(func(w http.ResponseWriter, r *http.Request) {
		if c.o.EndSession != nil {
			c.o.EndSession()
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	mux.HandleFunc("POST /api/pause", c.guard(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Paused bool `json:"paused"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			writeErr(w, http.StatusBadRequest, "bad json")
			return
		}
		if c.o.SetPaused != nil {
			c.o.SetPaused(in.Paused)
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	mux.HandleFunc("DELETE /api/pins/{hash}", c.guard(func(w http.ResponseWriter, r *http.Request) {
		ok, err := c.o.Pins.RemoveHash(r.PathValue("hash"))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ok && c.o.OnPinsChanged != nil {
			c.o.OnPinsChanged()
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": ok})
	}))
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

// guard requires the custom header the page sends, so a cross-site form
// post from another tab cannot drive the console.
func (c *Console) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-Console") != "1" {
			writeErr(w, http.StatusForbidden, "missing X-Device-Console header")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		h(w, r)
	}
}

func (c *Console) state(w http.ResponseWriter, _ *http.Request) {
	st := Status{}
	if c.o.Status != nil {
		st = c.o.Status()
	}
	c.mu.Lock()
	ps := make([]*prompt, 0, len(c.prompts))
	for _, p := range c.prompts {
		ps = append(ps, p)
	}
	c.mu.Unlock()
	sort.Slice(ps, func(i, j int) bool { return ps[i].Expires.Before(ps[j].Expires) })
	var pinsList []proto.PinnedClient
	if c.o.Pins != nil {
		pinsList = c.o.Pins.List()
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "prompts": ps, "pins": pinsList})
}

func (c *Console) enroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	in.Token = strings.ToUpper(strings.TrimSpace(in.Token))
	if in.Token == "" || c.o.Enroll == nil {
		writeErr(w, http.StatusBadRequest, "enter the enrollment token from Devices → Add device")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := c.o.Enroll(ctx, strings.TrimSpace(in.ServerURL), in.Token); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (c *Console) answer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Answer string `json:"answer"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	a := map[string]platform.Consent{"deny": platform.ConsentDeny, "once": platform.ConsentOnce,
		"remember": platform.ConsentRemember}
	consent, ok := a[in.Answer]
	if !ok {
		writeErr(w, http.StatusBadRequest, "answer must be deny, once or remember")
		return
	}
	c.mu.Lock()
	p := c.prompts[r.PathValue("id")]
	c.mu.Unlock()
	if p == nil {
		writeErr(w, http.StatusNotFound, "prompt expired")
		return
	}
	select {
	case p.answer <- consent:
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// Run serves until ctx ends.
func (c *Console) Run(ctx context.Context) error {
	srv := &http.Server{Addr: c.o.Addr, Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", c.o.Addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	c.o.Log.Info("device console listening", "addr", c.o.Addr)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

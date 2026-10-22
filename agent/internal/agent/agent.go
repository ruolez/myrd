// Package agent wires the device agent together: enrollment, device key,
// signaling, the session manager, auto-update, the tray and (Linux demo
// device) the device console.
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/agent/internal/buildinfo"
	"github.com/ruolez/remotectl/agent/internal/config"
	"github.com/ruolez/remotectl/agent/internal/devconsole"
	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/agent/internal/identity"
	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/session"
	"github.com/ruolez/remotectl/agent/internal/signal"
	"github.com/ruolez/remotectl/agent/internal/tray"
	"github.com/ruolez/remotectl/agent/internal/update"
	"github.com/ruolez/remotectl/proto"
)

type Config struct {
	Settings config.Settings
	Version  string
	Backend  platform.Backend
	Keystore identity.Keystore // default identity.Default(DataDir)
	Log      *slog.Logger

	// Windows service mode: the service holds the socket but the session runs
	// in agent-session.exe. When Handler is set it replaces the in-process
	// session.Manager, and Capabilities supplies hello's capabilities.
	Handler      session.Handler
	Capabilities func() (proto.Capabilities, proto.Permissions)
}

type Agent struct {
	cfg  Config
	log  *slog.Logger
	ks   identity.Keystore
	pins *pins.Store

	mu       sync.Mutex
	state    config.State
	key      ed25519.PrivateKey
	sig      *signal.Client
	mgr      *session.Manager
	handler  session.Handler
	updater  *update.Updater
	console  *devconsole.Console
	trayUp   func(tray.State)
	sigState signal.Status
	enrolled chan struct{}
	activity string // tray line from fileFeed
	updLine  string // tray line from update.status

	fileFeed  *tray.ActivityFeed
	transfers devconsole.TransferLog

	autoAccept atomic.Bool
	paused     atomic.Bool
	installing atomic.Bool // refuse new sessions while the OS installer runs (BeforeInstall)
	prompting  atomic.Bool // the enrollment token dialog is open
	runCtx     context.Context
}

// enrollPoll is how often an unenrolled agent looks for an enrollment made
// from the command line.
var enrollPoll = 2 * time.Second

func New(cfg Config) (*Agent, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Keystore == nil {
		cfg.Keystore = identity.Default(cfg.Settings.DataDir)
	}
	if err := os.MkdirAll(cfg.Settings.DataDir, 0o700); err != nil {
		return nil, err
	}
	st, err := config.Load(cfg.Settings.DataDir)
	if err != nil {
		return nil, err
	}
	ps, err := pins.Open(filepath.Join(cfg.Settings.DataDir, "pinned-clients.json"))
	if err != nil {
		return nil, err
	}
	a := &Agent{cfg: cfg, log: cfg.Log, ks: cfg.Keystore, pins: ps, state: st, enrolled: make(chan struct{})}
	a.fileFeed = tray.NewActivityFeed(a.setActivity, tray.Notify)
	if st.Enrolled() {
		close(a.enrolled)
	}
	if a.state.ServerURL == "" {
		a.state.ServerURL = cfg.Settings.ServerURL
	}
	return a, nil
}

// Pins exposes the pinned-client store (tray/console).
func (a *Agent) Pins() *pins.Store { return a.pins }

func (a *Agent) State() config.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// Enroll exchanges a one-time token for a device id. It creates the device
// key on first use; the private key never leaves the keystore.
func (a *Agent) Enroll(ctx context.Context, serverURL, token string) error {
	if serverURL == "" {
		serverURL = a.State().ServerURL
	}
	if serverURL == "" {
		return errors.New("server URL required")
	}
	if a.State().Enrolled() {
		return errors.New("already enrolled; uninstall first")
	}
	key, err := a.ks.Load()
	if errors.Is(err, identity.ErrNotFound) {
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err == nil {
			err = a.ks.Save(key)
		}
	}
	if err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	res, err := api.New(serverURL).Enroll(ctx, api.EnrollRequest{
		Token: strings.ToUpper(strings.TrimSpace(token)), PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
		Hostname: a.cfg.Settings.Hostname, OS: runtime.GOOS, Arch: buildinfo.Arch, Version: a.cfg.Version,
	})
	if err != nil {
		return unreachable(serverURL, err)
	}
	st := config.State{ServerURL: strings.TrimRight(serverURL, "/"), DeviceID: res.DeviceID, Name: res.Name}
	if err := config.Save(a.cfg.Settings.DataDir, st); err != nil {
		return err
	}
	a.mu.Lock()
	a.state, a.key = st, key
	select {
	case <-a.enrolled:
	default:
		close(a.enrolled)
	}
	a.mu.Unlock()
	a.log.Info("enrolled", "device_id", res.DeviceID, "name", res.Name)
	a.pushTray()
	return nil
}

// unreachable explains a request that never got an answer from the server:
// a firewall such as Little Snitch that blocks the agent but not the
// browser looks like a plain timeout.
func unreachable(serverURL string, err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	return fmt.Errorf("could not reach %s (%w). Check this computer's internet connection, "+
		"and that no firewall (such as Little Snitch) blocks RemoteAgent", serverURL, ue.Err)
}

// Uninstall deletes the device server-side (best effort) and wipes local state.
func (a *Agent) Uninstall(ctx context.Context) error {
	st := a.State()
	if st.Enrolled() {
		if key, err := a.ks.Load(); err == nil {
			c := api.New(st.ServerURL)
			if tok, _, err := c.Authenticate(ctx, st.DeviceID, key); err == nil {
				if err := c.DeleteSelf(ctx, tok); err != nil {
					a.log.Warn("server-side removal failed", "err", err)
				}
			}
		}
	}
	_ = a.ks.Delete()
	_ = os.Remove(filepath.Join(a.cfg.Settings.DataDir, "pinned-clients.json"))
	return config.Remove(a.cfg.Settings.DataDir)
}

// EndSession is the local kill switch.
func (a *Agent) EndSession() {
	if h := a.currentHandler(); h != nil {
		h.EndAll(proto.EndAgentKill)
	}
}

func (a *Agent) SetPaused(p bool) {
	a.paused.Store(p)
	if p {
		a.EndSession()
	}
	a.pushTray()
}

func (a *Agent) currentHandler() session.Handler {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.handler
}

// TrayHooks are wired to the menu-bar / system-tray items.
func (a *Agent) TrayHooks(quit func()) tray.Hooks {
	return tray.Hooks{
		OnEndSession: a.EndSession,
		OnPause:      a.SetPaused,
		OnQuit:       quit,
		OnUninstall:  uninstallAction,
		OnEnroll:     a.enrollAction(),
	}
}

// enrollAction reopens the token dialog from the tray ("Enroll this
// device…"); nil where there is no native dialog.
func (a *Agent) enrollAction() func() {
	if promptEnrollToken == nil {
		return nil
	}
	return func() {
		a.mu.Lock()
		ctx := a.runCtx
		a.mu.Unlock()
		if ctx != nil && !a.State().Enrolled() {
			go a.promptForToken(ctx)
		}
	}
}

// SetTrayUpdater registers the tray's update function.
func (a *Agent) SetTrayUpdater(f func(tray.State)) {
	a.mu.Lock()
	a.trayUp = f
	a.mu.Unlock()
	a.pushTray()
}

// onFileActivity shows file transfers in the tray (and a notification on
// macOS) and on the device console.
func (a *Agent) onFileActivity(act files.Activity) {
	a.transfers.Add(act)
	a.fileFeed.Add(act)
}

func (a *Agent) setActivity(line string) {
	a.mu.Lock()
	a.activity = line
	a.mu.Unlock()
	a.pushTray()
}

func (a *Agent) pushTray() {
	a.mu.Lock()
	up := a.trayUp
	st := tray.State{Enrolled: a.state.Enrolled(), Online: a.sigState.Online, Paused: a.paused.Load(), Activity: a.activity,
		Update: a.updLine}
	mgr := a.mgr
	sigState := a.sigState
	a.mu.Unlock()
	if up == nil {
		return
	}
	switch {
	case !st.Enrolled:
		st.Status = "Waiting for enrollment"
	case sigState.Fatal != "":
		st.Status = "Refused by server: " + sigState.Fatal
	case st.Online:
		st.Status = "Online"
	default:
		st.Status = "Connecting…"
	}
	if mgr != nil {
		if info := mgr.Current(); info != nil {
			st.InSession = info.State == "active"
			st.Peer = fmt.Sprintf("%s (%s)", info.Label, info.Email)
			if info.State == proto.StatusAwaitingApproval {
				st.Status = "Approval requested by " + st.Peer
			} else if st.InSession {
				st.Status = "Session in progress: " + st.Peer
			}
		}
	}
	up(st)
}

// ConsoleStatus feeds the device console page.
func (a *Agent) consoleStatus() devconsole.Status {
	a.mu.Lock()
	st, ss, mgr := a.state, a.sigState, a.mgr
	a.mu.Unlock()
	out := devconsole.Status{Enrolled: st.Enrolled(), DeviceID: st.DeviceID, Name: st.Name, ServerURL: st.ServerURL,
		Hostname: a.cfg.Settings.Hostname, Version: a.cfg.Version, Online: ss.Online, LastError: ss.LastError,
		Fatal: ss.Fatal, Paused: a.paused.Load(), Encoder: a.cfg.Backend.EncoderName(), Transfers: a.transfers.List()}
	if mgr != nil {
		out.Session = mgr.Current()
	}
	return out
}

func (a *Agent) hello() proto.Hello {
	h := proto.Hello{AgentVersion: a.cfg.Version, OS: runtime.GOOS, Arch: buildinfo.Arch, Hostname: a.cfg.Settings.Hostname,
		PinnedClientKeys: a.pins.List(), ActiveSessions: []string{}}
	if a.cfg.Capabilities != nil {
		h.Capabilities, h.Permissions = a.cfg.Capabilities()
	} else {
		mons, _ := a.cfg.Backend.Monitors()
		for _, m := range mons {
			h.Capabilities.Monitors = append(h.Capabilities.Monitors, proto.Monitor{ID: m.ID, Name: m.Name, X: m.X, Y: m.Y,
				Width: m.Width, Height: m.Height, Scale: m.Scale, Primary: m.Primary})
		}
		h.Capabilities.Codecs = []string{"h264"}
		h.Capabilities.MaxFPS = session.EncoderMaxFPS(a.cfg.Backend.EncoderName())
		h.Capabilities.Encoder = a.cfg.Backend.EncoderName()
		p := a.cfg.Backend.Permissions()
		h.Permissions = proto.Permissions{Screen: p.Screen, Input: p.Input}
	}
	if hd := a.currentHandler(); hd != nil {
		h.ActiveSessions = hd.ActiveSessions()
	}
	if h.Capabilities.Monitors == nil {
		h.Capabilities.Monitors = []proto.Monitor{}
	}
	if h.PinnedClientKeys == nil {
		h.PinnedClientKeys = []proto.PinnedClient{}
	}
	return h
}

func (a *Agent) send(typ string, msg any) error {
	a.mu.Lock()
	s := a.sig
	a.mu.Unlock()
	if s == nil {
		return signal.ErrNotConnected
	}
	return s.Send(typ, msg)
}

// onUpdateStatus reports update progress to the server and in the tray.
func (a *Agent) onUpdateStatus(s proto.UpdateStatus) {
	line := tray.UpdateLine(s)
	a.mu.Lock()
	a.updLine = line
	a.mu.Unlock()
	a.pushTray()
	if s.State == proto.UpdateInstalling {
		tray.Notify(tray.ProductName, line)
	}
	if err := a.send(proto.TypeUpdateStatus, s); err != nil {
		a.log.Debug("update.status not sent", "state", s.State, "err", err)
	}
}

func (a *Agent) syncPins() {
	_ = a.send(proto.TypePinsSync, proto.PinsSync{PinnedClientKeys: a.pins.List()})
}

// Run blocks until ctx ends: serves the console, waits for enrollment, then
// keeps the control-plane connection up.
func (a *Agent) Run(ctx context.Context) error {
	a.mu.Lock()
	a.runCtx = ctx
	a.mu.Unlock()
	if err := a.cfg.Backend.Init(); err != nil {
		a.log.Error("native backend init failed; sessions will be refused", "backend", a.cfg.Backend.Name(), "err", err)
	}
	// The OS permission dialogs cover the token dialog; an unenrolled agent
	// asks for them once it is enrolled.
	askedPermissions := false
	askPermissions := func() {
		if askedPermissions || !requestPermissionsOnStart || a.cfg.Capabilities != nil {
			return
		}
		askedPermissions = true
		p := a.cfg.Backend.Permissions()
		if !p.Screen {
			a.cfg.Backend.RequestPermission(true)
		}
		if !p.Input {
			a.cfg.Backend.RequestPermission(false)
		}
	}
	if a.State().Enrolled() {
		askPermissions()
	}
	if a.cfg.Settings.ConsoleAddr != "" {
		a.console = devconsole.New(devconsole.Options{Addr: a.cfg.Settings.ConsoleAddr, Log: a.log, Pins: a.pins,
			Status: a.consoleStatus, Enroll: a.Enroll, EndSession: a.EndSession, SetPaused: a.SetPaused,
			OnPinsChanged: a.syncPins})
		go func() {
			if err := a.console.Run(ctx); err != nil {
				a.log.Error("device console failed", "err", err)
			}
		}()
	}

	for ctx.Err() == nil {
		if !a.State().Enrolled() {
			if tok := a.cfg.Settings.EnrollToken; tok != "" {
				if err := a.Enroll(ctx, a.cfg.Settings.ServerURL, tok); err != nil {
					a.log.Error("enrollment with RC_ENROLL_TOKEN failed", "err", err)
				}
			}
			if !a.State().Enrolled() {
				a.log.Info("waiting for enrollment (device console or `remote-agent enroll --token`)")
				if promptEnrollToken != nil {
					go a.promptForToken(ctx)
				}
				if err := a.awaitEnrollment(ctx); err != nil {
					return err
				}
			}
		}
		askPermissions()
		err := a.runConnected(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case api.IsCode(err, "device_removed", "not_found"):
			a.log.Warn("device was removed on the server; returning to enrollment")
			_ = config.Remove(a.cfg.Settings.DataDir)
			_ = a.ks.Delete()
			a.mu.Lock()
			a.state = config.State{ServerURL: a.state.ServerURL}
			a.enrolled = make(chan struct{})
			a.mu.Unlock()
			a.cfg.Settings.EnrollToken = "" // single-use; don't loop on it
		default:
			a.log.Warn("device refused by server; retrying in 5 minutes", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Minute):
			}
		}
		a.pushTray()
	}
	return ctx.Err()
}

// awaitEnrollment returns once the agent is enrolled: through Enroll, or by
// `remote-agent enroll` run in a terminal while this agent waits, which
// writes the same state file.
func (a *Agent) awaitEnrollment(ctx context.Context) error {
	t := time.NewTicker(enrollPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-a.enrolledCh():
			return nil
		case <-t.C:
			if st, err := config.Load(a.cfg.Settings.DataDir); err == nil && st.Enrolled() {
				a.adoptEnrollment(st)
			}
		}
	}
}

func (a *Agent) adoptEnrollment(st config.State) {
	a.mu.Lock()
	if a.state.Enrolled() {
		a.mu.Unlock()
		return
	}
	a.state = st
	close(a.enrolled)
	a.mu.Unlock()
	a.log.Info("enrolled from the command line", "device_id", st.DeviceID, "name", st.Name)
	a.pushTray()
}

func (a *Agent) enrolledCh() chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.enrolled
}

func (a *Agent) runConnected(ctx context.Context) error {
	key, err := a.ks.Load()
	if err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	st := a.State()
	client := api.New(st.ServerURL)

	handler := a.cfg.Handler
	var mgr *session.Manager
	if handler == nil {
		var prompter session.Prompter
		if a.console != nil {
			prompter = a.console
		}
		mgr, err = session.NewManager(session.Options{
			Backend: a.cfg.Backend, DeviceKey: key, Pins: a.pins, Send: a.send, Prompter: prompter,
			AutoAccept: a.autoAccept.Load, Paused: func() bool { return a.paused.Load() || a.installing.Load() },
			OnPinsChanged:   a.syncPins,
			OnSessionChange: func(*session.Info) { a.pushTray() },
			UDPPort:         a.cfg.Settings.UDPPort, NAT1To1IPs: a.cfg.Settings.NAT1To1IPs,
			AgentVersion: a.cfg.Version, Log: a.log, // MaxFPS unset: the session follows the live encoder
			Files: files.LocalHost{}, FileDeny: []string{a.cfg.Settings.DataDir}, OnFileActivity: a.onFileActivity,
		})
		if err != nil {
			return err
		}
		defer mgr.Close()
		handler = mgr
	}

	var sigClient *signal.Client
	busy := func() bool { return len(handler.ActiveSessions()) > 0 }
	if mgr != nil {
		busy = func() bool { return mgr.Current() != nil } // pending and awaiting approval count too
	}
	upd := update.New(update.Options{API: client, Token: func(c context.Context) (string, error) { return sigClient.Token(c) },
		Version: a.cfg.Version, DataDir: a.cfg.Settings.DataDir, Log: a.log,
		Busy: busy, EndAll: handler.EndAll, OnStatus: a.onUpdateStatus,
		BeforeInstall: func() func() {
			a.installing.Store(true)
			return func() { a.installing.Store(false) }
		},
		OnHandedOff: func() {
			if restartIntoUpdate == nil {
				return
			}
			// The install normally stops this process within seconds.
			time.AfterFunc(time.Minute, func() {
				a.log.Warn("still running after the update was installed; restarting into the new build")
				restartIntoUpdate()
			})
		}})
	sigClient = signal.New(signal.Options{
		API: client, DeviceID: st.DeviceID, Key: key, Handler: handler, Log: a.log, Hello: a.hello,
		OnHelloAck: func(m proto.HelloAck) {
			a.autoAccept.Store(m.Device.AutoAccept)
			a.renamed(m.Device.Name)
			if mgr != nil {
				mgr.SetRevoked(m.RevokedClientKeys)
			}
			upd.HelloAcked()
			if m.Update.Available {
				go upd.CheckNow(ctx, update.Trigger{})
			}
		},
		OnUpdateCheck: func(m proto.UpdateCheck) {
			go upd.CheckNow(ctx, update.Trigger{Explicit: m.Explicit, Force: m.Force})
		},
		OnDeviceUpdate: func(m proto.DeviceUpdate) {
			a.autoAccept.Store(m.AutoAccept)
			a.renamed(m.Name)
		},
		OnKeyRevoked: func(m proto.KeyRevoked) {
			if mgr != nil {
				mgr.Revoke(m.PublicKey)
			}
		},
		OnStatus: func(s signal.Status) {
			a.mu.Lock()
			a.sigState = s
			a.mu.Unlock()
			a.pushTray()
		},
	})
	a.mu.Lock()
	a.key, a.sig, a.mgr, a.handler, a.updater = key, sigClient, mgr, handler, upd
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.sig, a.mgr, a.handler = nil, nil, nil
		a.mu.Unlock()
	}()

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go upd.Run(cctx)
	go a.watchPermissions(cctx)
	a.pushTray()
	return sigClient.Run(cctx)
}

func (a *Agent) renamed(name string) {
	a.mu.Lock()
	changed := name != "" && name != a.state.Name
	if changed {
		a.state.Name = name
	}
	st := a.state
	a.mu.Unlock()
	if changed {
		_ = config.Save(a.cfg.Settings.DataDir, st)
	}
}

func (a *Agent) watchPermissions(ctx context.Context) {
	if a.cfg.Capabilities != nil {
		return // service mode: the session helper reports permissions
	}
	last := a.cfg.Backend.Permissions()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p := a.cfg.Backend.Permissions()
			if p != last {
				last = p
				_ = a.send(proto.TypePermissions, proto.PermissionsMsg{Permissions: proto.Permissions{Screen: p.Screen, Input: p.Input}})
			}
		}
	}
}

// promptForToken shows the native first-run dialog until an enrollment
// succeeds, the user cancels, or the agent enrolls another way.
func (a *Agent) promptForToken(ctx context.Context) {
	if !a.prompting.CompareAndSwap(false, true) {
		return
	}
	defer a.prompting.Store(false)
	var lastErr error
	for ctx.Err() == nil && !a.State().Enrolled() {
		tok, ok := promptEnrollToken(ctx, enrollPromptMessage(a.cfg.Settings.ServerURL, lastErr))
		if !ok {
			return
		}
		if lastErr = a.Enroll(ctx, a.cfg.Settings.ServerURL, tok); lastErr != nil {
			a.log.Error("enrollment failed", "server", a.cfg.Settings.ServerURL, "err", lastErr)
			continue
		}
		return
	}
}

// enrollPromptMessage names the server, so an installer built for the wrong
// one is obvious, and explains the previous failure on a retry.
func enrollPromptMessage(serverURL string, lastErr error) string {
	msg := "Enter the enrollment token shown in the web app under Devices → Add device."
	if serverURL == "" {
		msg += "\n\nThis installer has no server address built in. Download it again from your server's Add device page."
	} else {
		msg += "\n\nServer: " + serverURL
	}
	if lastErr != nil {
		msg += "\n\nEnrollment failed: " + lastErr.Error()
	}
	return msg
}

package winsvc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/agent/internal/buildinfo"
	"github.com/ruolez/remotectl/agent/internal/config"
	"github.com/ruolez/remotectl/agent/internal/identity"
	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/preview"
	"github.com/ruolez/remotectl/agent/internal/session"
	"github.com/ruolez/remotectl/agent/internal/signal"
	"github.com/ruolez/remotectl/agent/internal/sysinfo"
	"github.com/ruolez/remotectl/agent/internal/tray"
	"github.com/ruolez/remotectl/agent/internal/update"
	"github.com/ruolez/remotectl/agent/internal/vdd"
	"github.com/ruolez/remotectl/proto"
)

// Options shared by the service and the helper.
type Options struct {
	Settings   config.Settings
	Version    string
	Log        *slog.Logger
	PinsPath   string                  // default <data dir>/pinned-clients.json (same as internal/agent)
	NewBackend func() platform.Backend // helper only
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.PinsPath == "" {
		o.PinsPath = filepath.Join(o.Settings.DataDir, "pinned-clients.json")
	}
}

// ---------------------------------------------------------------- service

// ServiceCore returns the Core run by the LocalSystem service: enroll (from
// the MSI's one-time registry token), then keep the signaling socket up with
// the Proxy as its session handler.
func ServiceCore(o Options) Core {
	o.defaults()
	return func(ctx context.Context, p *Proxy) error {
		update.CountServiceStart(o.Settings.DataDir) // crash-loop detection for a pending update
		for ctx.Err() == nil {
			err := runConnected(ctx, &o, p)
			if ctx.Err() != nil {
				break
			}
			switch {
			case api.IsCode(err, "device_removed", "not_found"):
				// Same as internal/agent: forget the identity and wait for a new token.
				o.Log.Warn("winsvc: device removed on the server; returning to enrollment")
				config.Remove(o.Settings.DataDir)
				identity.Default(o.Settings.DataDir).Delete()
				o.Settings.EnrollToken = ""
				p.SetStatus(ServiceStatus{Status: "Removed — waiting for enrollment"})
			default:
				o.Log.Warn("winsvc: refused by server; retrying in 5 minutes", "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Minute):
				}
			}
		}
		return nil
	}
}

func runConnected(ctx context.Context, o *Options, p *Proxy) error {
	{
		st, key, err := ensureEnrolled(ctx, o)
		if err != nil {
			return err
		}
		client := api.New(st.ServerURL)
		p.SetDeviceKey(base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)))

		// Capabilities come from the helper; give it a moment so the first
		// hello carries real monitors and encoder.
		capsReady := make(chan struct{})
		var capsOnce sync.Once
		var sc *signal.Client
		var scMu sync.Mutex
		p.OnCaps = func(c HelperCaps) {
			capsOnce.Do(func() { close(capsReady) })
			scMu.Lock()
			s := sc
			scMu.Unlock()
			if s != nil {
				s.Send(proto.TypePermissions, proto.PermissionsMsg{Permissions: c.Permissions})
			}
		}
		select {
		case <-capsReady:
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}

		hostname := o.Settings.Hostname
		if hostname == "" {
			hostname, _ = os.Hostname()
		}
		var s *signal.Client
		// The service owns updates: detached msiexec, health check and rollback.
		upd := update.New(update.Options{API: client, Token: func(c context.Context) (string, error) { return s.Token(c) },
			Version: o.Version, DataDir: o.Settings.DataDir, Log: o.Log,
			Busy: func() bool { return len(p.ActiveSessions()) > 0 }, EndAll: p.EndAll,
			BeforeInstall: func() func() { return p.HoldHelper(5 * time.Second) },
			OnStatus: func(st proto.UpdateStatus) {
				p.SetUpdateLine(tray.UpdateLine(st))
				if err := s.Send(proto.TypeUpdateStatus, st); err != nil {
					o.Log.Debug("winsvc: update.status not sent", "state", st.State, "err", err)
				}
			}})
		s = signal.New(signal.Options{
			API:      client,
			DeviceID: st.DeviceID,
			Key:      key,
			Handler:  p,
			Log:      o.Log,
			Hello: func() proto.Hello {
				h := proto.Hello{
					AgentVersion:   o.Version,
					OS:             runtime.GOOS,
					Arch:           buildinfo.Arch,
					Hostname:       hostname,
					ActiveSessions: p.ActiveSessions(),
				}
				if c, ok := p.Caps(); ok {
					h.Capabilities = c.Capabilities
					h.Permissions = c.Permissions
				}
				// The helper owns the pin store; re-read it for every hello.
				if ps, err := pins.Open(o.PinsPath); err == nil {
					h.PinnedClientKeys = ps.List()
				}
				return h
			},
			OnHelloAck: func(a proto.HelloAck) {
				p.SetServerFeatures(a.Features)
				p.SetRevoked(a.RevokedClientKeys)
				p.SetDevice(a.Device)
				upd.HelloAcked()
				if a.Update.Available {
					go upd.CheckNow(ctx, update.Trigger{})
				}
			},
			OnUpdateCheck: func(m proto.UpdateCheck) {
				go upd.CheckNow(ctx, update.Trigger{Explicit: m.Explicit, Force: m.Force})
			},
			// The service installs the driver; the helper reports what the desktop shows.
			OnVirtualDisplay: func(m proto.VirtualDisplayRequest) { go vdJobs.run(m.Action, p, o.Log) },
			// The helper in the console session takes the picture.
			OnPreviewRequest: func(raw []byte) { p.HandleSignal(proto.TypePreviewRequest, raw) },
			OnDeviceUpdate: func(u proto.DeviceUpdate) {
				p.SetDevice(proto.DeviceInfo{ID: st.DeviceID, Name: u.Name, AutoAccept: u.AutoAccept})
			},
			OnKeyRevoked: func(k proto.KeyRevoked) { p.Revoke(k.PublicKey) },
			OnStatus: func(s signal.Status) {
				status := "Online"
				switch {
				case s.Fatal != "":
					status = "Refused by server: " + s.Fatal
				case !s.Online && s.LastError != "":
					status = "Reconnecting…"
				case !s.Online:
					status = "Connecting…"
				}
				p.SetStatus(ServiceStatus{Enrolled: true, Online: s.Online, Status: status})
			},
		})
		p.Send = s.Send
		scMu.Lock()
		sc = s
		scMu.Unlock()
		go upd.Run(ctx)
		return s.Run(ctx)
	}
}

// ensureEnrolled returns the persisted state and device key, enrolling with
// the registry token (msiexec ENROLL_TOKEN=...) or RC_ENROLL_TOKEN when not
// enrolled yet. Without a token it waits, polling the registry.
func ensureEnrolled(ctx context.Context, o *Options) (config.State, ed25519.PrivateKey, error) {
	ks := identity.Default(o.Settings.DataDir)
	lastReason := ""
	for {
		st, err := config.Load(o.Settings.DataDir)
		if err != nil {
			return st, nil, err
		}
		if st.Enrolled() {
			key, err := ks.Load()
			if err != nil {
				return st, nil, fmt.Errorf("device key: %w", err)
			}
			if st.ServerURL == "" {
				st.ServerURL = serverURL(o)
			}
			return st, key, nil
		}
		token := o.Settings.EnrollToken
		fromRegistry := false
		if token == "" {
			token = regString("EnrollToken")
			fromRegistry = token != ""
		}
		url := serverURL(o)
		if reason := enrollWaitReason(token, url); reason != lastReason {
			lastReason = reason
			if reason != "" {
				o.Log.Warn("winsvc: " + reason)
			}
		}
		if token != "" && url != "" {
			st, key, err := enroll(ctx, o, ks, url, token)
			if err == nil || api.IsCode(err, "not_found", "bad_request", "conflict", "forbidden") {
				// Single-use token: drop it whether it worked or was rejected.
				if fromRegistry {
					regDelete("EnrollToken")
				}
				o.Settings.EnrollToken = ""
			}
			if err == nil {
				return st, key, nil
			}
			o.Log.Error("winsvc: enrollment failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return config.State{}, nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func enroll(ctx context.Context, o *Options, ks identity.Keystore, url, token string) (config.State, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return config.State{}, nil, err
	}
	hostname := o.Settings.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	res, err := api.New(url).Enroll(ctx, api.EnrollRequest{
		Token:     token,
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Hostname:  hostname,
		OS:        runtime.GOOS,
		Arch:      buildinfo.Arch,
		Version:   o.Version,
	})
	if err != nil {
		return config.State{}, nil, err
	}
	if err := ks.Save(priv); err != nil {
		return config.State{}, nil, err
	}
	st := config.State{ServerURL: url, DeviceID: res.DeviceID, Name: res.Name}
	if err := config.Save(o.Settings.DataDir, st); err != nil {
		return config.State{}, nil, err
	}
	o.Log.Info("winsvc: enrolled", "device_id", res.DeviceID)
	return st, priv, nil
}

func serverURL(o *Options) string {
	if o.Settings.ServerURL != "" {
		return o.Settings.ServerURL
	}
	return regString("ServerURL")
}

func regString(name string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return v
}

func regDelete(name string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, regKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	k.DeleteValue(name)
}

// ---------------------------------------------------------------- helper

// waitForDeviceKey blocks until the service has enrolled and stored the
// device key, instead of exiting and being respawned in a loop.
func waitForDeviceKey(ctx context.Context, ks identity.Keystore, log *slog.Logger) (ed25519.PrivateKey, error) {
	logged := false
	for {
		key, err := ks.Load()
		if !errors.Is(err, identity.ErrNotFound) {
			return key, err
		}
		if !logged {
			log.Info("winsvc: helper waiting for enrollment")
			logged = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// RunHelper is `remote-agent.exe helper`: it runs in the console session as
// SYSTEM, connects to the service's pipes and runs the real session.Manager
// with the native backend, plus the tray. It returns when the service asks
// it to stop, the pipes break, or the owner picks Quit.
func RunHelper(o Options) error {
	o.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, w, err := dialService(ctx)
	if err != nil {
		return fmt.Errorf("connect to service: %w", err)
	}
	defer r.Close()
	defer w.Close()
	link := NewLink(w)

	backend := o.NewBackend()
	if err := backend.Init(); err != nil {
		return fmt.Errorf("native backend: %w", err)
	}
	defer backend.Shutdown()

	key, err := waitForDeviceKey(ctx, identity.Default(o.Settings.DataDir), o.Log)
	if err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	ps, err := pins.Open(o.PinsPath)
	if err != nil {
		return fmt.Errorf("pins: %w", err)
	}

	var autoAccept, paused, askNew, noPreviews atomic.Bool
	prefs, err := config.LoadPrefs(o.Settings.DataDir)
	if err != nil {
		o.Log.Warn("winsvc: prefs unreadable, using defaults", "err", err)
	}
	askNew.Store(prefs.AskNewBrowsers)
	noPreviews.Store(prefs.NoScreenPreviews)
	var trayUpdate atomic.Pointer[func(tray.State)]
	var trayMu sync.Mutex
	trayState := tray.State{Enrolled: true, Status: "Connecting…", AskNew: prefs.AskNewBrowsers, Previews: !prefs.NoScreenPreviews}
	pushTray := func(f func(*tray.State)) {
		trayMu.Lock()
		f(&trayState)
		s := trayState
		trayMu.Unlock()
		if u := trayUpdate.Load(); u != nil {
			(*u)(s)
		}
	}

	// File transfers run as the user signed in to this console session and
	// show in the tray tooltip and menu.
	var sid uint32
	_ = windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid)
	fileFeed := tray.NewActivityFeed(func(line string) { pushTray(func(s *tray.State) { s.Activity = line }) }, tray.Notify)

	var mgr *session.Manager
	mgr, err = session.NewManager(session.Options{
		Backend:        backend,
		DeviceKey:      key,
		Pins:           ps,
		Send:           link.Send,
		AutoAccept:     autoAccept.Load,
		AskNewBrowsers: askNew.Load,
		Paused:         paused.Load,
		OnPinsChanged: func() {
			link.Send(proto.TypePinsSync, proto.PinsSync{PinnedClientKeys: ps.List()})
		},
		UnlockOS:      "windows",
		Restart:       RestartDevice,
		RestartAgent:  StartServiceRestart,
		DisplayMarker: filepath.Join(o.Settings.DataDir, session.DisplayMarkerName),
		SecureAttention: func() {
			if err := link.SAS(); err != nil {
				o.Log.Warn("winsvc: Ctrl+Alt+Delete not passed to the service", "err", err)
			}
		},
		OnSessionChange: func(i *session.Info) {
			if mgr != nil {
				link.Active(mgr.ActiveSessions())
			}
			pushTray(func(s *tray.State) {
				s.InSession = i != nil && i.State == "active"
				s.Peer = ""
				if i != nil {
					s.Peer = fmt.Sprintf("%s (%s)", i.Label, i.Email)
				}
			})
		},
		UDPPort:        o.Settings.UDPPort,
		NAT1To1IPs:     o.Settings.NAT1To1IPs,
		AgentVersion:   o.Version,
		Files:          UserFS{SessionID: sid},
		FileDeny:       []string{o.Settings.DataDir, o.PinsPath},
		OnFileActivity: fileFeed.Add,
		Log:            o.Log,
	})
	if err != nil {
		return err
	}
	defer mgr.Close()

	if err := link.Caps(helperCaps(backend, key)); err != nil {
		return err
	}

	// The helper runs in the console session, where the console user, idle
	// time and lock screen are visible; the service drops these reports when
	// the server does not take them.
	collector := &sysinfo.Collector{Locked: sysinfo.LockScreenShown, VirtualDisplay: vdd.Info}
	if w, ok := backend.(interface{ Memory() uint64 }); ok { // the capture worker
		collector.CaptureMemory = w.Memory
	}
	// A headless device's virtual monitor stays its only, primary screen.
	go vdd.Keep(ctx, o.Log)
	info := sysinfo.NewReporter(collector.Collect,
		func(h proto.HostInfo) error { return link.Send(proto.TypeDeviceInfo, proto.HostInfoReport{Info: h}) },
		func() bool { return true })
	go info.Run(ctx)

	previews := preview.New(preview.Options{Backend: backend, Pins: ps, DeviceKey: key, Send: link.Send, Revoked: mgr.IsRevoked,
		DeviceID: func() string {
			st, _ := config.Load(o.Settings.DataDir)
			return st.DeviceID
		},
		State: func() preview.DeviceState {
			return preview.DeviceState{PreviewsOff: noPreviews.Load(), Paused: paused.Load(), AskEveryTime: !autoAccept.Load(),
				ScreenPermission: backend.Permissions().Screen, InSession: mgr.Current() != nil}
		},
		OnServed: func(label string, at time.Time) {
			pushTray(func(s *tray.State) {
				s.Preview = "Screen preview sent to " + label + " at " + at.Local().Format("15:04")
			})
			time.AfterFunc(5*time.Minute, func() {
				pushTray(func(s *tray.State) {
					if time.Since(at) >= 5*time.Minute {
						s.Preview = ""
					}
				})
			})
		},
		Log: o.Log})

	served := make(chan error, 1)
	go func() {
		served <- Serve(r, PreviewRouter{HelperTarget: mgr, Previews: previews}, HelperEvents{
			// Sent on every hello_ack: a new connection gets a report right away.
			OnDevice: func(d proto.DeviceInfo) {
				autoAccept.Store(d.AutoAccept)
				info.Kick()
			},
			OnStatus: func(s ServiceStatus) {
				pushTray(func(t *tray.State) {
					t.Enrolled, t.Online, t.Status, t.Update = s.Enrolled, s.Online, s.Status, s.Update
				})
			},
		})
	}()

	// The tray owns this (locked) OS thread until the process exits.
	exit := func(code int) {
		mgr.EndAll(proto.EndAgentKill)
		mgr.Close()
		backend.Shutdown()
		os.Exit(code)
	}
	go func() {
		err := <-served
		if err != nil && !errors.Is(err, os.ErrClosed) {
			o.Log.Info("winsvc: service link closed", "err", err)
		}
		exit(0)
	}()
	runtime.LockOSThread()
	tray.Run(tray.Hooks{
		OnEndSession: func() { mgr.EndAll(proto.EndAgentKill) },
		OnPause: func(p bool) {
			paused.Store(p)
			pushTray(func(s *tray.State) { s.Paused = p })
		},
		OnAskNew: func(ask bool) {
			askNew.Store(ask)
			if err := config.UpdatePrefs(o.Settings.DataDir, func(p *config.Prefs) { p.AskNewBrowsers = ask }); err != nil {
				o.Log.Error("winsvc: saving prefs failed", "err", err)
			}
			pushTray(func(s *tray.State) { s.AskNew = ask })
		},
		OnPreviews: func(on bool) {
			noPreviews.Store(!on)
			if err := config.UpdatePrefs(o.Settings.DataDir, func(p *config.Prefs) { p.NoScreenPreviews = !on }); err != nil {
				o.Log.Error("winsvc: saving prefs failed", "err", err)
			}
			pushTray(func(s *tray.State) { s.Previews = on })
		},
		OnQuit: func() {
			link.Quit()
			exit(0)
		},
	}, func(update func(tray.State)) {
		trayUpdate.Store(&update)
		pushTray(func(*tray.State) {})
	})
	// tray.Run can return without Quit when there is no shell to host the icon
	// (logon screen); keep serving sessions until the service link closes.
	select {}
}

func helperCaps(b platform.Backend, key ed25519.PrivateKey) HelperCaps {
	c := HelperCaps{Capabilities: proto.Capabilities{Codecs: []string{"h264"}, MaxFPS: session.EncoderMaxFPS(b.EncoderName()), Encoder: b.EncoderName(),
		Features: []string{proto.FeatureDeviceInfo, proto.FeaturePreview}},
		DeviceKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}
	if mons, err := b.Monitors(); err == nil {
		for _, m := range mons {
			c.Capabilities.Monitors = append(c.Capabilities.Monitors, proto.Monitor{
				ID: m.ID, Name: m.Name, X: m.X, Y: m.Y, Width: m.Width, Height: m.Height, Scale: m.Scale, Primary: m.Primary,
			})
		}
	}
	perm := b.Permissions()
	c.Permissions = proto.Permissions{Screen: perm.Screen, Input: perm.Input}
	var sid uint32
	if windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid) == nil {
		c.SessionID = sid
	}
	return c
}

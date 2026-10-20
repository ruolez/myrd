package winsvc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/ruolez/remotectl/proto"
)

// ServiceName is the Windows service name; the MSI installs the same name.
const ServiceName = "RemoteAgent"

// Core is the service's agent loop: enrollment, the signaling socket and
// update checks, with p as its session.Handler. It returns when ctx ends or
// the device is permanently refused by the server.
type Core func(ctx context.Context, p *Proxy) error

// IsService reports whether the process was started by the SCM.
func IsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// RunService runs as the RemoteAgent service until the SCM stops it.
func RunService(core Core, log *slog.Logger) error {
	return svc.Run(ServiceName, &handler{core: core, log: log})
}

type handler struct {
	core Core
	log  *slog.Logger
}

func (h *handler) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptSessionChange
	status <- svc.Status{State: svc.StartPending}

	exe, err := os.Executable()
	if err != nil {
		h.log.Error("winsvc: executable path", "err", err)
		return true, 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	quit := make(chan struct{})
	var quitOnce sync.Once
	p := &Proxy{Log: h.log, OnQuit: func() { quitOnce.Do(func() { close(quit) }) }}
	kick := make(chan struct{}, 1)

	var wg sync.WaitGroup
	wg.Add(2)
	coreErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		superviseHelper(ctx, p, exe, kick, h.log)
	}()
	go func() {
		defer wg.Done()
		coreErr <- h.core(ctx, p)
	}()

	status <- svc.Status{State: svc.Running, Accepts: accepts}
	exitCode := uint32(0)
loop:
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				break loop
			case svc.SessionChange:
				select {
				case kick <- struct{}{}:
				default:
				}
			}
		case <-quit:
			h.log.Info("winsvc: quit requested from the tray")
			break loop
		case err := <-coreErr:
			if err != nil {
				// Device removed/disabled: stop for good (a clean exit, so the
				// SCM recovery actions do not restart us in a loop).
				h.log.Error("winsvc: agent stopped", "err", err)
			}
			break loop
		}
	}

	status <- svc.Status{State: svc.StopPending}
	p.EndAll(proto.EndAgentKill)
	cancel()
	waitTimeout(&wg, 10*time.Second)
	return false, exitCode
}

func waitTimeout(wg *sync.WaitGroup, d time.Duration) {
	ch := make(chan struct{})
	go func() { wg.Wait(); close(ch) }()
	select {
	case <-ch:
	case <-time.After(d):
	}
}

// superviseHelper keeps one helper alive in the active console session,
// moving it when the console session changes and restarting it with backoff
// when it crashes.
func superviseHelper(ctx context.Context, p *Proxy, exe string, kick <-chan struct{}, log *slog.Logger) {
	backoff := time.Second
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-kick:
			return true
		case <-time.After(d):
			return true
		}
	}
	for ctx.Err() == nil {
		sid, ok := activeConsoleSession()
		if !ok {
			if !sleep(2 * time.Second) {
				return
			}
			continue
		}
		started := time.Now()
		if err := runHelperOnce(ctx, p, exe, sid, kick, log); err != nil {
			log.Warn("winsvc: helper", "session", sid, "err", err)
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
		if !sleep(backoff) {
			return
		}
	}
}

func runHelperOnce(ctx context.Context, p *Proxy, exe string, sid uint32, kick <-chan struct{}, log *slog.Logger) error {
	srv, err := newPipeServer()
	if err != nil {
		return fmt.Errorf("create pipes: %w", err)
	}
	proc, pid, err := spawnInSession(exe, []string{"helper"}, sid)
	if err != nil {
		srv.close()
		return fmt.Errorf("spawn: %w", err)
	}
	defer windows.CloseHandle(proc)
	exited := waitProcess(proc)
	log.Info("winsvc: helper started", "pid", pid, "session", sid)

	w, r, err := srv.accept(ctx, 15*time.Second)
	if err != nil {
		windows.TerminateProcess(proc, 1)
		return fmt.Errorf("accept: %w", err)
	}
	detached := make(chan error, 1)
	go func() { detached <- p.Attach(r, w) }()

	stop := func() {
		p.Shutdown()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			windows.TerminateProcess(proc, 1)
			<-exited
		}
	}
	defer func() {
		w.Close()
		r.Close()
		<-detached
	}()
	for {
		select {
		case <-ctx.Done():
			stop()
			return nil
		case <-kick:
			if cur, ok := activeConsoleSession(); !ok || cur != sid {
				log.Info("winsvc: console session changed, moving helper", "from", sid, "to", cur)
				stop()
				return nil
			}
		case <-exited:
			var code uint32
			windows.GetExitCodeProcess(proc, &code)
			return fmt.Errorf("helper exited with code %d", code)
		case err := <-detached:
			detached <- err // for the deferred receive
			stop()
			return fmt.Errorf("pipe closed: %v", err)
		}
	}
}

// Install registers the service (auto start, restart on failure) and starts it.
func Install(exe string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(ServiceName); err == nil {
		s.Close()
		return fmt.Errorf("service %s already installed", ServiceName)
	}
	s, err := m.CreateService(ServiceName, exe, mgr.Config{
		DisplayName: "RemoteAgent",
		Description: "Remote control agent: keeps this device reachable for its owner and shows a visible indicator during sessions.",
		StartType:   mgr.StartAutomatic,
	}, "service")
	if err != nil {
		return err
	}
	defer s.Close()
	restart := mgr.RecoveryAction{Type: mgr.ServiceRestart, Delay: 5 * time.Second}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{restart, restart, restart}, 86400); err != nil {
		return err
	}
	return s.Start()
}

// Uninstall stops and deletes the service.
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(ServiceName)
	if err != nil {
		return err
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		s.Control(svc.Stop)
		for i := 0; i < 50; i++ {
			if st, err := s.Query(); err != nil || st.State == svc.Stopped {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	return s.Delete()
}

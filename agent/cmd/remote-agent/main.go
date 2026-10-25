// Command remote-agent is the remotectl device agent.
//
//	remote-agent [run]                 run the agent (default)
//	remote-agent enroll --token XXXX-XXXX [--server URL]
//	remote-agent status
//	remote-agent selftest              capture ~3 s and inject input; prints a report
//	remote-agent uninstall
//	remote-agent version
//	remote-agent capture-worker        spawned by the agent per session: capture and encoding
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ruolez/remotectl/agent/internal/agent"
	"github.com/ruolez/remotectl/agent/internal/buildinfo"
	"github.com/ruolez/remotectl/agent/internal/config"
	"github.com/ruolez/remotectl/agent/internal/identity"
	"github.com/ruolez/remotectl/agent/internal/tray"
)

// Set with -ldflags "-X main.version=…".
var version = "dev"

// Cocoa (tray, overlay, prompts) needs the process main thread.
func init() { runtime.LockOSThread() }

func main() {
	if len(os.Args) > 1 && os.Args[1] == "capture-worker" { // before any log file is opened
		os.Exit(cmdCaptureWorker(os.Args[2:]))
	}
	if platformMain(os.Args[1:]) {
		return
	}
	cmd, args := "run", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	log := newLogger()
	var err error
	switch cmd {
	case "run":
		err = cmdRun(log, args)
	case "enroll":
		err = cmdEnroll(log, args)
	case "status":
		err = cmdStatus(args)
	case "selftest":
		err = cmdSelftest(args)
	case "uninstall":
		err = cmdUninstall(log, args)
	case "version":
		fmt.Printf("remote-agent %s (%s/%s)\n", version, runtime.GOOS, buildinfo.Arch)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\nusage: remote-agent [run|enroll|status|selftest|uninstall|version]\n", cmd)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newLogger() *slog.Logger {
	lvl := slog.LevelInfo
	if os.Getenv("RC_LOG_LEVEL") == "debug" {
		lvl = slog.LevelDebug
	}
	var w io.Writer = os.Stderr
	if path := defaultLogFile(); path != "" {
		if f, err := openLogFile(path, maxLogSize); err == nil {
			w = io.MultiWriter(os.Stderr, f) // closed at process exit
			// launchd discards stderr: keep panic traces in the log.
			if lf, ok := f.(*os.File); ok {
				_ = debug.SetCrashOutput(lf, debug.CrashOptions{})
			}
		}
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
}

// settings merges RC_* env with command-line flags (flags win).
func settings(fs *flag.FlagSet, args []string) (config.Settings, error) {
	s := config.FromEnv()
	fs.StringVar(&s.ServerURL, "server", s.ServerURL, "control plane URL (RC_SERVER_URL)")
	fs.StringVar(&s.DataDir, "data-dir", s.DataDir, "state directory (RC_DATA_DIR)")
	fs.StringVar(&s.EnrollToken, "token", s.EnrollToken, "enrollment token XXXX-XXXX (RC_ENROLL_TOKEN)")
	fs.StringVar(&s.ConsoleAddr, "console", s.ConsoleAddr, "device console listen address, e.g. :5562 (RC_CONSOLE_ADDR)")
	fs.StringVar(&s.Hostname, "hostname", s.Hostname, "hostname reported to the server (RC_HOSTNAME)")
	udp := fs.String("udp-port", strconv.Itoa(s.UDPPort), "single ICE UDP port, 0 = ephemeral (RC_UDP_PORT)")
	nat := fs.String("nat1to1", strings.Join(s.NAT1To1IPs, ","), "extra host candidate IPs (RC_NAT1TO1_IPS)")
	if err := fs.Parse(args); err != nil {
		return s, err
	}
	s.UDPPort, _ = strconv.Atoi(*udp)
	s.NAT1To1IPs = nil
	for _, ip := range strings.Split(*nat, ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			s.NAT1To1IPs = append(s.NAT1To1IPs, ip)
		}
	}
	return s, nil
}

func newAgent(log *slog.Logger, s config.Settings) (*agent.Agent, error) {
	return agent.New(agent.Config{Settings: s, Version: version,
		Backend: captureBackend(log), Keystore: identity.Default(s.DataDir), Log: log})
}

func cmdRun(log *slog.Logger, args []string) error {
	s, err := settings(flag.NewFlagSet("run", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	release, err := singleInstance(s.DataDir)
	if errors.Is(err, errAlreadyRunning) && os.Getenv("XPC_SERVICE_NAME") == "" {
		log.Info("not starting: " + err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	defer release()
	a, err := newAgent(log, s)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	errc := make(chan error, 1)
	quit := func() { cancel() }
	// tray.Run owns the main thread on macOS/Windows; on Linux it just blocks.
	go func() {
		<-ctx.Done()
		select {
		case err := <-errc:
			exit(log, err)
		case <-time.After(5 * time.Second):
			exit(log, nil)
		}
	}()
	tray.Run(a.TrayHooks(quit), func(update func(tray.State)) {
		a.SetTrayUpdater(update)
		err := a.Run(ctx)
		errc <- err
		cancel()
	})
	return nil
}

func exit(log *slog.Logger, err error) {
	if err != nil && err != context.Canceled {
		log.Error("agent stopped", "err", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func cmdEnroll(log *slog.Logger, args []string) error {
	s, err := settings(flag.NewFlagSet("enroll", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	if s.EnrollToken == "" {
		return fmt.Errorf("--token is required")
	}
	a, err := newAgent(log, s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Enroll(ctx, s.ServerURL, s.EnrollToken); err != nil {
		return err
	}
	st := a.State()
	fmt.Printf("enrolled as %s (%s) on %s\n", st.Name, st.DeviceID, st.ServerURL)
	return nil
}

func cmdStatus(args []string) error {
	s, err := settings(flag.NewFlagSet("status", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	st, err := config.Load(s.DataDir)
	if err != nil {
		return err
	}
	_, keyErr := identity.Default(s.DataDir).Load()
	fmt.Printf("data dir:   %s\nenrolled:   %v\ndevice id:  %s\nname:       %s\nserver:     %s\ndevice key: %v\nversion:    %s\n",
		s.DataDir, st.Enrolled(), st.DeviceID, st.Name, st.ServerURL, keyErr == nil, version)
	return nil
}

func cmdUninstall(log *slog.Logger, args []string) error {
	s, err := settings(flag.NewFlagSet("uninstall", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	a, err := newAgent(log, s)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Uninstall(ctx); err != nil {
		return err
	}
	fmt.Println("device removed and local state wiped")
	return nil
}

//go:build windows

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ruolez/remotectl/agent/internal/config"
	"github.com/ruolez/remotectl/agent/internal/native"
	"github.com/ruolez/remotectl/agent/internal/winsvc"
)

// platformMain adds the Windows process model:
//
//	remote-agent service            run as the RemoteAgent service (the SCM passes this)
//	remote-agent helper             session helper, spawned by the service into the console session
//	remote-agent install-service    register + start the service (admin; the MSI does this normally)
//	remote-agent uninstall-service  stop + delete the service
//
// Anything else (run, enroll, status, selftest, uninstall, version) falls
// through to the single-process commands in main.go.
func platformMain(args []string) bool {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "service":
		os.Exit(runService())
	case "helper":
		os.Exit(runHelper())
	case "install-service":
		exe, err := os.Executable()
		if err == nil {
			err = winsvc.Install(exe)
		}
		os.Exit(report("install-service", err))
	case "uninstall-service":
		os.Exit(report("uninstall-service", winsvc.Uninstall()))
	case "":
		if winsvc.IsService() {
			os.Exit(runService())
		}
	}
	return false
}

func report(what string, err error) int {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		return 1
	}
	fmt.Printf("%s: ok\n", what)
	return 0
}

func windowsOptions(logName string) (winsvc.Options, func()) {
	s := config.FromEnv()
	log, closeLog := fileLogger(s.DataDir, logName)
	native.SetLogger(log)
	return winsvc.Options{Settings: s, Version: version, Log: log, NewBackend: native.New}, closeLog
}

func runService() int {
	o, closeLog := windowsOptions("service.log")
	defer closeLog()
	if err := winsvc.RunService(winsvc.ServiceCore(o), o.Log); err != nil {
		o.Log.Error("service failed", "err", err)
		return 1
	}
	return 0
}

func runHelper() int {
	o, closeLog := windowsOptions("helper.log")
	defer closeLog()
	if err := winsvc.RunHelper(o); err != nil {
		o.Log.Error("helper failed", "err", err)
		return 1
	}
	return 0
}

// fileLogger writes to <data dir>\<name> (services have no console); the
// file is restarted when it grows past 10 MB.
func fileLogger(dataDir, name string) (*slog.Logger, func()) {
	lvl := slog.LevelInfo
	if os.Getenv("RC_LOG_LEVEL") == "debug" {
		lvl = slog.LevelDebug
	}
	var w io.Writer = os.Stderr
	closeFn := func() {}
	if f, err := openLogFile(filepath.Join(dataDir, name), maxLogSize); err == nil {
		w = f
		closeFn = func() { f.Close() }
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), closeFn
}

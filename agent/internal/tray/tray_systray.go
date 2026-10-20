//go:build darwin || windows

package tray

import (
	"runtime"
	"sync"

	"fyne.io/systray"
)

// ProductName shows in the menu and tooltip; the product name is still an
// open decision, so it is one variable.
var ProductName = "remotectl"

func iconBytes(k iconKind) []byte {
	if runtime.GOOS == "windows" {
		return iconICO(k)
	}
	return iconPNG(k)
}

func setIcon(k iconKind) {
	b := iconBytes(k)
	if k == iconIdle {
		systray.SetTemplateIcon(b, b) // macOS recolours it for light/dark menu bars
		return
	}
	systray.SetIcon(b)
}

func run(h Hooks, ready func(update func(State))) {
	systray.Run(func() { onReady(h, ready) }, func() {})
}

func onReady(h Hooks, ready func(update func(State))) {
	setIcon(iconOffline)
	systray.SetTooltip(ProductName)

	status := systray.AddMenuItem(ProductName+": starting…", "")
	status.Disable()
	peer := systray.AddMenuItem("", "")
	peer.Disable()
	peer.Hide()
	systray.AddSeparator()
	pause := systray.AddMenuItemCheckbox("Pause new sessions", "Refuse incoming sessions until unpaused", false)
	end := systray.AddMenuItem("End session", "Disconnect the controlling browser now")
	end.Disable()
	systray.AddSeparator()
	var uninstall *systray.MenuItem
	if h.OnUninstall != nil {
		uninstall = systray.AddMenuItem("Uninstall "+ProductName+"…", "Remove the agent from this computer")
	}
	quit := systray.AddMenuItem("Quit", "Stop the agent until next login")

	var mu sync.Mutex
	last := State{}
	lastIcon := iconOffline
	first := true
	update := func(s State) {
		mu.Lock()
		defer mu.Unlock()
		if !first && s == last {
			return
		}
		first = false
		last = s

		k := iconOffline
		switch {
		case s.InSession:
			k = iconInSession
		case s.Online:
			k = iconIdle
		}
		if k != lastIcon {
			setIcon(k)
			lastIcon = k
		}

		line := s.Status
		if line == "" {
			switch {
			case !s.Enrolled:
				line = "Waiting for enrollment"
			case s.InSession:
				line = "Session active"
			case s.Online:
				line = "Online"
			default:
				line = "Offline — reconnecting"
			}
		}
		if s.Paused && !s.InSession {
			line += " (paused)"
		}
		status.SetTitle(ProductName + ": " + line)
		systray.SetTooltip(ProductName + " — " + line)

		if s.InSession && s.Peer != "" {
			peer.SetTitle("Controlled by " + s.Peer)
			peer.Show()
		} else {
			peer.Hide()
		}
		if s.InSession {
			end.Enable()
		} else {
			end.Disable()
		}
		if s.Paused {
			pause.Check()
		} else {
			pause.Uncheck()
		}
	}

	go func() {
		var uninstallCh chan struct{}
		if uninstall != nil {
			uninstallCh = uninstall.ClickedCh
		}
		for {
			select {
			case <-pause.ClickedCh:
				mu.Lock()
				paused := !last.Paused
				mu.Unlock()
				if paused {
					pause.Check()
				} else {
					pause.Uncheck()
				}
				if h.OnPause != nil {
					h.OnPause(paused)
				}
			case <-end.ClickedCh:
				if h.OnEndSession != nil {
					h.OnEndSession()
				}
			case <-uninstallCh:
				h.OnUninstall()
			case <-quit.ClickedCh:
				if h.OnQuit != nil {
					h.OnQuit()
				}
				systray.Quit()
				return
			}
		}
	}()

	go ready(update)
}

//go:build darwin || windows

package tray

import (
	"runtime"
	"sync"

	"fyne.io/systray"
)

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

// truncTip keeps the tooltip within Windows' 128-character limit.
func truncTip(s string) string {
	r := []rune(s)
	if len(r) <= 127 {
		return s
	}
	return string(r[:126]) + "…"
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
	activity := systray.AddMenuItem("", "")
	activity.Disable()
	activity.Hide()
	updating := systray.AddMenuItem("", "")
	updating.Disable()
	updating.Hide()
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
		tip := ProductName + " — " + line
		if s.Activity != "" {
			activity.SetTitle(s.Activity)
			activity.Show()
			tip += "\n" + s.Activity
		} else {
			activity.Hide()
		}
		if s.Update != "" {
			updating.SetTitle(s.Update)
			updating.Show()
			tip += "\n" + s.Update
		} else {
			updating.Hide()
		}
		systray.SetTooltip(truncTip(tip))

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

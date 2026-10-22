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
	previewLine := systray.AddMenuItem("", "")
	previewLine.Disable()
	previewLine.Hide()
	updating.Hide()
	var enroll *systray.MenuItem
	if h.OnEnroll != nil {
		enroll = systray.AddMenuItem("Enroll this device…", "Enter the enrollment token from the web app")
		enroll.Hide()
	}
	systray.AddSeparator()
	pause := systray.AddMenuItemCheckbox("Pause new sessions", "Refuse incoming sessions until unpaused", false)
	var askNew *systray.MenuItem
	if h.OnAskNew != nil {
		askNew = systray.AddMenuItemCheckbox("Ask before new browsers connect",
			"Show a prompt on this computer before a browser it has not approved connects. Off: the first connection from your account is allowed and remembered.", false)
	}
	var previews *systray.MenuItem
	if h.OnPreviews != nil {
		previews = systray.AddMenuItemCheckbox("Show screen previews",
			"Send a small picture of this screen about once a minute to your dashboard while it is open. It is encrypted for your approved browsers only.", false)
	}
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
		if s.Preview != "" {
			previewLine.SetTitle(s.Preview)
			previewLine.Show()
		} else {
			previewLine.Hide()
		}
		systray.SetTooltip(truncTip(tip))

		if enroll != nil {
			if s.Enrolled {
				enroll.Hide()
			} else {
				enroll.Show()
			}
		}
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
		if askNew != nil {
			if s.AskNew {
				askNew.Check()
			} else {
				askNew.Uncheck()
			}
		}
		if previews != nil {
			if s.Previews {
				previews.Check()
			} else {
				previews.Uncheck()
			}
		}
	}

	go func() {
		var uninstallCh, enrollCh, askNewCh, previewsCh chan struct{}
		if askNew != nil {
			askNewCh = askNew.ClickedCh
		}
		if previews != nil {
			previewsCh = previews.ClickedCh
		}
		if uninstall != nil {
			uninstallCh = uninstall.ClickedCh
		}
		if enroll != nil {
			enrollCh = enroll.ClickedCh
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
			case <-askNewCh:
				mu.Lock()
				ask := !last.AskNew
				mu.Unlock()
				if ask {
					askNew.Check()
				} else {
					askNew.Uncheck()
				}
				h.OnAskNew(ask)
			case <-previewsCh:
				mu.Lock()
				on := !last.Previews
				mu.Unlock()
				if on {
					previews.Check()
				} else {
					previews.Uncheck()
				}
				h.OnPreviews(on)
			case <-end.ClickedCh:
				if h.OnEndSession != nil {
					h.OnEndSession()
				}
			case <-uninstallCh:
				h.OnUninstall()
			case <-enrollCh:
				h.OnEnroll()
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

// Package tray is the device-local status surface: a menu-bar / system-tray
// icon that turns red during a session, with Pause, Ask before new browsers
// connect, End session (kill switch) and Quit. On the headless Linux demo device it is a no-op and the device
// console (internal/devconsole) plays this role.
package tray

// ProductName shows in the menu, tooltip and notifications.
var ProductName = "my RemoteDesktop"

type State struct {
	Enrolled  bool
	Online    bool
	InSession bool
	Paused    bool   // new sessions refused while paused
	AskNew    bool   // "Ask before new browsers connect" (config.Prefs.AskNewBrowsers)
	Peer      string // "<label> (<email>)" of the controlling browser
	Status    string // one-line human status, e.g. "Online", "Waiting for enrollment"
	Activity  string // file transfer line, e.g. "Receiving report.pdf (42%)"; see ActivityFeed
	Update    string // agent update line, e.g. "Updating to 0.3.1…"; see UpdateLine
}

type Hooks struct {
	OnEndSession func()            // kill switch: end the active session now
	OnPause      func(paused bool) // refuse new sessions while paused
	OnAskNew     func(ask bool)    // "Ask before new browsers connect"; nil hides it
	OnQuit       func()
	OnUninstall  func() // macOS "Uninstall RemoteAgent" menu item; nil hides it
	OnEnroll     func() // "Enroll this device…" while not enrolled; nil hides it
}

// Run must be called from the main goroutine (Cocoa needs the main thread).
// It calls ready once the tray exists, handing back an update function, and
// blocks until Quit. ready runs on its own goroutine.
func Run(h Hooks, ready func(update func(State))) { run(h, ready) }

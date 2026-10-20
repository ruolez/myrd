// Package tray is the device-local status surface: a menu-bar / system-tray
// icon that turns red during a session, with Pause, End session (kill switch)
// and Quit. On the headless Linux demo device it is a no-op and the device
// console (internal/devconsole) plays this role.
package tray

type State struct {
	Enrolled  bool
	Online    bool
	InSession bool
	Paused    bool   // new sessions refused while paused
	Peer      string // "<label> (<email>)" of the controlling browser
	Status    string // one-line human status, e.g. "Online", "Waiting for enrollment"
}

type Hooks struct {
	OnEndSession func()            // kill switch: end the active session now
	OnPause      func(paused bool) // refuse new sessions while paused
	OnQuit       func()
	OnUninstall  func() // macOS "Uninstall RemoteAgent" menu item; nil hides it
}

// Run must be called from the main goroutine (Cocoa needs the main thread).
// It calls ready once the tray exists, handing back an update function, and
// blocks until Quit. ready runs on its own goroutine.
func Run(h Hooks, ready func(update func(State))) { run(h, ready) }

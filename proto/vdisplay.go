package proto

// Virtual display (docs/PROTOCOL.md §3, device.info): a Windows device without a
// monitor attached gets a virtual one, so its screen can be any resolution.
// The driver (Virtual Display Driver, MIT, signed by the SignPath
// Foundation) ships inside the agent; the server only asks for it to be
// installed or removed, and the device reports the state in device.info.

// TypeVirtualDisplay is S→A: install or remove the virtual display driver.
const TypeVirtualDisplay = "virtual_display"

// VirtualDisplayRequest.Action values.
const (
	VirtualDisplayInstall = "install"
	VirtualDisplayRemove  = "remove"
)

// VirtualDisplayInfo.Driver values.
const (
	VDriverAbsent     = "absent"
	VDriverInstalling = "installing"
	VDriverInstalled  = "installed"
	VDriverRemoving   = "removing"
	VDriverFailed     = "failed"
)

// VDriverStates lists every valid VirtualDisplayInfo.Driver value.
var VDriverStates = []string{VDriverAbsent, VDriverInstalling, VDriverInstalled, VDriverRemoving, VDriverFailed}

type VirtualDisplayRequest struct {
	V      int    `json:"v"`
	Type   string `json:"type"`
	Action string `json:"action"`
}

// VirtualDisplayInfo is HostInfo.VirtualDisplay, reported by Windows agents.
type VirtualDisplayInfo struct {
	// Supported: the driver can run here (64-bit Windows 10 1803 / Server
	// 2019 or later).
	Supported bool `json:"supported"`
	// Headless: no monitor is attached (only Windows' generic one).
	Headless bool `json:"headless"`
	// Driver is one of VDriverStates.
	Driver string `json:"driver"`
	// Active: the virtual monitor is on the desktop.
	Active bool `json:"active"`
	// Error is why the last install or removal failed (Driver "failed").
	Error string `json:"error,omitempty"`
}

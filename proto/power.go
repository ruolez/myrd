package proto

// Remote restart (control `power`, docs/PROTOCOL.md §5): the browser asks the
// device, over the session's end-to-end control channel, to restart itself
// or its agent. The server only sees the session end with the reason.

const (
	// FeaturePower: the agent restarts on request; hello `power` lists the
	// actions it offers.
	FeaturePower = "power"

	PowerRestart      = "restart"       // restart the device
	PowerRestartAgent = "restart_agent" // restart this agent
)

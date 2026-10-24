package proto

// Remote screen resolution (control `display`, docs/PROTOCOL.md §5): the
// browser switches the device's monitor to another display mode for the
// session; the agent restores the saved mode after the session.

// FeatureDisplay: the agent can switch display modes; the stream state then
// carries `display`.
const FeatureDisplay = "display"

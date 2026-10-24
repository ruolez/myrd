package session

import "github.com/ruolez/remotectl/proto"

// helloFeatures lists the optional protocol features this agent supports,
// advertised in the control-channel hello (docs/PROTOCOL.md §5): "stream"
// and "move" always, "files" when Options enables file transfer, "wallpaper" when the
// backend can hide the wallpaper, "unlock" when the device types saved
// passwords on its lock screen (Options.UnlockOS), "power" when it restarts on
// request (Options.Restart / RestartAgent), "display" when the backend can
// switch the monitor's display mode.
func helloFeatures(o Options, wallpaper, display bool) []string {
	f := []string{"stream", "move"}
	if o.Files != nil {
		f = append(f, "files")
	}
	if wallpaper {
		f = append(f, "wallpaper")
	}
	if o.UnlockOS != "" {
		f = append(f, proto.FeatureUnlock)
	}
	if len(powerActions(o)) > 0 {
		f = append(f, proto.FeaturePower)
	}
	if display {
		f = append(f, proto.FeatureDisplay)
	}
	return f
}

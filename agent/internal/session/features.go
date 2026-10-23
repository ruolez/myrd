package session

import "github.com/ruolez/remotectl/proto"

// helloFeatures lists the optional protocol features this agent supports,
// advertised in the control-channel hello (docs/PROTOCOL.md §5): "stream"
// and "move" always, "files" when Options enables file transfer, "wallpaper" when the
// backend can hide the wallpaper, "unlock" when the device types saved
// passwords on its lock screen (Options.UnlockOS).
func helloFeatures(o Options, wallpaper bool) []string {
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
	return f
}

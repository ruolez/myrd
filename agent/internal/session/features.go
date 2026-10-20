package session

// helloFeatures lists the optional protocol features this agent supports,
// advertised in the control-channel hello (docs/PROTOCOL.md §5): "stream"
// always, "files" when Options enables file transfer, "wallpaper" when the
// backend can hide the wallpaper.
func helloFeatures(o Options, wallpaper bool) []string {
	f := []string{"stream"}
	if o.Files != nil {
		f = append(f, "files")
	}
	if wallpaper {
		f = append(f, "wallpaper")
	}
	return f
}

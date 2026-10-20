package session

// helloFeatures lists the optional protocol features this agent supports,
// advertised in the control-channel hello (docs/PROTOCOL.md §5): "stream"
// always, "files" when Options enables file transfer.
func helloFeatures(o Options) []string {
	f := []string{"stream"}
	if o.Files != nil {
		f = append(f, "files")
	}
	return f
}

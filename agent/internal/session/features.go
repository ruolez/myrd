package session

// helloFeatures lists the optional protocol features this agent supports,
// advertised in the control-channel hello (docs/PROTOCOL.md §5). The stream
// track adds "stream"; the file track adds "files" when Options enables it.
func helloFeatures(o Options) []string {
	return []string{}
}

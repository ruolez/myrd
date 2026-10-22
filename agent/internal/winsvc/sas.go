package winsvc

// softwareSASGeneration is the SoftwareSASGeneration policy value that lets
// services simulate Ctrl+Alt+Delete (bit 1; bit 2 is Ease of Access apps,
// kept as it was), and whether cur, the current value (exists=false when
// unset), has to change for that.
func softwareSASGeneration(cur uint64, exists bool) (uint32, bool) {
	if exists && cur&1 != 0 {
		return uint32(cur), false
	}
	return uint32(cur) | 1, true
}

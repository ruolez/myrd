package identity

import "path/filepath"

// Placeholder: file store. The linux backend replaces this with the OS secret store.
func platformKeystore(dataDir string) Keystore {
	return FileKeystore{Path: filepath.Join(dataDir, "device.key")}
}

package identity

import (
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dpapiEntropy binds the blob to this purpose; it is not a secret.
var dpapiEntropy = []byte("remotectl-device-key-v1")

// DPAPIKeystore keeps the private key as a DPAPI blob sealed with
// CRYPTPROTECT_LOCAL_MACHINE, in a file whose DACL allows only SYSTEM,
// Administrators and the account that runs the agent.
type DPAPIKeystore struct{ Path string }

func platformKeystore(dataDir string) Keystore {
	return DPAPIKeystore{Path: filepath.Join(dataDir, "device.key.dpapi")}
}

func (k DPAPIKeystore) Load() (ed25519.PrivateKey, error) {
	blob, err := os.ReadFile(k.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	raw, err := dpapiUnprotect(blob)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, errors.New("corrupt device key blob")
	}
	return ed25519.PrivateKey(raw), nil
}

func (k DPAPIKeystore) Save(key ed25519.PrivateKey) error {
	blob, err := dpapiProtect(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(k.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := restrictACL(dir, true); err != nil {
		return err
	}
	tmp := k.Path + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return err
	}
	if err := restrictACL(tmp, false); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, k.Path)
}

func (k DPAPIKeystore) Delete() error {
	err := os.Remove(k.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func blobOf(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func takeBlob(out *windows.DataBlob) []byte {
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}

func dpapiProtect(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	desc, _ := windows.UTF16PtrFromString("remotectl device key")
	err := windows.CryptProtectData(blobOf(plain), desc, blobOf(dpapiEntropy), 0, nil,
		windows.CRYPTPROTECT_LOCAL_MACHINE|windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return takeBlob(&out), nil
}

func dpapiUnprotect(blob []byte) ([]byte, error) {
	var out windows.DataBlob
	err := windows.CryptUnprotectData(blobOf(blob), nil, blobOf(dpapiEntropy), 0, nil,
		windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	return takeBlob(&out), nil
}

// restrictACL replaces the DACL with SYSTEM + Administrators + the current
// user (full control), protected from inheritance.
func restrictACL(path string, dir bool) error {
	inherit := ""
	if dir {
		inherit = "OICI" // object + container inherit for files created inside
	}
	sddl := "D:P(A;" + inherit + ";FA;;;SY)(A;" + inherit + ";FA;;;BA)"
	if sid, err := currentUserSID(); err == nil && sid != "S-1-5-18" {
		sddl += "(A;" + inherit + ";FA;;;" + sid + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func currentUserSID() (string, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}

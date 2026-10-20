//go:build darwin

package identity

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <Security/Security.h>

static CFMutableDictionaryRef rc_query(const char *service, const char *account) {
	CFMutableDictionaryRef q = CFDictionaryCreateMutable(NULL, 0, &kCFTypeDictionaryKeyCallBacks,
	                                                     &kCFTypeDictionaryValueCallBacks);
	CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(q, kSecAttrService, s);
	CFDictionarySetValue(q, kSecAttrAccount, a);
	CFRelease(s);
	CFRelease(a);
	return q;
}

// Returns errSecSuccess and a malloc'd copy of the secret, or an OSStatus.
static OSStatus rc_kc_load(const char *service, const char *account, void **out, long *len) {
	CFMutableDictionaryRef q = rc_query(service, account);
	CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef data = NULL;
	OSStatus st = SecItemCopyMatching(q, &data);
	CFRelease(q);
	if (st != errSecSuccess) return st;
	CFIndex n = CFDataGetLength((CFDataRef)data);
	*out = malloc(n);
	memcpy(*out, CFDataGetBytePtr((CFDataRef)data), n);
	*len = n;
	CFRelease(data);
	return errSecSuccess;
}

// The item's default ACL trusts only the creating application, so other apps
// get a Keychain prompt; kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly keeps
// it out of backups and iCloud.
static OSStatus rc_kc_save(const char *service, const char *account, const void *buf, long len) {
	CFMutableDictionaryRef q = rc_query(service, account);
	SecItemDelete(q);
	CFDataRef d = CFDataCreate(NULL, buf, len);
	CFStringRef label = CFStringCreateWithCString(NULL, "remotectl device key", kCFStringEncodingUTF8);
	CFDictionarySetValue(q, kSecValueData, d);
	CFDictionarySetValue(q, kSecAttrLabel, label);
	CFDictionarySetValue(q, kSecAttrAccessible, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly);
	OSStatus st = SecItemAdd(q, NULL);
	CFRelease(d);
	CFRelease(label);
	CFRelease(q);
	return st;
}

static OSStatus rc_kc_delete(const char *service, const char *account) {
	CFMutableDictionaryRef q = rc_query(service, account);
	OSStatus st = SecItemDelete(q);
	CFRelease(q);
	return st;
}
*/
import "C"

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"
)

// KeychainService is the generic-password service name; product name is an
// open decision, so it lives in one place.
var KeychainService = "com.remotectl.agent"

const keychainAccount = "device-key"

// KeychainKeystore keeps the Ed25519 private key in the user's login Keychain.
// It is only the migration source now: a self-signed build has no Team ID, so
// the item's access was tied to one exact build and every update prompted for
// the login password before the agent could load its key.
type KeychainKeystore struct{ Service, Account string }

func platformKeystore(dataDir string) Keystore {
	return migratingKeystore{
		primary: FileKeystore{Path: filepath.Join(dataDir, "device.key")},
		legacy:  KeychainKeystore{Service: KeychainService, Account: keychainAccount},
	}
}

func (k KeychainKeystore) cstrings() (*C.char, *C.char, func()) {
	s, a := C.CString(k.Service), C.CString(k.Account)
	return s, a, func() { C.free(unsafe.Pointer(s)); C.free(unsafe.Pointer(a)) }
}

func (k KeychainKeystore) Load() (ed25519.PrivateKey, error) {
	s, a, free := k.cstrings()
	defer free()
	var out unsafe.Pointer
	var n C.long
	st := C.rc_kc_load(s, a, &out, &n)
	if st == C.errSecItemNotFound {
		return nil, ErrNotFound
	}
	if st != C.errSecSuccess {
		return nil, fmt.Errorf("keychain read: OSStatus %d", int(st))
	}
	defer C.free(out)
	b := C.GoBytes(out, C.int(n))
	if len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("corrupt device key in keychain")
	}
	return ed25519.PrivateKey(b), nil
}

func (k KeychainKeystore) Save(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("invalid ed25519 private key")
	}
	s, a, free := k.cstrings()
	defer free()
	if st := C.rc_kc_save(s, a, unsafe.Pointer(&key[0]), C.long(len(key))); st != C.errSecSuccess {
		return fmt.Errorf("keychain write: OSStatus %d", int(st))
	}
	return nil
}

func (k KeychainKeystore) Delete() error {
	s, a, free := k.cstrings()
	defer free()
	st := C.rc_kc_delete(s, a)
	if st != C.errSecSuccess && st != C.errSecItemNotFound {
		return fmt.Errorf("keychain delete: OSStatus %d", int(st))
	}
	return nil
}

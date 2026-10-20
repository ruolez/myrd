package proto

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Release signing (docs/PROTOCOL.md "Agent updates"). The offline release
// key signs the installer's identity and hash; where the server hosts the
// file is deliberately not signed.
const (
	SidecarFormat   = "remotectl-release-sig-v2"
	MaxSidecarBytes = 4096
)

// ReleaseOSes and ReleaseArches are the targets a release can be built for.
var (
	ReleaseOSes   = []string{"darwin", "windows"}
	ReleaseArches = []string{"amd64", "arm64", "universal"}
)

// ReleaseMessage is what the release key signs:
//
//	"remotectl-release-v2\n" + version + "\n" + os + "\n" + arch + "\n" + lower(sha256hex)
func ReleaseMessage(version, os, arch, sha256hex string) []byte {
	return []byte("remotectl-release-v2\n" + version + "\n" + os + "\n" + arch + "\n" + strings.ToLower(sha256hex))
}

// VerifyRelease checks a release signature against a base64 Ed25519 public key.
func VerifyRelease(pubB64, version, os, arch, sha256hex, sigB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("no valid release public key")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("bad release signature encoding")
	}
	if !ed25519.Verify(pub, ReleaseMessage(version, os, arch, sha256hex), sig) {
		return errors.New("release signature does not verify")
	}
	return nil
}

// KeyID names a release public key: the first 16 hex digits of the SHA-256
// of the raw key ("" when pubB64 is not a key).
func KeyID(pubB64 string) string {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return ""
	}
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// RolloutBucket is uint32_be(sha256(device_id)[0:4]) % 100; a device takes a
// release when its bucket is below the release's rollout_pct.
func RolloutBucket(deviceID string) int {
	h := sha256.Sum256([]byte(deviceID))
	return int(binary.BigEndian.Uint32(h[:4]) % 100)
}

// ExtForOS is the installer extension a release for os must have.
func ExtForOS(os string) string {
	switch os {
	case "darwin":
		return ".pkg"
	case "windows":
		return ".msi"
	}
	return ""
}

// ReleaseSidecar is the <installer>.sig file written by remotectl-release sign.
type ReleaseSidecar struct {
	Format    string `json:"format"`
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Filename  string `json:"filename"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

// ParseSidecar decodes and validates a sidecar; it does not check the signature.
func ParseSidecar(b []byte) (ReleaseSidecar, error) {
	var s ReleaseSidecar
	if len(b) > MaxSidecarBytes {
		return s, errors.New("signature file is too large")
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, errors.New("signature file is not valid JSON")
	}
	switch {
	case s.Format != SidecarFormat:
		return s, fmt.Errorf("signature file format %q, want %q", s.Format, SidecarFormat)
	case !ValidVersion(s.Version):
		return s, fmt.Errorf("version %q is not semantic (x.y.z)", s.Version)
	case !oneOf(s.OS, ReleaseOSes):
		return s, fmt.Errorf("os %q is not one of %s", s.OS, strings.Join(ReleaseOSes, ", "))
	case !oneOf(s.Arch, ReleaseArches):
		return s, fmt.Errorf("arch %q is not one of %s", s.Arch, strings.Join(ReleaseArches, ", "))
	case !isLowerHex(s.SHA256, 64):
		return s, errors.New("sha256 must be 64 lowercase hex digits")
	case s.Size <= 0:
		return s, errors.New("size must be positive")
	case s.Filename == "" || path.Base(s.Filename) != s.Filename || strings.ContainsAny(s.Filename, `\/`) ||
		!strings.HasSuffix(strings.ToLower(s.Filename), ExtForOS(s.OS)):
		return s, fmt.Errorf("filename must be a plain %s name", ExtForOS(s.OS))
	case !isLowerHex(s.KeyID, 16):
		return s, errors.New("key_id must be 16 lowercase hex digits")
	}
	if sig, err := base64.StdEncoding.DecodeString(s.Signature); err != nil || len(sig) != ed25519.SignatureSize {
		return s, errors.New("signature must be a base64 Ed25519 signature")
	}
	return s, nil
}

// VerifySidecar checks a parsed sidecar against the release public key.
func VerifySidecar(pubB64 string, s ReleaseSidecar) error {
	if id := KeyID(pubB64); id != "" && s.KeyID != id {
		return fmt.Errorf("signed with a different release key (%s, this server trusts %s)", s.KeyID, id)
	}
	return VerifyRelease(pubB64, s.Version, s.OS, s.Arch, s.SHA256, s.Signature)
}

func oneOf(s string, set []string) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

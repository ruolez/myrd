package proto

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type releaseVectors struct {
	Release struct {
		Seed      string `json:"seed_hex"`
		PublicKey string `json:"public_key"`
		KeyID     string `json:"key_id"`
		Cases     []struct {
			Version   string `json:"version"`
			OS        string `json:"os"`
			Arch      string `json:"arch"`
			SHA256    string `json:"sha256"`
			Message   string `json:"message"`
			Signature string `json:"signature"`
		} `json:"cases"`
	} `json:"release"`
	Buckets []struct {
		DeviceID string `json:"device_id"`
		Bucket   int    `json:"bucket"`
	} `json:"rollout_bucket"`
}

func loadReleaseVectors(t *testing.T) (releaseVectors, ed25519.PrivateKey) {
	t.Helper()
	b, err := os.ReadFile("../docs/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v releaseVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(v.Release.Seed)
	if err != nil || len(seed) != ed25519.SeedSize || len(v.Release.Cases) == 0 {
		t.Fatal("release vectors missing")
	}
	return v, ed25519.NewKeyFromSeed(seed)
}

func TestReleaseVectors(t *testing.T) {
	v, priv := loadReleaseVectors(t)
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	if pub != v.Release.PublicKey {
		t.Fatalf("public key = %s, want %s", pub, v.Release.PublicKey)
	}
	if got := KeyID(v.Release.PublicKey); got != v.Release.KeyID {
		t.Fatalf("KeyID = %s, want %s", got, v.Release.KeyID)
	}
	for _, c := range v.Release.Cases {
		msg := ReleaseMessage(c.Version, c.OS, c.Arch, c.SHA256)
		if string(msg) != c.Message {
			t.Errorf("%s: message = %q, want %q", c.Version, msg, c.Message)
		}
		if sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg)); sig != c.Signature {
			t.Errorf("%s: signature = %s, want %s", c.Version, sig, c.Signature)
		}
		if err := VerifyRelease(v.Release.PublicKey, c.Version, c.OS, c.Arch, c.SHA256, c.Signature); err != nil {
			t.Errorf("%s: VerifyRelease: %v", c.Version, err)
		}
	}
}

func TestVerifyReleaseRejectsTampering(t *testing.T) {
	v, _ := loadReleaseVectors(t)
	c := v.Release.Cases[0]
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	otherPub := base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	flipped := "0" + c.SHA256[1:]
	if flipped == c.SHA256 {
		flipped = "1" + c.SHA256[1:]
	}
	for _, tc := range []struct {
		name                         string
		pub, ver, os, arch, sha, sig string
	}{
		{"version", v.Release.PublicKey, "9.9.9", c.OS, c.Arch, c.SHA256, c.Signature},
		{"os", v.Release.PublicKey, c.Version, "windows", c.Arch, c.SHA256, c.Signature},
		{"arch", v.Release.PublicKey, c.Version, c.OS, "amd64", c.SHA256, c.Signature},
		{"sha256", v.Release.PublicKey, c.Version, c.OS, c.Arch, flipped, c.Signature},
		{"other key", otherPub, c.Version, c.OS, c.Arch, c.SHA256, c.Signature},
		{"no key", "", c.Version, c.OS, c.Arch, c.SHA256, c.Signature},
		{"bad signature", v.Release.PublicKey, c.Version, c.OS, c.Arch, c.SHA256, "c2ln"},
	} {
		if err := VerifyRelease(tc.pub, tc.ver, tc.os, tc.arch, tc.sha, tc.sig); err == nil {
			t.Errorf("%s: tampered release verified", tc.name)
		}
	}
}

func TestVerifyReleaseIgnoresSHA256Case(t *testing.T) {
	v, _ := loadReleaseVectors(t)
	c := v.Release.Cases[0]
	if err := VerifyRelease(v.Release.PublicKey, c.Version, c.OS, c.Arch, strings.ToUpper(c.SHA256), c.Signature); err != nil {
		t.Fatal(err)
	}
}

func TestRolloutBucketVectors(t *testing.T) {
	v, _ := loadReleaseVectors(t)
	if len(v.Buckets) == 0 {
		t.Fatal("rollout_bucket vectors missing")
	}
	for _, c := range v.Buckets {
		if got := RolloutBucket(c.DeviceID); got != c.Bucket {
			t.Errorf("RolloutBucket(%s) = %d, want %d", c.DeviceID, got, c.Bucket)
		}
	}
}

func validSidecar(t *testing.T) (ReleaseSidecar, string) {
	t.Helper()
	v, priv := loadReleaseVectors(t)
	c := v.Release.Cases[0]
	return ReleaseSidecar{
		Format: SidecarFormat, Version: c.Version, OS: c.OS, Arch: c.Arch, SHA256: c.SHA256,
		Size: 1234, Filename: "RemoteAgent-" + c.Version + ExtForOS(c.OS), KeyID: v.Release.KeyID,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ReleaseMessage(c.Version, c.OS, c.Arch, c.SHA256))),
	}, v.Release.PublicKey
}

func TestParseSidecarRoundTrip(t *testing.T) {
	sc, pub := validSidecar(t)
	b, err := json.Marshal(sc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSidecar(b)
	if err != nil {
		t.Fatal(err)
	}
	if got != sc {
		t.Fatalf("got %+v, want %+v", got, sc)
	}
	if err := VerifySidecar(pub, got); err != nil {
		t.Fatal(err)
	}
}

func TestParseSidecarRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ReleaseSidecar)
	}{
		{"format", func(s *ReleaseSidecar) { s.Format = "remotectl-release-sig-v1" }},
		{"version", func(s *ReleaseSidecar) { s.Version = "dev" }},
		{"os", func(s *ReleaseSidecar) { s.OS = "linux" }},
		{"arch", func(s *ReleaseSidecar) { s.Arch = "386" }},
		{"sha256 length", func(s *ReleaseSidecar) { s.SHA256 = s.SHA256[:63] }},
		{"sha256 upper", func(s *ReleaseSidecar) { s.SHA256 = strings.ToUpper(s.SHA256) }},
		{"size", func(s *ReleaseSidecar) { s.Size = 0 }},
		{"filename ext", func(s *ReleaseSidecar) { s.Filename = "RemoteAgent.msi" }},
		{"filename path", func(s *ReleaseSidecar) { s.Filename = "../RemoteAgent.pkg" }},
		{"key id", func(s *ReleaseSidecar) { s.KeyID = "xyz" }},
		{"signature", func(s *ReleaseSidecar) { s.Signature = "not base64!" }},
	} {
		sc, _ := validSidecar(t)
		tc.edit(&sc)
		b, _ := json.Marshal(sc)
		if _, err := ParseSidecar(b); err == nil {
			t.Errorf("%s: invalid sidecar accepted", tc.name)
		}
	}
	if _, err := ParseSidecar([]byte(`{` + strings.Repeat(" ", MaxSidecarBytes) + `}`)); err == nil {
		t.Error("oversized sidecar accepted")
	}
	if _, err := ParseSidecar([]byte(`not json`)); err == nil {
		t.Error("non-JSON sidecar accepted")
	}
}

func TestVerifySidecarWrongKey(t *testing.T) {
	sc, _ := validSidecar(t)
	other := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	otherPub := base64.StdEncoding.EncodeToString(other.Public().(ed25519.PublicKey))
	if err := VerifySidecar(otherPub, sc); err == nil || !strings.Contains(err.Error(), "different release key") {
		t.Fatalf("err = %v, want a different-key error", err)
	}
}

func TestCommittedReleaseKeyDecodes(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(ReleasePublicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		t.Fatalf("ReleasePublicKey is not a base64 Ed25519 public key: %v", err)
	}
}

package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func testKey(t *testing.T) (keyFile, pub string) {
	t.Helper()
	keyFile = filepath.Join(t.TempDir(), "conf", "release.key")
	out, err := keygen(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	return keyFile, out
}

func writeInstaller(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeygenWritesPrivateFileAndRefusesOverwrite(t *testing.T) {
	keyFile, pub := testKey(t)
	st, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", st.Mode().Perm())
	}
	priv, err := loadPrivateKey(keyFile, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)); got != pub {
		t.Fatalf("public key = %s, want %s", got, pub)
	}
	if _, err := keygen(keyFile); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
}

func TestSignThenVerify(t *testing.T) {
	keyFile, pub := testKey(t)
	priv, _ := loadPrivateKey(keyFile, "")
	inst := writeInstaller(t, "RemoteAgent-0.3.0.pkg", "pkg bytes")
	sigPath, err := signFile(priv, "0.3.0", "darwin", "universal", inst)
	if err != nil {
		t.Fatal(err)
	}
	if sigPath != inst+".sig" {
		t.Fatalf("sig path = %s", sigPath)
	}
	b, _ := os.ReadFile(sigPath)
	var sc proto.ReleaseSidecar
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatal(err)
	}
	// SHA-256 and signature are checked by TestSidecarHashIsSHA256OfFile and verifyFile.
	want := proto.ReleaseSidecar{Format: proto.SidecarFormat, Version: "0.3.0", OS: "darwin", Arch: "universal",
		SHA256: sc.SHA256, Size: 9, Filename: "RemoteAgent-0.3.0.pkg", KeyID: proto.KeyID(pub), Signature: sc.Signature}
	if sc != want {
		t.Fatalf("sidecar = %+v, want %+v", sc, want)
	}
	if err := verifyFile(pub, inst, sigPath); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarHashIsSHA256OfFile(t *testing.T) {
	keyFile, _ := testKey(t)
	priv, _ := loadPrivateKey(keyFile, "")
	// sha256("abc") from FIPS 180-2.
	inst := writeInstaller(t, "RemoteAgent.msi", "abc")
	sigPath, err := signFile(priv, "0.3.0", "windows", "amd64", inst)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(sigPath)
	sc, err := proto.ParseSidecar(b)
	if err != nil {
		t.Fatal(err)
	}
	if sc.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || sc.Size != 3 {
		t.Fatalf("sha256/size = %s/%d", sc.SHA256, sc.Size)
	}
}

func TestVerifyRejects(t *testing.T) {
	keyFile, pub := testKey(t)
	priv, _ := loadPrivateKey(keyFile, "")
	_, otherPub := testKey(t)
	inst := writeInstaller(t, "RemoteAgent-0.3.0.pkg", "pkg bytes")
	sigPath, err := signFile(priv, "0.3.0", "darwin", "universal", inst)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(otherPub, inst, sigPath); err == nil {
		t.Error("verified with another key")
	}
	if err := os.WriteFile(inst, []byte("pkg bytez"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(pub, inst, sigPath); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("tampered file: err = %v", err)
	}
}

func TestSignRejectsWrongExtension(t *testing.T) {
	keyFile, _ := testKey(t)
	priv, _ := loadPrivateKey(keyFile, "")
	inst := writeInstaller(t, "RemoteAgent.msi", "x")
	if _, err := signFile(priv, "0.3.0", "darwin", "universal", inst); err == nil {
		t.Fatal("signed an .msi as a macOS release")
	}
	if _, err := os.Stat(inst + ".sig"); err == nil {
		t.Fatal("sidecar written for a rejected release")
	}
}

func TestLoadPrivateKeyPrefersEnv(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	got, err := loadPrivateKey(filepath.Join(t.TempDir(), "missing"), base64.StdEncoding.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(priv) {
		t.Fatal("env key not used")
	}
}

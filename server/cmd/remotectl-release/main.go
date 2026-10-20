// remotectl-release manages the offline Ed25519 release key and signs agent
// installers (docs/PROTOCOL.md "Agent updates").
//
//	remotectl-release keygen --out ~/.config/remotectl/release.key
//	remotectl-release sign [--key-file F] --version 0.3.0 --os darwin --arch universal RemoteAgent-0.3.0.pkg
//	remotectl-release verify [--pub B64] RemoteAgent-0.3.0.pkg [RemoteAgent-0.3.0.pkg.sig]
//
// sign writes <installer>.sig, which is uploaded next to the installer in
// Admin → Releases. RELEASE_PRIVATE_KEY (base64), when set, is used instead
// of the key file (CI).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ruolez/remotectl/proto"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultKeyFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "remotectl", "release.key")
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: remotectl-release keygen | sign | verify")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "keygen":
		out := fs.String("out", defaultKeyFile(), "private key file to create (mode 0600)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		pub, err := keygen(*out)
		if err != nil {
			return err
		}
		fmt.Printf("Private key written to %s. Keep it offline and back it up.\n\n", *out)
		fmt.Printf("Public key (key id %s); commit it as proto/release_key.go:\n\n", proto.KeyID(pub))
		fmt.Printf("\tvar ReleasePublicKey = %q\n", pub)
		return nil
	case "sign":
		keyFile := fs.String("key-file", defaultKeyFile(), "private key file (ignored when RELEASE_PRIVATE_KEY is set)")
		version := fs.String("version", "", "release version (x.y.z)")
		goos := fs.String("os", "", "darwin | windows")
		arch := fs.String("arch", "", "amd64 | arm64 | universal")
		otherKey := fs.Bool("allow-other-key", false, "sign with a key other than the one compiled into agents (tests)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("sign takes exactly one installer file")
		}
		priv, err := loadPrivateKey(*keyFile, os.Getenv("RELEASE_PRIVATE_KEY"))
		if err != nil {
			return err
		}
		pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
		if pub != proto.ReleasePublicKey && !*otherKey {
			return fmt.Errorf("key %s is not the release key compiled into agents (%s)", proto.KeyID(pub), proto.KeyID(proto.ReleasePublicKey))
		}
		sig, err := signFile(priv, *version, *goos, *arch, fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Println(sig)
		return nil
	case "verify":
		pub := fs.String("pub", proto.ReleasePublicKey, "base64 Ed25519 release public key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() < 1 || fs.NArg() > 2 {
			return errors.New("verify takes an installer and optionally its .sig")
		}
		sigPath := fs.Arg(0) + ".sig"
		if fs.NArg() == 2 {
			sigPath = fs.Arg(1)
		}
		if err := verifyFile(*pub, fs.Arg(0), sigPath); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

// keygen creates a new private key file and returns the base64 public key.
func keygen(out string) (string, error) {
	if _, err := os.Stat(out); err == nil {
		return "", fmt.Errorf("%s already exists; refusing to replace a release key", out)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv)); err != nil {
		f.Close()
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), f.Close()
}

func loadPrivateKey(file, env string) (ed25519.PrivateKey, error) {
	raw := strings.TrimSpace(env)
	if raw == "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("release key: %w", err)
		}
		raw = strings.TrimSpace(string(b))
	}
	k, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(k) != ed25519.PrivateKeySize {
		return nil, errors.New("release key must be a base64 64-byte Ed25519 private key")
	}
	return ed25519.PrivateKey(k), nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// signFile writes <installer>.sig and returns its path.
func signFile(priv ed25519.PrivateKey, version, goos, arch, installer string) (string, error) {
	sha, size, err := hashFile(installer)
	if err != nil {
		return "", err
	}
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	sc := proto.ReleaseSidecar{
		Format: proto.SidecarFormat, Version: version, OS: goos, Arch: arch, SHA256: sha, Size: size,
		Filename: filepath.Base(installer), KeyID: proto.KeyID(pub),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, proto.ReleaseMessage(version, goos, arch, sha))),
	}
	b, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return "", err
	}
	if _, err := proto.ParseSidecar(b); err != nil {
		return "", err
	}
	if err := proto.VerifySidecar(pub, sc); err != nil {
		return "", err
	}
	out := installer + ".sig"
	return out, os.WriteFile(out, append(b, '\n'), 0o644)
}

func verifyFile(pub, installer, sigPath string) error {
	b, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	sc, err := proto.ParseSidecar(b)
	if err != nil {
		return err
	}
	sha, size, err := hashFile(installer)
	if err != nil {
		return err
	}
	if sha != sc.SHA256 || size != sc.Size {
		return fmt.Errorf("installer does not match the signature file (sha256 %s, size %d)", sha, size)
	}
	return proto.VerifySidecar(pub, sc)
}

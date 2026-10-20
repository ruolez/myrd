// remotectl-release manages the offline Ed25519 release key.
//
//	remotectl-release keygen
//	remotectl-release sign --key <b64 private> --version 1.2.3 --os darwin --arch universal --url https://… --sha256 <hex>
//	remotectl-release verify --pub <b64 public> --sig <b64> --version … --os … --arch … --url … --sha256 …
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ruolez/remotectl/proto"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: remotectl-release keygen | sign | verify")
	}
	switch args[0] {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Println("RELEASE_PUBLIC_KEY=" + base64.StdEncoding.EncodeToString(pub))
		fmt.Println("RELEASE_PRIVATE_KEY=" + base64.StdEncoding.EncodeToString(priv))
		fmt.Fprintln(os.Stderr, "Keep the private key offline. Compile the public key into the agent and set RELEASE_PUBLIC_KEY on the server.")
		return nil
	case "sign", "verify":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		key := fs.String("key", "", "base64 Ed25519 private key (sign)")
		pubS := fs.String("pub", "", "base64 Ed25519 public key (verify)")
		sigS := fs.String("sig", "", "base64 signature (verify)")
		version := fs.String("version", "", "release version")
		goos := fs.String("os", "", "darwin | windows | linux")
		arch := fs.String("arch", "", "amd64 | arm64 | universal")
		url := fs.String("url", "", "download URL")
		sha := fs.String("sha256", "", "hex SHA-256 of the artifact")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *version == "" || *goos == "" || *arch == "" || *url == "" || len(*sha) != 64 {
			return errors.New("--version, --os, --arch, --url and a 64-hex --sha256 are required")
		}
		msg := proto.ReleaseManifestMessage(*version, *goos, *arch, *url, *sha)
		if args[0] == "sign" {
			k, err := base64.StdEncoding.DecodeString(*key)
			if err != nil || len(k) != ed25519.PrivateKeySize {
				return errors.New("--key must be a base64 64-byte Ed25519 private key")
			}
			fmt.Println(base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(k), msg)))
			return nil
		}
		pub, err1 := base64.StdEncoding.DecodeString(*pubS)
		sig, err2 := base64.StdEncoding.DecodeString(*sigS)
		if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize {
			return errors.New("--pub and --sig must be base64")
		}
		if !ed25519.Verify(pub, msg, sig) {
			return errors.New("signature does NOT verify")
		}
		fmt.Println("ok")
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

package proto

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"math/big"
	"regexp"
	"strings"
)

// Fingerprint roles bound into the signature so an answer signature can never
// be replayed as an offer signature.
const (
	RoleOffer  = "offer"
	RoleAnswer = "answer"
)

var fpLine = regexp.MustCompile(`(?mi)^a=fingerprint:sha-256 ([0-9A-F:]+)\s*$`)

// SDPFingerprint returns the first sha-256 DTLS fingerprint in sdp, uppercased.
func SDPFingerprint(sdp string) (string, error) {
	m := fpLine.FindStringSubmatch(sdp)
	if m == nil {
		return "", errors.New("no sha-256 fingerprint in sdp")
	}
	return strings.ToUpper(m[1]), nil
}

// FingerprintMessage is the exact byte string each side signs:
//
//	"remotectl-fp-v1\n" + session_id + "\n" + role + "\n" + FINGERPRINT
func FingerprintMessage(sessionID, role, fingerprint string) []byte {
	return []byte("remotectl-fp-v1\n" + sessionID + "\n" + role + "\n" + strings.ToUpper(fingerprint))
}

// VerifyBrowserSig checks an ECDSA P-256/SHA-256 signature in IEEE P1363
// form (r||s, 64 bytes, as produced by WebCrypto) against an SPKI public key.
func VerifyBrowserSig(spkiB64, sigB64 string, msg []byte) error {
	der, err := base64.StdEncoding.DecodeString(spkiB64)
	if err != nil {
		return err
	}
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return errors.New("client key is not ECDSA P-256")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != 64 {
		return errors.New("bad signature encoding")
	}
	h := sha256.Sum256(msg)
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(ec, h[:], r, s) {
		return errors.New("signature mismatch")
	}
	return nil
}

// VerifyDeviceSig checks an Ed25519 signature against a raw 32-byte key.
func VerifyDeviceSig(pubB64, sigB64 string, msg []byte) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad device public key")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("bad signature encoding")
	}
	if !ed25519.Verify(pub, msg, sig) {
		return errors.New("signature mismatch")
	}
	return nil
}

// ClientKeyHash is the hex SHA-256 of the SPKI DER, used as the pin id.
func ClientKeyHash(spkiB64 string) (string, error) {
	der, err := base64.StdEncoding.DecodeString(spkiB64)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(der)
	const hexd = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range h {
		out[i*2] = hexd[b>>4]
		out[i*2+1] = hexd[b&15]
	}
	return string(out), nil
}

// AuthChallengeMessage is what the agent signs to authenticate:
//
//	"remotectl-auth-v1\n" + device_id + "\n" + nonce
func AuthChallengeMessage(deviceID, nonce string) []byte {
	return []byte("remotectl-auth-v1\n" + deviceID + "\n" + nonce)
}

// ReleaseManifestMessage is what the offline release key signs:
//
//	"remotectl-release-v1\n" + version + "\n" + os + "\n" + arch + "\n" + url + "\n" + sha256hex
func ReleaseManifestMessage(version, os, arch, url, sha256hex string) []byte {
	return []byte("remotectl-release-v1\n" + version + "\n" + os + "\n" + arch + "\n" + url + "\n" + strings.ToLower(sha256hex))
}

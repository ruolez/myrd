package proto

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func p1363Signer(t *testing.T, k *ecdsa.PrivateKey) func([]byte) ([]byte, error) {
	t.Helper()
	return func(msg []byte) ([]byte, error) {
		h := sha256.Sum256(msg)
		r, s, err := ecdsa.Sign(rand.Reader, k, h[:])
		if err != nil {
			return nil, err
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return sig, nil
	}
}

type unlockFixture struct {
	unlock     *ecdh.PrivateKey
	unlockSPKI []byte
	client     *ecdsa.PrivateKey
	clientSPKI []byte
}

func newUnlockFixture(t *testing.T) unlockFixture {
	t.Helper()
	_, dev, _ := ed25519.GenerateKey(rand.Reader)
	uk, err := DeriveUnlockKey(dev)
	if err != nil {
		t.Fatal(err)
	}
	ukSPKI, err := UnlockKeySPKI(uk)
	if err != nil {
		t.Fatal(err)
	}
	ck, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ckSPKI, _ := x509.MarshalPKIXPublicKey(&ck.PublicKey)
	return unlockFixture{unlock: uk, unlockSPKI: ukSPKI, client: ck, clientSPKI: ckSPKI}
}

const unlockUser = "6d2b8f5e-1c3a-4e7b-9f00-2a4c6e8b0d13"

func TestDeriveUnlockKey(t *testing.T) {
	_, a, _ := ed25519.GenerateKey(rand.Reader)
	_, b, _ := ed25519.GenerateKey(rand.Reader)
	a1, _ := DeriveUnlockKey(a)
	a2, _ := DeriveUnlockKey(a)
	b1, _ := DeriveUnlockKey(b)
	if !a1.Equal(a2) {
		t.Fatal("the same device key derived two unlock keys")
	}
	if a1.Equal(b1) {
		t.Fatal("two device keys derived the same unlock key")
	}
}

func TestCredentialRoundTripAndTampering(t *testing.T) {
	f := newUnlockFixture(t)
	want := Credential{Username: `CORP\alice`, Password: "pä$$ wörd ✓"}
	s, err := SealCredential(rand.Reader, f.unlockSPKI, f.clientSPKI, unlockUser, want, p1363Signer(t, f.client))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenCredential(f.unlock, unlockUser, *s)
	if err != nil || got != want {
		t.Fatalf("open: %+v %v", got, err)
	}

	other := newUnlockFixture(t)
	forged, _ := SealCredential(rand.Reader, f.unlockSPKI, other.clientSPKI, unlockUser, want, p1363Signer(t, f.client))
	ct, _ := base64.StdEncoding.DecodeString(s.Ciphertext)
	flipped := append([]byte{}, ct...)
	flipped[0] ^= 1
	sig, _ := base64.StdEncoding.DecodeString(s.Sig)
	badSig := append([]byte{}, sig...)
	badSig[5] ^= 1
	b64 := base64.StdEncoding.EncodeToString
	cases := []struct {
		name string
		key  *ecdh.PrivateKey
		user string
		edit func(*SealedCredential)
		want error
	}{
		{"another user", f.unlock, "6d2b8f5e-1c3a-4e7b-9f00-2a4c6e8b0d14", nil, ErrCredentialSignature},
		{"another device", other.unlock, unlockUser, nil, ErrCredentialStaleKey},
		{"a flipped ciphertext byte", f.unlock, unlockUser, func(c *SealedCredential) { c.Ciphertext = b64(flipped) }, ErrCredentialSignature},
		{"a flipped signature byte", f.unlock, unlockUser, func(c *SealedCredential) { c.Sig = b64(badSig) }, ErrCredentialSignature},
		{"signed by a key other than the one named", f.unlock, unlockUser, func(c *SealedCredential) { *c = *forged }, ErrCredentialSignature},
		{"a short iv", f.unlock, unlockUser, func(c *SealedCredential) { c.IV = b64(make([]byte, 8)) }, ErrCredentialMalformed},
		{"not base64", f.unlock, unlockUser, func(c *SealedCredential) { c.EphPub = "%%" }, ErrCredentialMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := *s
			if c.edit != nil {
				c.edit(&m)
			}
			if _, err := OpenCredential(c.key, c.user, m); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestOpenCredentialRejectsAnEmptyPassword(t *testing.T) {
	f := newUnlockFixture(t)
	s, _ := SealCredential(rand.Reader, f.unlockSPKI, f.clientSPKI, unlockUser, Credential{Username: "alice"}, p1363Signer(t, f.client))
	if _, err := OpenCredential(f.unlock, unlockUser, *s); !errors.Is(err, ErrCredentialMalformed) {
		t.Fatalf("err = %v", err)
	}
}

type unlockInvalid struct {
	Name       string  `json:"name"`
	UserID     *string `json:"user_id,omitempty"`
	UnlockKey  *string `json:"unlock_key,omitempty"`
	Ciphertext *string `json:"ciphertext,omitempty"`
	Sig        *string `json:"sig,omitempty"`
}

type unlockVectors struct {
	Note            string          `json:"note"`
	DeviceSeedHex   string          `json:"device_seed_hex"`
	DevicePublicKey string          `json:"device_public_key"`
	UnlockDHex      string          `json:"unlock_d_hex"`
	UnlockSPKI      string          `json:"unlock_spki"`
	UKSHA           string          `json:"uk_sha"`
	KeyMessageUTF8  string          `json:"key_message_utf8"`
	KeyMessageSig   string          `json:"key_message_sig"`
	ClientDHex      string          `json:"client_d_hex"`
	ClientSPKI      string          `json:"client_spki"`
	CKSHA           string          `json:"ck_sha"`
	UserID          string          `json:"user_id"`
	EphDHex         string          `json:"eph_d_hex"`
	EphPub          string          `json:"eph_pub"`
	IV              string          `json:"iv"`
	PlaintextUTF8   string          `json:"plaintext_utf8"`
	KeyHex          string          `json:"key_hex"`
	HeaderUTF8      string          `json:"header_utf8"`
	Ciphertext      string          `json:"ciphertext"`
	Sig             string          `json:"sig"`
	Invalid         []unlockInvalid `json:"invalid"`
}

// buildUnlockVectors recomputes every derived value from the fixed inputs
// (all but the ECDSA signature, which is randomized).
func buildUnlockVectors(t *testing.T) (unlockVectors, *ecdsa.PrivateKey) {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString
	devSeed := seed("remotectl unlock test vector device key")
	devKey := ed25519.NewKeyFromSeed(devSeed)
	uk, err := DeriveUnlockKey(devKey)
	if err != nil {
		t.Fatal(err)
	}
	ukSPKI, _ := UnlockKeySPKI(uk)
	ukSHA := SPKIHash(ukSPKI)
	clientD := seed("remotectl unlock test vector client key")
	client, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), clientD)
	if err != nil {
		t.Fatal(err)
	}
	clientSPKI, _ := x509.MarshalPKIXPublicKey(&client.PublicKey)
	ephD := seed("remotectl unlock test vector ephemeral key")
	eph, err := ecdh.P256().NewPrivateKey(ephD)
	if err != nil {
		t.Fatal(err)
	}
	iv := seed("remotectl unlock test vector iv")[:12]
	pt := `{"username":"alice","password":"correct horse ✓"}`
	s, err := sealCredential(eph, iv, ukSPKI, clientSPKI, unlockUser, []byte(pt), func([]byte) ([]byte, error) { return make([]byte, 64), nil })
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := eph.ECDH(uk.PublicKey())
	ckSHA := SPKIHash(clientSPKI)
	key, _ := credentialKey(shared, eph.PublicKey().Bytes(), unlockUser, ckSHA, ukSHA)
	return unlockVectors{
		Note: "All keys come from sha256 of a label (see proto/unlock_test.go); the unlock key is derived from the device seed. " +
			"sig is ECDSA (randomized): verify it, do not compare it. Invalid cases change one field of the valid credential; opening must fail.",
		DeviceSeedHex: hex.EncodeToString(devSeed), DevicePublicKey: b64(devKey.Public().(ed25519.PublicKey)),
		UnlockDHex: hex.EncodeToString(uk.Bytes()), UnlockSPKI: b64(ukSPKI), UKSHA: ukSHA,
		KeyMessageUTF8: string(UnlockKeyMessage(ukSHA)), KeyMessageSig: b64(ed25519.Sign(devKey, UnlockKeyMessage(ukSHA))),
		ClientDHex: hex.EncodeToString(clientD), ClientSPKI: b64(clientSPKI), CKSHA: ckSHA, UserID: unlockUser,
		EphDHex: hex.EncodeToString(ephD), EphPub: s.EphPub, IV: s.IV, PlaintextUTF8: pt, KeyHex: hex.EncodeToString(key),
		HeaderUTF8: string(CredentialHeader(unlockUser, ckSHA, ukSHA, eph.PublicKey().Bytes(), iv)), Ciphertext: s.Ciphertext,
	}, client
}

func loadUnlockVectors(t *testing.T) unlockVectors {
	t.Helper()
	b, err := os.ReadFile("../docs/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		Unlock *unlockVectors `json:"unlock"`
	}
	if err := json.Unmarshal(b, &all); err != nil || all.Unlock == nil {
		t.Fatalf("unlock vectors missing: %v", err)
	}
	return *all.Unlock
}

func TestUnlockVectors(t *testing.T) {
	want, _ := buildUnlockVectors(t)
	v := loadUnlockVectors(t)
	got := v
	got.Invalid, got.Sig, got.Note = nil, "", want.Note
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		wj, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("vectors differ from the code:\n%s\nwant\n%s", gj, wj)
	}
	uk, _ := ecdh.P256().NewPrivateKey(mustHex(t, v.UnlockDHex))
	sealed := SealedCredential{ClientKey: v.ClientSPKI, UnlockKey: v.UKSHA, EphPub: v.EphPub, IV: v.IV, Ciphertext: v.Ciphertext, Sig: v.Sig}
	c, err := OpenCredential(uk, v.UserID, sealed)
	if err != nil || c != (Credential{Username: "alice", Password: "correct horse ✓"}) {
		t.Fatalf("valid vector: %+v %v", c, err)
	}
	if len(v.Invalid) == 0 {
		t.Fatal("no invalid cases")
	}
	for _, inv := range v.Invalid {
		s, user := sealed, v.UserID
		if inv.UserID != nil {
			user = *inv.UserID
		}
		if inv.UnlockKey != nil {
			s.UnlockKey = *inv.UnlockKey
		}
		if inv.Ciphertext != nil {
			s.Ciphertext = *inv.Ciphertext
		}
		if inv.Sig != nil {
			s.Sig = *inv.Sig
		}
		if _, err := OpenCredential(uk, user, s); err == nil {
			t.Errorf("invalid case %q opened", inv.Name)
		}
	}
}

// RC_PRINT_UNLOCK_VECTORS=1 go test -run TestPrintUnlockVectors prints the
// "unlock" section of docs/test-vectors.json (with a fresh signature and the
// invalid cases).
func TestPrintUnlockVectors(t *testing.T) {
	if os.Getenv("RC_PRINT_UNLOCK_VECTORS") == "" {
		t.Skip("set RC_PRINT_UNLOCK_VECTORS=1")
	}
	v, client := buildUnlockVectors(t)
	ct, _ := base64.StdEncoding.DecodeString(v.Ciphertext)
	sig, err := p1363Signer(t, client)(credentialSigMessage([]byte(v.HeaderUTF8), ct))
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString
	v.Sig = b64(sig)
	flipped := append([]byte{}, ct...)
	flipped[3] ^= 0x40
	badSig := append([]byte{}, sig...)
	badSig[0] ^= 1
	str := func(s string) *string { return &s }
	v.Invalid = []unlockInvalid{
		{Name: "another user", UserID: str("6d2b8f5e-1c3a-4e7b-9f00-2a4c6e8b0d14")},
		{Name: "another unlock key", UnlockKey: str(SPKIHash([]byte("other")))},
		{Name: "a flipped ciphertext byte", Ciphertext: str(b64(flipped))},
		{Name: "a truncated tag", Ciphertext: str(b64(ct[:len(ct)-1]))},
		{Name: "a flipped signature byte", Sig: str(b64(badSig))},
	}
	out, _ := json.MarshalIndent(map[string]any{"unlock": v}, "", "  ")
	os.Stdout.Write(append(out, '\n'))
}

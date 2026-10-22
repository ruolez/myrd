package proto

import (
	"bytes"
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
	"os"
	"reflect"
	"testing"
)

func mustSPKI(t *testing.T, pub *ecdh.PublicKey) []byte {
	t.Helper()
	k, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestPreviewRoundTripAndTampering(t *testing.T) {
	devPub, devKey, _ := ed25519.GenerateKey(rand.Reader)
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	spki := mustSPKI(t, priv.PublicKey())
	m := PreviewMeta{DeviceID: "3F0C6C1E-6C55-4C3A-9A39-0A3F8D0E2B11", CKSHA: "aa", PKSHA: SPKIHash(spki),
		CapturedAtMS: 1792600000000, MonitorID: 1, Width: 480, Height: 270, Mime: PreviewMime}
	jpeg := []byte("\xff\xd8 pretend jpeg \xff\xd9")
	s, err := SealPreview(rand.Reader, devKey, spki, m, jpeg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenPreview(devPub, priv, m, s)
	if err != nil || !bytes.Equal(got, jpeg) {
		t.Fatalf("open: %q %v", got, err)
	}

	other := m
	other.Width = 481
	if _, err := OpenPreview(devPub, priv, other, s); err == nil {
		t.Fatal("changed width accepted")
	}
	other = m
	other.CapturedAtMS++
	if _, err := OpenPreview(devPub, priv, other, s); err == nil {
		t.Fatal("changed capture time accepted")
	}
	bad := *s
	bad.Ciphertext = append([]byte{}, s.Ciphertext...)
	bad.Ciphertext[0] ^= 1
	if _, err := OpenPreview(devPub, priv, m, &bad); err == nil {
		t.Fatal("flipped ciphertext accepted")
	}
	wrongDev, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := OpenPreview(wrongDev, priv, m, s); err == nil {
		t.Fatal("another device's key accepted")
	}
	stranger, _ := ecdh.P256().GenerateKey(rand.Reader)
	if _, err := OpenPreview(devPub, stranger, m, s); err == nil {
		t.Fatal("decrypted with another browser's key")
	}
}

func TestPreviewKeyMessage(t *testing.T) {
	if got := string(PreviewKeyMessage("ck", "pk")); got != "remotectl-preview-key-v1\nck\npk" {
		t.Fatalf("%q", got)
	}
}

type previewVectors struct {
	Note            string           `json:"note"`
	DeviceSeedHex   string           `json:"device_seed_hex"`
	DevicePublicKey string           `json:"device_public_key"`
	ClientDHex      string           `json:"client_d_hex"`
	ClientSPKI      string           `json:"client_spki"`
	CKSHA           string           `json:"ck_sha"`
	PreviewDHex     string           `json:"preview_d_hex"`
	PreviewSPKI     string           `json:"preview_spki"`
	PKSHA           string           `json:"pk_sha"`
	KeyMessageUTF8  string           `json:"key_message_utf8"`
	KeyMessageSig   string           `json:"key_message_sig"`
	EphDHex         string           `json:"eph_d_hex"`
	EphPub          string           `json:"eph_pub"`
	IV              string           `json:"iv"`
	PlaintextHex    string           `json:"plaintext_hex"`
	DeviceID        string           `json:"device_id"`
	CapturedAtMS    int64            `json:"captured_at_ms"`
	MonitorID       uint32           `json:"monitor_id"`
	Width           int              `json:"width"`
	Height          int              `json:"height"`
	Mime            string           `json:"mime"`
	SharedHex       string           `json:"shared_hex"`
	KeyHex          string           `json:"key_hex"`
	HeaderUTF8      string           `json:"header_utf8"`
	Ciphertext      string           `json:"ciphertext"`
	Sig             string           `json:"sig"`
	Invalid         []previewInvalid `json:"invalid"`
}

func seed(label string) []byte {
	h := sha256.Sum256([]byte(label))
	return h[:]
}

// buildPreviewVectors recomputes every derived value from the fixed inputs.
func buildPreviewVectors(t *testing.T) previewVectors {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString
	devKey := ed25519.NewKeyFromSeed(seed("remotectl preview test vector device key"))
	clientD := seed("remotectl preview test vector client key")
	client, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), clientD)
	if err != nil {
		t.Fatal(err)
	}
	clientSPKI, _ := x509.MarshalPKIXPublicKey(&client.PublicKey)
	previewD := seed("remotectl preview test vector preview key")
	preview, err := ecdh.P256().NewPrivateKey(previewD)
	if err != nil {
		t.Fatal(err)
	}
	previewSPKI := mustSPKI(t, preview.PublicKey())
	ephD := seed("remotectl preview test vector ephemeral key")
	eph, err := ecdh.P256().NewPrivateKey(ephD)
	if err != nil {
		t.Fatal(err)
	}
	iv := seed("remotectl preview test vector iv")[:12]
	plaintext := []byte("remotectl preview test vector plaintext")
	m := PreviewMeta{DeviceID: "3f0c6c1e-6c55-4c3a-9a39-0a3f8d0e2b11", CKSHA: SPKIHash(clientSPKI), PKSHA: SPKIHash(previewSPKI),
		CapturedAtMS: 1792600000123, MonitorID: 2, Width: 480, Height: 300, Mime: PreviewMime}
	s, err := sealPreview(eph, iv, devKey, previewSPKI, m, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := eph.ECDH(preview.PublicKey())
	key, _ := previewKey(shared, s.EphPub, m)
	return previewVectors{
		Note: "All keys come from sha256 of a label (see proto/preview_test.go). key_message_sig is ECDSA (randomized): verify it, do not compare it. " +
			"Invalid cases change one field of the valid preview; opening must fail.",
		DeviceSeedHex: hex.EncodeToString(seed("remotectl preview test vector device key")), DevicePublicKey: b64(devKey.Public().(ed25519.PublicKey)),
		ClientDHex: hex.EncodeToString(clientD), ClientSPKI: b64(clientSPKI), CKSHA: m.CKSHA,
		PreviewDHex: hex.EncodeToString(previewD), PreviewSPKI: b64(previewSPKI), PKSHA: m.PKSHA,
		KeyMessageUTF8: string(PreviewKeyMessage(m.CKSHA, m.PKSHA)),
		EphDHex:        hex.EncodeToString(ephD), EphPub: b64(s.EphPub), IV: b64(iv), PlaintextHex: hex.EncodeToString(plaintext),
		DeviceID: m.DeviceID, CapturedAtMS: m.CapturedAtMS, MonitorID: m.MonitorID, Width: m.Width, Height: m.Height, Mime: m.Mime,
		SharedHex: hex.EncodeToString(shared), KeyHex: hex.EncodeToString(key), HeaderUTF8: string(PreviewHeader(m, s.EphPub, iv)),
		Ciphertext: b64(s.Ciphertext), Sig: b64(s.Sig),
	}
}

func loadPreviewVectors(t *testing.T) previewVectors {
	t.Helper()
	b, err := os.ReadFile("../docs/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		Preview *previewVectors `json:"preview"`
	}
	if err := json.Unmarshal(b, &all); err != nil || all.Preview == nil {
		t.Fatalf("preview vectors missing: %v", err)
	}
	return *all.Preview
}

func TestPreviewVectors(t *testing.T) {
	want := buildPreviewVectors(t)
	v := loadPreviewVectors(t)
	got := v
	got.Invalid, got.KeyMessageSig, got.Note = nil, "", want.Note
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		wj, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("vectors differ from the code:\n%s\nwant\n%s", gj, wj)
	}
	// The browser's signature over the key message verifies.
	if err := VerifyBrowserSig(v.ClientSPKI, v.KeyMessageSig, []byte(v.KeyMessageUTF8)); err != nil {
		t.Fatalf("key message signature: %v", err)
	}

	devPub, _ := base64.StdEncoding.DecodeString(v.DevicePublicKey)
	priv, _ := ecdh.P256().NewPrivateKey(mustHex(t, v.PreviewDHex))
	dec := func(s string) []byte { b, _ := base64.StdEncoding.DecodeString(s); return b }
	meta := PreviewMeta{DeviceID: v.DeviceID, CKSHA: v.CKSHA, PKSHA: v.PKSHA, CapturedAtMS: v.CapturedAtMS,
		MonitorID: v.MonitorID, Width: v.Width, Height: v.Height, Mime: v.Mime}
	sealed := &SealedPreview{EphPub: dec(v.EphPub), IV: dec(v.IV), Ciphertext: dec(v.Ciphertext), Sig: dec(v.Sig)}
	pt, err := OpenPreview(devPub, priv, meta, sealed)
	if err != nil || hex.EncodeToString(pt) != v.PlaintextHex {
		t.Fatalf("valid vector: %q %v", pt, err)
	}
	if len(v.Invalid) == 0 {
		t.Fatal("no invalid cases")
	}
	for _, c := range v.Invalid {
		m, s := meta, *sealed
		if c.DeviceID != nil {
			m.DeviceID = *c.DeviceID
		}
		if c.CapturedAtMS != nil {
			m.CapturedAtMS = *c.CapturedAtMS
		}
		if c.Width != nil {
			m.Width = *c.Width
		}
		if c.PKSHA != nil {
			m.PKSHA = *c.PKSHA
		}
		if c.Ciphertext != nil {
			s.Ciphertext = dec(*c.Ciphertext)
		}
		if c.Sig != nil {
			s.Sig = dec(*c.Sig)
		}
		if _, err := OpenPreview(devPub, priv, m, &s); err == nil {
			t.Errorf("invalid case %q opened", c.Name)
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// RC_PRINT_PREVIEW_VECTORS=1 go test -run TestPrintPreviewVectors prints the
// "preview" section of docs/test-vectors.json (with a fresh key message
// signature and the invalid cases).
func TestPrintPreviewVectors(t *testing.T) {
	if os.Getenv("RC_PRINT_PREVIEW_VECTORS") == "" {
		t.Skip("set RC_PRINT_PREVIEW_VECTORS=1")
	}
	v := buildPreviewVectors(t)
	client, _ := ecdsa.ParseRawPrivateKey(elliptic.P256(), mustHex(t, v.ClientDHex))
	h := sha256.Sum256([]byte(v.KeyMessageUTF8))
	r, s, err := ecdsa.Sign(rand.Reader, client, h[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	v.KeyMessageSig = base64.StdEncoding.EncodeToString(sig)
	ct, _ := base64.StdEncoding.DecodeString(v.Ciphertext)
	flipped := append([]byte{}, ct...)
	flipped[3] ^= 0x40
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	num := func(n int) *int { return &n }
	b64 := base64.StdEncoding.EncodeToString
	sigBytes, _ := base64.StdEncoding.DecodeString(v.Sig)
	otherSig := append([]byte{}, sigBytes...)
	otherSig[0] ^= 1
	add := func(name string, f func(*previewInvalid)) {
		var c previewInvalid
		c.Name = name
		f(&c)
		v.Invalid = append(v.Invalid, c)
	}
	add("another device", func(c *previewInvalid) { c.DeviceID = str("3f0c6c1e-6c55-4c3a-9a39-0a3f8d0e2b12") })
	add("an older capture time", func(c *previewInvalid) { c.CapturedAtMS = i64(v.CapturedAtMS - 1) })
	add("another width", func(c *previewInvalid) { c.Width = num(v.Width + 1) })
	add("another preview key", func(c *previewInvalid) { c.PKSHA = str(SPKIHash([]byte("other"))) })
	add("a flipped ciphertext byte", func(c *previewInvalid) { c.Ciphertext = str(b64(flipped)) })
	add("a truncated tag", func(c *previewInvalid) { c.Ciphertext = str(b64(ct[:len(ct)-1])) })
	add("a flipped signature byte", func(c *previewInvalid) { c.Sig = str(b64(otherSig)) })
	out, _ := json.MarshalIndent(v, "", "  ")
	os.Stdout.Write(append(out, '\n'))
}

type previewInvalid = struct {
	Name         string  `json:"name"`
	DeviceID     *string `json:"device_id,omitempty"`
	CapturedAtMS *int64  `json:"captured_at_ms,omitempty"`
	Width        *int    `json:"width,omitempty"`
	PKSHA        *string `json:"pk_sha,omitempty"`
	Ciphertext   *string `json:"ciphertext,omitempty"`
	Sig          *string `json:"sig,omitempty"`
}

package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

type credentialBrowser struct {
	id   string
	key  *ecdsa.PrivateKey
	spki []byte
}

func (e *env) credentialBrowser(c *http.Client) *credentialBrowser {
	e.t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	var out struct {
		ClientKey struct{ ID string } `json:"client_key"`
	}
	if st := e.call(c, "POST", "/v1/client-keys", map[string]string{"public_key": base64.StdEncoding.EncodeToString(der), "label": "Chrome on Mac"}, &out); st != 200 {
		e.t.Fatalf("client key: %d", st)
	}
	return &credentialBrowser{id: out.ClientKey.ID, key: k, spki: der}
}

func (b *credentialBrowser) seal(t *testing.T, ag *fakeAgent, userID, password string) proto.SealedCredential {
	t.Helper()
	uk, _ := proto.DeriveUnlockKey(ag.priv)
	ukSPKI, _ := proto.UnlockKeySPKI(uk)
	s, err := proto.SealCredential(rand.Reader, ukSPKI, b.spki, userID, proto.Credential{Username: "alice", Password: password},
		func(msg []byte) ([]byte, error) {
			h := sha256.Sum256(msg)
			r, s, err := ecdsa.Sign(rand.Reader, b.key, h[:])
			sig := make([]byte, 64)
			r.FillBytes(sig[:32])
			s.FillBytes(sig[32:])
			return sig, err
		})
	if err != nil {
		t.Fatal(err)
	}
	return *s
}

type credentialReply struct {
	Credential *struct {
		ClientKeyID string                 `json:"client_key_id"`
		Username    string                 `json:"username"`
		Sealed      proto.SealedCredential `json:"sealed"`
	} `json:"credential"`
}

func TestSavedCredentialLifecycle(t *testing.T) {
	e := newEnv(t)
	owner := e.login(adminEmail, adminPass)
	var me struct{ User struct{ ID string } }
	e.call(owner, "GET", "/v1/auth/me", nil, &me)
	ag := e.enroll(owner)
	b := e.credentialBrowser(owner)
	path := "/v1/devices/" + ag.id + "/credential"

	var got credentialReply
	if st := e.call(owner, "GET", path, nil, &got); st != 200 || got.Credential != nil {
		t.Fatalf("nothing saved yet: %d %+v", st, got.Credential)
	}

	sealed := b.seal(t, ag, me.User.ID, "hunter2")
	if st := e.call(owner, "PUT", path, map[string]any{"username": `CORP\alice`, "sealed": sealed}, nil); st != 200 {
		t.Fatalf("save: %d", st)
	}
	got = credentialReply{}
	if st := e.call(owner, "GET", path, nil, &got); st != 200 || got.Credential == nil ||
		got.Credential.Username != `CORP\alice` || got.Credential.ClientKeyID != b.id || !reflect.DeepEqual(got.Credential.Sealed, sealed) {
		t.Fatalf("read back: %d %+v", st, got.Credential)
	}

	other := e.createUser(owner, "bob@example.com")
	if st := e.call(other, "GET", path, nil, nil); st != 404 {
		t.Fatalf("another account reads the credential: %d", st)
	}
	if st := e.call(other, "PUT", path, map[string]any{"username": "x", "sealed": sealed}, nil); st != 404 {
		t.Fatalf("another account saves a credential: %d", st)
	}

	if st := e.call(owner, "DELETE", path, nil, nil); st != 200 {
		t.Fatalf("delete: %d", st)
	}
	got = credentialReply{}
	if e.call(owner, "GET", path, nil, &got); got.Credential != nil {
		t.Fatal("credential still there after delete")
	}
	events := e.deviceEvents(ag.id)
	if !slices.Contains(events, "device.credential.saved") || !slices.Contains(events, "device.credential.removed") {
		t.Fatalf("audit = %v", events)
	}
}

func TestSavedCredentialRejectsWhatNoDeviceCouldOpen(t *testing.T) {
	e := newEnv(t)
	owner := e.login(adminEmail, adminPass)
	var me struct{ User struct{ ID string } }
	e.call(owner, "GET", "/v1/auth/me", nil, &me)
	ag := e.enroll(owner)
	path := "/v1/devices/" + ag.id + "/credential"
	b := e.credentialBrowser(owner)
	valid := b.seal(t, ag, me.User.ID, "hunter2")

	stranger := &credentialBrowser{}
	stranger.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	stranger.spki, _ = x509.MarshalPKIXPublicKey(&stranger.key.PublicKey)
	long := make([]rune, proto.MaxCredentialField+1)
	for i := range long {
		long[i] = 'a'
	}
	cases := []struct {
		name     string
		username string
		edit     func(*proto.SealedCredential)
	}{
		{"a browser key the account never registered", "alice", func(s *proto.SealedCredential) { *s = stranger.seal(t, ag, me.User.ID, "x") }},
		{"a short iv", "alice", func(s *proto.SealedCredential) { s.IV = base64.StdEncoding.EncodeToString(make([]byte, 8)) }},
		{"an unlock key that is not a hash", "alice", func(s *proto.SealedCredential) { s.UnlockKey = "abc" }},
		{"no ciphertext", "alice", func(s *proto.SealedCredential) { s.Ciphertext = "" }},
		{"a username over the limit", string(long), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := valid
			if c.edit != nil {
				c.edit(&s)
			}
			if st := e.call(owner, "PUT", path, map[string]any{"username": c.username, "sealed": s}, nil); st != 400 {
				t.Fatalf("status %d, want 400", st)
			}
		})
	}
}

func TestRevokingABrowserDropsTheCredentialsItSaved(t *testing.T) {
	e := newEnv(t)
	owner := e.login(adminEmail, adminPass)
	var me struct{ User struct{ ID string } }
	e.call(owner, "GET", "/v1/auth/me", nil, &me)
	ag := e.enroll(owner)
	path := "/v1/devices/" + ag.id + "/credential"
	b := e.credentialBrowser(owner)
	if st := e.call(owner, "PUT", path, map[string]any{"username": "alice", "sealed": b.seal(t, ag, me.User.ID, "pw")}, nil); st != 200 {
		t.Fatalf("save: %d", st)
	}
	if st := e.call(owner, "DELETE", "/v1/client-keys/"+b.id, nil, nil); st != 200 {
		t.Fatalf("revoke: %d", st)
	}
	var got credentialReply
	if e.call(owner, "GET", path, nil, &got); got.Credential != nil {
		t.Fatal("a revoked browser's credential survived")
	}
	if st := e.call(owner, "PUT", path, map[string]any{"username": "alice", "sealed": b.seal(t, ag, me.User.ID, "pw")}, nil); st != 400 {
		t.Fatalf("a revoked browser saved a credential: %d", st)
	}
}

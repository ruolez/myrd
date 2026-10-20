package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ruolez/remotectl/agent/internal/api"
	"github.com/ruolez/remotectl/proto"
)

func TestBucketStableAndInRange(t *testing.T) {
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("device-%d", i)
		b := Bucket(id)
		if b < 0 || b >= 100 || Bucket(id) != b {
			t.Fatalf("bucket(%s) = %d", id, b)
		}
	}
	// Independent computation of the documented formula.
	h := sha256.Sum256([]byte("00000000-0000-4000-8000-000000000000"))
	want := int((uint32(h[0])<<24 | uint32(h[1])<<16 | uint32(h[2])<<8 | uint32(h[3])) % 100)
	if got := Bucket("00000000-0000-4000-8000-000000000000"); got != want {
		t.Fatalf("bucket = %d want %d", got, want)
	}
}

func TestInCohortBoundaries(t *testing.T) {
	id := "abc"
	if InCohort(id, 0) || !InCohort(id, 100) || InCohort(id, Bucket(id)) || !InCohort(id, Bucket(id)+1) {
		t.Fatal("cohort boundary wrong")
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.0", "1.1.9", true}, {"1.2.0", "1.2.0", false}, {"1.10.0", "1.9.0", true},
		{"v2.0.0", "1.99.99", true}, {"1.2", "1.2.0", false}, {"1.2.1-beta", "1.2.0", true}, {"0.9.0", "1.0.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s,%s) = %v", c.a, c.b, got)
		}
	}
}

func signed(t *testing.T, priv ed25519.PrivateKey, m api.Manifest) api.Manifest {
	t.Helper()
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, proto.ReleaseManifestMessage(m.Version, m.OS, m.Arch, m.URL, m.SHA256)))
	return m
}

func TestVerifyManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	m := signed(t, priv, api.Manifest{Version: "1.2.0", OS: "darwin", Arch: "arm64", URL: "https://cdn/x.zip", SHA256: "ab"})
	if err := VerifyManifest(m, pubB64); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	m.URL = "https://evil/x.zip"
	if VerifyManifest(m, pubB64) == nil {
		t.Fatal("tampered URL accepted")
	}
	if VerifyManifest(m, "") == nil {
		t.Fatal("accepted without a release key")
	}
}

func TestInstallVerifiesHashAndAppliesOnce(t *testing.T) {
	payload := []byte("new agent build")
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer srv.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	var applied string
	u := New(Options{DeviceID: "d", Version: "1.0.0", ReleasePubKey: base64.StdEncoding.EncodeToString(pub), DataDir: dir,
		Apply: func(p string, _ api.Manifest) (string, error) { applied = p; return "/prev", nil }})
	m := signed(t, priv, api.Manifest{Version: "1.1.0", OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srv.URL + "/a.bin",
		SHA256: hex.EncodeToString(sum[:]), RolloutPct: 100})
	if err := u.Install(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(applied); string(b) != string(payload) {
		t.Fatalf("applied payload %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, markerName)); err != nil {
		t.Fatal("no rollback marker written")
	}

	bad := signed(t, priv, api.Manifest{Version: "1.2.0", OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srv.URL + "/b.bin",
		SHA256: hex.EncodeToString(make([]byte, 32)), RolloutPct: 100})
	applied = ""
	if err := u.Install(context.Background(), bad); err == nil || applied != "" {
		t.Fatalf("hash mismatch installed: err=%v", err)
	}
	notCohort := signed(t, priv, api.Manifest{Version: "1.2.0", OS: runtime.GOOS, Arch: runtime.GOARCH, URL: srv.URL, SHA256: "00", RolloutPct: 0})
	if err := u.Install(context.Background(), notCohort); err == nil {
		t.Fatal("installed outside the rollout cohort")
	}
}

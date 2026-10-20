package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func signedRequest(priv ed25519.PrivateKey, home, version string) Request {
	m := ReqManifest{Version: version, OS: "darwin", Arch: "universal", SHA256: strings.Repeat("0", 64), Size: 10}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, proto.ReleaseMessage(m.Version, m.OS, m.Arch, m.SHA256)))
	return Request{V: 1, Op: OpInstall, ID: "r1", Manifest: &m,
		Payload: home + "/Library/Application Support/RemoteAgent/updates/remote-agent-" + version + ".pkg"}
}

func TestValidateRequest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	const home = "/Users/ann"
	ok := signedRequest(priv, home, "0.3.1")
	with := func(f func(*Request)) Request {
		r := ok
		m := *ok.Manifest
		r.Manifest = &m
		f(&r)
		return r
	}
	cases := []struct {
		name      string
		req       Request
		installed string
		want      string
	}{
		{"valid install", ok, "0.3.0", ""},
		{"valid healthy", Request{V: 1, Op: OpHealthy, ID: "r2", Version: "0.3.1"}, "0.3.1", ""},
		{"wrong protocol version", with(func(r *Request) { r.V = 2 }), "0.3.0", "request version 2"},
		{"bad id", with(func(r *Request) { r.ID = "../x" }), "0.3.0", `bad request id "../x"`},
		{"unknown op", with(func(r *Request) { r.Op = "exec" }), "0.3.0", `unknown op "exec"`},
		{"install without manifest", with(func(r *Request) { r.Manifest = nil }), "0.3.0", "install request without a manifest"},
		{"not newer", ok, "0.3.1", "0.3.1 is not newer than the installed 0.3.1"},
		{"downgrade", ok, "0.4.0", "0.3.1 is not newer than the installed 0.4.0"},
		{"not for macOS", with(func(r *Request) { r.Manifest.OS = "windows" }), "0.3.0", "release is for windows/universal"},
		{"oversize", with(func(r *Request) { r.Manifest.Size = MaxPayload + 1 }), "0.3.0", "release size 536870913 out of range"},
		{"signed with another key", signedRequest(other, home, "0.3.1"), "0.3.0", "release signature does not verify"},
		{"tampered version", with(func(r *Request) {
			r.Manifest.Version = "0.3.2"
			r.Payload = home + "/Library/Application Support/RemoteAgent/updates/remote-agent-0.3.2.pkg"
		}), "0.3.0", "release signature does not verify"},
		{"payload in another home", with(func(r *Request) {
			r.Payload = "/Users/bob/Library/Application Support/RemoteAgent/updates/remote-agent-0.3.1.pkg"
		}), "0.3.0", "payload must be " + home + "/Library/Application Support/RemoteAgent/updates/remote-agent-0.3.1.pkg"},
		{"payload path traversal", with(func(r *Request) {
			r.Payload = home + "/Library/Application Support/RemoteAgent/updates/../../../../../../etc/remote-agent-0.3.1.pkg"
		}), "0.3.0", "payload must be " + home + "/Library/Application Support/RemoteAgent/updates/remote-agent-0.3.1.pkg"},
		{"payload with another name", with(func(r *Request) {
			r.Payload = home + "/Library/Application Support/RemoteAgent/updates/x.pkg"
		}), "0.3.0", "payload must be " + home + "/Library/Application Support/RemoteAgent/updates/remote-agent-0.3.1.pkg"},
		{"no requester home", ok, "0.3.0", "no home directory for the requesting user"},
		{"healthy with a bad version", Request{V: 1, Op: OpHealthy, ID: "r2", Version: "x"}, "0.3.1", `bad version "x"`},
	}
	for _, c := range cases {
		h := home
		if c.name == "no requester home" {
			h = ""
		}
		err := ValidateRequest(c.req, c.installed, pubB64, h)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("%s: ValidateRequest = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStartupAction(t *testing.T) {
	cases := []struct {
		name     string
		st       DaemonStatus
		running  string
		reported string
		want     string
	}{
		{"installed and running", DaemonStatus{ID: "a", State: DaemonInstalled, From: "0.3.0", To: "0.3.1"}, "0.3.1", "", startupHealthy},
		{"installed but an older build runs", DaemonStatus{ID: "a", State: DaemonInstalled, From: "0.3.0", To: "0.3.1"}, "0.3.0", "", ""},
		{"already healthy", DaemonStatus{ID: "a", State: DaemonHealthy, To: "0.3.1"}, "0.3.1", "", ""},
		{"failed, not reported", DaemonStatus{ID: "a", State: DaemonFailed, To: "0.3.1"}, "0.3.0", "", startupReport},
		{"rolled back, not reported", DaemonStatus{ID: "b", State: DaemonRolledBack, To: "0.3.1"}, "0.3.0", "a", startupReport},
		{"failure already reported", DaemonStatus{ID: "a", State: DaemonFailed, To: "0.3.1"}, "0.3.0", "a", ""},
		{"still installing", DaemonStatus{ID: "a", State: DaemonInstalling, To: "0.3.1"}, "0.3.0", "", ""},
		// postinstall starts the new agent before the daemon records the result
		{"new build started mid-install", DaemonStatus{ID: "a", State: DaemonInstalling, To: "0.3.1"}, "0.3.1", "", startupHealthy},
	}
	for _, c := range cases {
		if got := startupAction(c.st, c.running, c.reported); got != c.want {
			t.Errorf("%s: startupAction = %q, want %q", c.name, got, c.want)
		}
	}
}

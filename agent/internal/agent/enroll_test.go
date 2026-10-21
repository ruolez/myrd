package agent

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/config"
	"github.com/ruolez/remotectl/agent/internal/identity"
	"github.com/ruolez/remotectl/agent/internal/platform/fake"
)

func TestWaitingAgentAdoptsCommandLineEnrollment(t *testing.T) {
	enrollPoll = 10 * time.Millisecond
	dir := t.TempDir()
	a, err := New(Config{Settings: config.Settings{DataDir: dir, ServerURL: "https://myrd.site"},
		Backend: fake.New(), Keystore: identity.FileKeystore{Path: dir + "/device.key"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.awaitEnrollment(ctx) }()

	// What `remote-agent enroll` leaves behind in the same data dir.
	want := config.State{ServerURL: "https://myrd.site", DeviceID: "8c1f0e2a-device", Name: "New Mac"}
	if err := config.Save(dir, want); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("awaitEnrollment: %v", err)
	}
	if got := a.State(); got != want {
		t.Fatalf("state %+v, want %+v", got, want)
	}
}

func TestEnrollNamesTheFirewallWhenTheServerNeverAnswers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() // accepts and never answers, like a dropped connection
	server := "http://" + ln.Addr().String()
	dir := t.TempDir()
	a, err := New(Config{Settings: config.Settings{DataDir: dir, ServerURL: server},
		Backend: fake.New(), Keystore: identity.FileKeystore{Path: dir + "/device.key"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = a.Enroll(ctx, server, "AB12-CD34")
	if err == nil || !strings.HasPrefix(err.Error(), "could not reach "+server+" (") ||
		!strings.HasSuffix(err.Error(), "that no firewall (such as Little Snitch) blocks RemoteAgent") {
		t.Fatalf("err = %v", err)
	}
	if a.State().Enrolled() {
		t.Fatal("enrolled without an answer")
	}
}

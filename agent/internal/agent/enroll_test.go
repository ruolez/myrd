package agent

import (
	"context"
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

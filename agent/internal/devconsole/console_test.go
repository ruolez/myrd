package devconsole

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/pins"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/session"
)

func newConsole(t *testing.T) (*Console, *httptest.Server) {
	t.Helper()
	ps, _ := pins.Open(filepath.Join(t.TempDir(), "p.json"))
	c := New(Options{Pins: ps, Status: func() Status { return Status{Enrolled: true} }})
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(srv.Close)
	return c, srv
}

func waitPrompt(t *testing.T, c *Console) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		c.mu.Lock()
		for id := range c.prompts {
			c.mu.Unlock()
			return id
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("prompt never registered")
	return ""
}

func TestPromptAnsweredFromPage(t *testing.T) {
	c, srv := newConsole(t)
	got := make(chan platform.Consent, 1)
	go func() {
		a, _ := c.Prompt(context.Background(), session.ConsentRequest{Title: "t", Label: "Chrome", NewKey: true})
		got <- a
	}()
	id := waitPrompt(t, c)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/prompts/"+id, strings.NewReader(`{"answer":"remember"}`))
	req.Header.Set("X-Device-Console", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("answer: %v %v", err, resp.StatusCode)
	}
	if a := <-got; a != platform.ConsentRemember {
		t.Fatalf("consent = %v", a)
	}
}

func TestPromptTimesOutAsDeny(t *testing.T) {
	c, _ := newConsole(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if a, err := c.Prompt(ctx, session.ConsentRequest{}); a != platform.ConsentDeny || err == nil {
		t.Fatalf("got %v %v", a, err)
	}
}

func TestPostWithoutHeaderRejected(t *testing.T) {
	_, srv := newConsole(t)
	resp, err := http.Post(srv.URL+"/api/session/end", "application/json", nil)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %v %v", resp.StatusCode, err)
	}
}

func TestIndexServed(t *testing.T) {
	_, srv := newConsole(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("index: %v %v", err, resp.StatusCode)
	}
}

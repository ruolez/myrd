package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

func TestPowerActions(t *testing.T) {
	ok := func() error { return nil }
	cases := []struct {
		name string
		o    Options
		want []string
	}{
		{"neither", Options{}, nil},
		{"device only", Options{Restart: ok}, []string{proto.PowerRestart}},
		{"both", Options{Restart: ok, RestartAgent: ok}, []string{proto.PowerRestart, proto.PowerRestartAgent}},
	}
	for _, c := range cases {
		got := powerActions(c.o)
		offered := slices.Contains(helloFeatures(c.o, false, false), proto.FeaturePower)
		if !slices.Equal(got, c.want) || offered != (len(c.want) > 0) {
			t.Errorf("%s: actions %v feature %v, want %v", c.name, got, offered, c.want)
		}
	}
}

func (h *harness) sendPower(t *testing.T, id int, action string) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"t": "power", "id": id, "action": action})
	if err := h.control.SendText(string(b)); err != nil {
		t.Fatal(err)
	}
	for {
		if m := h.waitControl(t, "power"); m["id"] == float64(id) {
			return m
		}
	}
}

func TestLoopbackPowerRestartsAndEndsTheSession(t *testing.T) {
	cases := []struct {
		action, reason string
	}{
		{proto.PowerRestart, proto.EndDeviceRestart},
		{proto.PowerRestartAgent, proto.EndAgentRestart},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			var device, agent atomic.Int32
			h := newHarness(t, platform.ConsentOnce, func(o *Options) {
				o.Restart = func() error { device.Add(1); return nil }
				o.RestartAgent = func() error { agent.Add(1); return nil }
			})
			h.offer(false)
			if err := <-h.answerOK; err != nil {
				t.Fatal(err)
			}
			hello := h.waitControl(t, "hello")
			if fmt.Sprint(hello["power"]) != "[restart restart_agent]" {
				t.Fatalf("hello power = %v", hello["power"])
			}
			if m := h.sendPower(t, 1, c.action); m["ok"] != true {
				t.Fatalf("power = %v", m)
			}
			waitFor(t, "session end", func() bool { _, _, ends := h.snapshot(); return len(ends) == 1 })
			_, _, ends := h.snapshot()
			got := [3]any{ends[0].Reason, device.Load(), agent.Load()}
			want := [3]any{c.reason, int32(0), int32(0)}
			if c.action == proto.PowerRestart {
				want[1] = int32(1)
			} else {
				want[2] = int32(1)
			}
			if got != want {
				t.Fatalf("reason, device restarts, agent restarts = %v, want %v", got, want)
			}
		})
	}
}

func TestLoopbackPowerRefusals(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce, func(o *Options) {
		o.Restart = func() error { return errors.New("no shutdown privilege") }
	})
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	h.waitControl(t, "hello")
	got := []any{h.sendPower(t, 1, proto.PowerRestart)["code"], h.sendPower(t, 2, proto.PowerRestartAgent)["code"],
		h.sendPower(t, 3, "shutdown")["code"]}
	if want := []any{powerFailed, powerUnsupported, powerUnsupported}; !slices.Equal(got, want) {
		t.Fatalf("codes %v, want %v", got, want)
	}
	time.Sleep(100 * time.Millisecond)
	if _, _, ends := h.snapshot(); len(ends) != 0 {
		t.Fatalf("a refused request ended the session: %v", ends)
	}
}

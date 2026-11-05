//go:build !windows

package session

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/agent/internal/terminal"
	"github.com/ruolez/remotectl/proto"
)

func withTerminal(host terminal.Host, allowed *bool) func(*Options) {
	return func(o *Options) {
		o.Terminal = host
		o.TerminalAllowed = func() bool { return *allowed }
	}
}

func (h *harness) waitTerm(t *testing.T, kind string) map[string]any {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m := <-h.termText:
			if m["t"] == kind {
				return m
			}
			if m["t"] == "error" {
				t.Fatalf("term error waiting for %s: %v", kind, m)
			}
		case <-timeout:
			t.Fatalf("no term %q", kind)
		}
	}
}

// waitOutput reads output frames of term until they contain want.
func (h *harness) waitOutput(t *testing.T, term uint16, want string) string {
	t.Helper()
	var got strings.Builder
	timeout := time.After(10 * time.Second)
	for !strings.Contains(got.String(), want) {
		select {
		case b := <-h.termData:
			id, p, err := terminal.DecodeData(terminal.TypeOutput, b)
			if err != nil {
				t.Fatalf("output frame: %v", err)
			}
			if id == term {
				got.Write(p)
			}
		case <-timeout:
			t.Fatalf("output %q lacks %q", got.String(), want)
		}
	}
	return got.String()
}

func (h *harness) terminalRecords() []proto.TerminalRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []proto.TerminalRecord
	for _, m := range h.termRecs {
		out = append(out, m.Terminals...)
	}
	return out
}

// A terminal session runs a real shell end to end, needs no screen
// permission, sends no video, refuses the desktop channels, and its shell
// outlives it for the next session.
func TestLoopbackTerminalSession(t *testing.T) {
	allowed := true
	host := terminal.NewLocal(slog.New(slog.DiscardHandler))
	defer host.Close()
	h := newHarness(t, platform.ConsentOnce, withTerminal(host, &allowed))
	h.backend.Perms = platform.Permissions{} // no Screen Recording, no Accessibility
	h.mode = proto.ModeTerminal
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	if hello := h.waitControl(t, "hello"); !strings.Contains(fmt.Sprint(hello["features"]), "terminal") {
		t.Fatalf("hello features %v lack terminal", hello["features"])
	}
	h.prompter.mu.Lock()
	reqs := append([]ConsentRequest(nil), h.prompter.reqs...)
	h.prompter.mu.Unlock()
	if len(reqs) != 1 || !strings.Contains(reqs[0].Message, "terminal") {
		t.Fatalf("consent prompts %+v: want one that says terminal", reqs)
	}
	waitFor(t, "input channel closed", func() bool { return h.input.ReadyState() == webrtc.DataChannelStateClosed })
	waitFor(t, "term channel open", func() bool { return h.term.ReadyState() == webrtc.DataChannelStateOpen })

	_ = h.term.SendText(`{"t":"open","id":1,"cols":77,"rows":21}`)
	opened := h.waitTerm(t, "opened")
	term := uint16(opened["term"].(float64))
	_ = h.term.Send(terminal.EncodeData(terminal.TypeInput, term, []byte("stty size; echo out-$((6*7))\r")))
	out := h.waitOutput(t, term, "out-42")
	if !strings.Contains(out, "21 77") {
		t.Fatalf("output %q lacks the window size", out)
	}
	if h.backend.Capturing() {
		t.Fatal("a terminal session started screen capture")
	}
	select {
	case <-h.rtpPackets:
		t.Fatal("video sent in a terminal session")
	default:
	}

	end, _ := proto.Encode(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sessionID, Reason: proto.EndUserHangup})
	h.mgr.HandleSignal(proto.TypeSessionEnd, end)
	waitFor(t, "session.end", func() bool { _, _, e := h.snapshot(); return len(e) > 0 })
	h.mu.Lock()
	order := strings.Join(h.signalOrder, ",")
	h.mu.Unlock()
	if !strings.HasPrefix(order, proto.TypeSessionTerminal) || !strings.HasSuffix(order, proto.TypeSessionEnd) {
		t.Fatalf("signal order %s: terminal records must precede session.end", order)
	}
	recs := h.terminalRecords()
	if len(recs) != 2 || recs[0].Action != proto.TermOpened || recs[1].Action != proto.TermClosed ||
		recs[1].Reason != terminal.ReasonSessionEnded || recs[1].BytesIn == 0 || recs[1].BytesOut == 0 {
		t.Fatalf("records %+v", recs)
	}

	srcs, _ := host.List()
	if len(srcs) != 1 || srcs[0].Kind != proto.TermSourceShell || srcs[0].Attached != 0 {
		t.Fatalf("kept shells after the session: %+v", srcs)
	}
}

func TestLoopbackTermChannelClosedWhenNotAllowed(t *testing.T) {
	allowed := false
	host := terminal.NewLocal(slog.New(slog.DiscardHandler))
	defer host.Close()
	h := newHarness(t, platform.ConsentOnce, withTerminal(host, &allowed))
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	if hello := h.waitControl(t, "hello"); strings.Contains(fmt.Sprint(hello["features"]), "terminal") {
		t.Fatalf("hello features %v offer terminal while not allowed", hello["features"])
	}
	waitFor(t, "term channel closed", func() bool { return h.term.ReadyState() == webrtc.DataChannelStateClosed })
	if h.input.ReadyState() != webrtc.DataChannelStateOpen {
		t.Fatal("desktop session lost its input channel")
	}
}

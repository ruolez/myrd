package app

import (
	"reflect"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func intp(i int) *int { return &i }

func TestValidTerminalRecord(t *testing.T) {
	opened := proto.TerminalRecord{Term: 1, Action: proto.TermOpened, Source: proto.TermSourceShell, Title: "zsh", Result: "ok"}
	closed := proto.TerminalRecord{Term: 1, Action: proto.TermClosed, Source: proto.TermSourceShell, Title: "zsh", Result: "ok",
		Reason: "exited", ExitCode: intp(0), BytesIn: 10, BytesOut: 200, DurationMS: 5000}
	failed := proto.TerminalRecord{Action: proto.TermAttached, Source: "unknown", Title: "tmux:gone", Result: "error", Error: "not_found"}
	with := func(r proto.TerminalRecord, f func(*proto.TerminalRecord)) proto.TerminalRecord { f(&r); return r }
	cases := []struct {
		name string
		r    proto.TerminalRecord
		want bool
	}{
		{"opened", opened, true},
		{"closed", closed, true},
		{"failed attach with unknown source", failed, true},
		{"ok with an error code", with(opened, func(r *proto.TerminalRecord) { r.Error = "denied" }), false},
		{"error without a code", with(failed, func(r *proto.TerminalRecord) { r.Error = "" }), false},
		{"error code not snake_case", with(failed, func(r *proto.TerminalRecord) { r.Error = "Not Found" }), false},
		{"unknown result", with(opened, func(r *proto.TerminalRecord) { r.Result = "maybe" }), false},
		{"unknown action", with(opened, func(r *proto.TerminalRecord) { r.Action = "typed" }), false},
		{"unknown source on success", with(opened, func(r *proto.TerminalRecord) { r.Source = "unknown" }), false},
		{"opened with byte counts", with(opened, func(r *proto.TerminalRecord) { r.BytesOut = 1 }), false},
		{"closed without reason", with(closed, func(r *proto.TerminalRecord) { r.Reason = "" }), false},
		{"closed with unknown reason", with(closed, func(r *proto.TerminalRecord) { r.Reason = "bored" }), false},
		{"negative bytes", with(closed, func(r *proto.TerminalRecord) { r.BytesIn = -1 }), false},
		{"title too long", with(opened, func(r *proto.TerminalRecord) { r.Title = string(make([]byte, 513)) }), false},
		{"title not utf-8", with(opened, func(r *proto.TerminalRecord) { r.Title = "\xff" }), false},
	}
	for _, c := range cases {
		if got := validTerminalRecord(c.r); got != c.want {
			t.Errorf("%s: valid = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTerminalEventAndMeta(t *testing.T) {
	cases := []struct {
		r     proto.TerminalRecord
		event string
		meta  map[string]any
	}{
		{proto.TerminalRecord{Term: 2, Action: proto.TermOpened, Source: "shell", Title: "PowerShell", Admin: true, Result: "ok"},
			"terminal.opened", map[string]any{"term": uint16(2), "source": "shell", "title": "PowerShell", "admin": true}},
		{proto.TerminalRecord{Action: proto.TermOpened, Source: "shell", Title: "zsh", Result: "error", Error: "spawn_failed"},
			"terminal.open_failed", map[string]any{"term": uint16(0), "source": "shell", "title": "zsh", "error": "spawn_failed"}},
		{proto.TerminalRecord{Term: 3, Action: proto.TermAttached, Source: "tmux", Title: "main", Result: "ok"},
			"terminal.attached", map[string]any{"term": uint16(3), "source": "tmux", "title": "main"}},
		{proto.TerminalRecord{Term: 3, Action: proto.TermClosed, Source: "tmux", Title: "main", Result: "ok", Reason: "killed",
			ExitCode: intp(-1), BytesIn: 4, BytesOut: 9, DurationMS: 7},
			"terminal.closed", map[string]any{"term": uint16(3), "source": "tmux", "title": "main", "reason": "killed",
				"bytes_in": int64(4), "bytes_out": int64(9), "duration_ms": int64(7), "exit_code": -1}},
	}
	for _, c := range cases {
		if got := terminalEvent(c.r); got != c.event {
			t.Errorf("event %q, want %q", got, c.event)
		}
		if got := terminalMeta(c.r); !reflect.DeepEqual(got, c.meta) {
			t.Errorf("%s meta %#v, want %#v", c.event, got, c.meta)
		}
	}
}

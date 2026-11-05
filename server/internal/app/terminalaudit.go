package app

import (
	"unicode/utf8"

	"github.com/ruolez/remotectl/proto"
)

var terminalSources = map[string]bool{proto.TermSourceShell: true, proto.TermSourceTmux: true,
	proto.TermSourceScreen: true, proto.TermSourceConsole: true, proto.TermSourceApp: true}

var terminalReasons = map[string]bool{"detached": true, "exited": true, "killed": true, "session_ended": true}

// validTerminalRecord checks a terminal record reported by an agent
// (session.terminal) before it is written to the audit log.
func validTerminalRecord(r proto.TerminalRecord) bool {
	ok := r.Result == "ok"
	switch {
	case !ok && r.Result != "error":
	case len(r.Title) > 512 || !utf8.ValidString(r.Title):
	case !isErrorCode(r.Error) || ok && r.Error != "" || !ok && r.Error == "":
	case r.BytesIn < 0 || r.BytesOut < 0 || r.DurationMS < 0:
	case r.Action == proto.TermOpened || r.Action == proto.TermAttached:
		// A failed attempt may not know the source kind ("unknown").
		return (terminalSources[r.Source] || !ok && r.Source == "unknown") && r.Reason == "" && r.ExitCode == nil &&
			r.BytesIn == 0 && r.BytesOut == 0
	case r.Action == proto.TermClosed:
		return ok && terminalSources[r.Source] && terminalReasons[r.Reason]
	}
	return false
}

// terminalEvent is the audit event of a valid record.
func terminalEvent(r proto.TerminalRecord) string {
	ok := r.Result == "ok"
	switch r.Action {
	case proto.TermOpened:
		return pick(ok, "terminal.opened", "terminal.open_failed")
	case proto.TermAttached:
		return pick(ok, "terminal.attached", "terminal.attach_failed")
	}
	return "terminal.closed"
}

func terminalMeta(r proto.TerminalRecord) map[string]any {
	m := map[string]any{"term": r.Term, "source": r.Source, "title": r.Title}
	if r.Admin {
		m["admin"] = true
	}
	if r.Error != "" {
		m["error"] = r.Error
	}
	if r.Action == proto.TermClosed {
		m["reason"], m["bytes_in"], m["bytes_out"], m["duration_ms"] = r.Reason, r.BytesIn, r.BytesOut, r.DurationMS
		if r.ExitCode != nil {
			m["exit_code"] = *r.ExitCode
		}
	}
	return m
}

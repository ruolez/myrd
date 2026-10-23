package session

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// Remote restart (control `power`): the controller asks for a restart of the
// device or the agent. It travels on the session's control channel, so only a
// browser this device approved can ask; the server cannot.

// powerGrace lets the reply and the `bye` reach the browser before the
// session ends.
const powerGrace = 300 * time.Millisecond

// Reply codes of a refused or failed power request.
const (
	powerUnsupported = "unsupported"
	powerFailed      = "failed"
)

type powerRequest struct {
	ID     int64  `json:"id"`
	Action string `json:"action"`
}

// powerActions lists the actions Options offers, in hello `power`.
func powerActions(o Options) []string {
	var a []string
	if o.Restart != nil {
		a = append(a, proto.PowerRestart)
	}
	if o.RestartAgent != nil {
		a = append(a, proto.PowerRestartAgent)
	}
	return a
}

// onPower answers control `power`: schedules the action, replies, then ends
// the session with a reason that tells the browser what is happening.
func (s *sess) onPower(raw []byte) {
	var req powerRequest
	_ = json.Unmarshal(raw, &req)
	reply := func(code string) {
		msg := map[string]any{"t": "power", "id": req.ID, "ok": code == ""}
		if code != "" {
			msg["code"] = code
		}
		s.media.sendControl(msg)
	}
	var run func() error
	var reason string
	switch req.Action {
	case proto.PowerRestart:
		run, reason = s.m.o.Restart, proto.EndDeviceRestart
	case proto.PowerRestartAgent:
		run, reason = s.m.o.RestartAgent, proto.EndAgentRestart
	}
	if run == nil {
		reply(powerUnsupported)
		return
	}
	if err := run(); err != nil {
		s.log.Error("power: refused", "action", req.Action, "err", err)
		reply(powerFailed)
		return
	}
	s.log.Info("power: scheduled", "action", req.Action, "user", s.userID)
	reply("")
	go func() {
		s.m.sleep(context.Background(), powerGrace)
		s.end(reason, true)
	}()
}

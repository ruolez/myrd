package winsvc

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ruolez/remotectl/agent/internal/vdd"
	"github.com/ruolez/remotectl/proto"
)

// vdJobs runs the server's virtual display requests, one at a time.
var vdJobs vdRunner

type vdRunner struct{ busy atomic.Bool }

// vdReportGap keeps the job's result apart from its start: the server drops
// a device.info that follows the last one within 5 s.
const vdReportGap = 6 * time.Second

func (r *vdRunner) run(action string, p *Proxy, log *slog.Logger) {
	var state, done string
	var do func(*slog.Logger) error
	switch action {
	case proto.VirtualDisplayInstall:
		state, done, do = proto.VDriverInstalling, proto.VDriverInstalled, vdd.Install
	case proto.VirtualDisplayRemove:
		state, done, do = proto.VDriverRemoving, proto.VDriverAbsent, vdd.Remove
	default:
		log.Warn("vdd: unknown request", "action", action)
		return
	}
	if !r.busy.CompareAndSwap(false, true) {
		log.Info("vdd: a request is already running", "action", action)
		return
	}
	defer r.busy.Store(false)
	log.Info("vdd: requested by the server", "action", action)
	started := time.Now()
	p.SetVirtualDisplayJob(vdd.Job{Driver: state}, false)
	err := do(log)
	time.Sleep(vdReportGap - time.Since(started))
	if err != nil {
		log.Error("vdd: request failed", "action", action, "err", err)
		p.SetVirtualDisplayJob(vdd.Job{Driver: proto.VDriverFailed, Err: err.Error()}, false)
		return
	}
	p.SetVirtualDisplayJob(vdd.Job{Driver: done}, true)
}

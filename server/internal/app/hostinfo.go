package app

import (
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/ruolez/remotectl/proto"
	"github.com/ruolez/remotectl/server/internal/store"
)

// minInfoInterval drops device.info reports that come faster than this.
const minInfoInterval = 5 * time.Second

// agentFeatures is what the server accepts from agents (hello_ack.features).
var agentFeatures = []string{proto.FeatureDeviceInfo, proto.FeaturePreview}

// cleanHostInfo bounds a report before it is stored and shown: text is
// truncated, disks are capped, CPU is clamped, memory used never
// exceeds the total and a virtual display in an unknown state is dropped.
// The agent's own memory passes as reported.
func cleanHostInfo(in proto.HostInfo) proto.HostInfo {
	t := func(s string) string { return cleanText(s, proto.MaxHostInfoText) }
	out := proto.HostInfo{
		OSName: t(in.OSName), OSVersion: t(in.OSVersion), OSBuild: t(in.OSBuild),
		Locked: in.Locked, UptimeS: max(in.UptimeS, 0), CPUCount: min(max(in.CPUCount, 0), 4096),
		MemTotal: in.MemTotal, MemUsed: min(in.MemUsed, in.MemTotal), Disks: []proto.DiskInfo{},
	}
	if in.User != nil {
		u := t(*in.User)
		out.User = &u
	}
	if in.IdleS != nil {
		v := max(*in.IdleS, 0)
		out.IdleS = &v
	}
	if in.CPUPct != nil && !math.IsNaN(*in.CPUPct) {
		v := math.Round(min(max(*in.CPUPct, 0), 100)*10) / 10
		out.CPUPct = &v
	}
	if in.Agent != nil {
		a := *in.Agent
		out.Agent = &a
	}
	if v := in.VirtualDisplay; v != nil && slices.Contains(proto.VDriverStates, v.Driver) {
		out.VirtualDisplay = &proto.VirtualDisplayInfo{Supported: v.Supported, Headless: v.Headless,
			Driver: v.Driver, Active: v.Active, Error: t(v.Error)}
	}
	for _, d := range in.Disks {
		if len(out.Disks) == proto.MaxHostDisks {
			break
		}
		if d.Total == 0 {
			continue
		}
		out.Disks = append(out.Disks, proto.DiskInfo{Name: t(d.Name), Mount: t(d.Mount), FS: t(d.FS),
			Total: d.Total, Free: min(d.Free, d.Total)})
	}
	return out
}

func (h *Hub) onDeviceInfo(ac *agentConn, raw []byte) {
	var m proto.HostInfoReport
	if json.Unmarshal(raw, &m) != nil {
		h.sendErr(ac.wsConn, "", "", proto.ErrBadRequest, "invalid device.info")
		return
	}
	if time.Since(ac.lastInfo) < minInfoInterval {
		return
	}
	ac.lastInfo = time.Now()
	info := cleanHostInfo(m.Info)
	b, err := json.Marshal(info)
	if err != nil {
		return
	}
	ctx, cancel := dbctx()
	defer cancel()
	at, err := store.SetDeviceInfo(ctx, h.app.pool, ac.deviceID, b)
	if err != nil {
		h.app.log.Error("device info", "err", err)
		return
	}
	push := proto.HostInfoPush{DeviceID: ac.deviceID, Info: info, At: at.UTC().Format(time.RFC3339)}
	for _, bc := range h.browsersOf(ac.userID) {
		h.sendMsg(bc.wsConn, proto.TypeDeviceInfo, push)
	}
}

// Package vdd installs and drives the virtual display on Windows devices
// without a monitor (docs/PROTOCOL.md §3 "Virtual display"): Virtual Display
// Driver 25.7.23 (github.com/VirtualDrivers/Virtual-Display-Driver, MIT,
// driver/LICENSE.txt), an Indirect Display driver signed by the SignPath
// Foundation. Its monitor reports the hardware id MTT1337; its settings
// (resolutions, monitor count) are read from VDDPATH\vdd_settings.xml.
package vdd

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ruolez/remotectl/proto"
)

// Mode is a resolution the virtual monitor offers.
type Mode struct{ W, H int }

// Modes are the virtual monitor's resolutions; Windows starts with the first.
var Modes = []Mode{{1920, 1080}, {3840, 2160}, {2560, 1600}, {2560, 1440}, {1920, 1200}, {1680, 1050},
	{1600, 900}, {1440, 900}, {1366, 768}, {1280, 800}, {1280, 720}, {1024, 768}}

// SettingsXML is vdd_settings.xml for one virtual monitor offering modes at
// 60 Hz on the default render adapter.
func SettingsXML(modes []Mode) []byte {
	var b bytes.Buffer
	b.WriteString("<?xml version='1.0' encoding='utf-8'?>\n<vdd_settings>\n")
	b.WriteString("    <monitors>\n        <count>1</count>\n    </monitors>\n")
	b.WriteString("    <gpu>\n        <friendlyname>default</friendlyname>\n    </gpu>\n")
	b.WriteString("    <global>\n        <g_refresh_rate>60</g_refresh_rate>\n    </global>\n")
	b.WriteString("    <resolutions>\n")
	for _, m := range modes {
		fmt.Fprintf(&b, "        <resolution>\n            <width>%d</width>\n            <height>%d</height>\n"+
			"            <refresh_rate>60</refresh_rate>\n        </resolution>\n", m.W, m.H)
	}
	b.WriteString("    </resolutions>\n")
	b.WriteString("    <options>\n        <CustomEdid>false</CustomEdid>\n        <PreventSpoof>false</PreventSpoof>\n" +
		"        <EdidCeaOverride>false</EdidCeaOverride>\n        <HardwareCursor>true</HardwareCursor>\n" +
		"        <SDR10bit>false</SDR10bit>\n        <HDRPlus>false</HDRPlus>\n        <logging>false</logging>\n" +
		"        <debuglogging>false</debuglogging>\n    </options>\n</vdd_settings>\n")
	return b.Bytes()
}

// MonitorID is the hardware id of the virtual monitor.
const MonitorID = "MTT1337"

// Classify reads monitor device ids (EnumDisplayDevices: MONITOR\<hwid>\...):
// headless when none is a real monitor (only Windows' generic
// Default_Monitor or the virtual one), active when the virtual one is there.
func Classify(ids []string) (headless, active bool) {
	headless = true
	for _, id := range ids {
		parts := strings.Split(id, `\`)
		if len(parts) < 2 || !strings.EqualFold(parts[0], "MONITOR") {
			continue
		}
		switch hw := parts[1]; {
		case strings.EqualFold(hw, MonitorID):
			active = true
		case strings.EqualFold(hw, "Default_Monitor"), hw == "":
		default:
			headless = false
		}
	}
	return headless, active
}

// Supported reports whether the bundled driver runs on this Windows: x64,
// Windows 10 1803 (build 17134, UMDF 2.25) or later; Server 2019 is 17763.
func Supported(major, build uint32, arch string) bool {
	return arch == "amd64" && major >= 10 && build >= 17134
}

// Job is the service's install or removal in progress, or how the last one
// failed; zero when idle.
type Job struct {
	Driver string // proto.VDriverInstalling, VDriverRemoving or VDriverFailed
	Err    string
}

// Apply lays the job over the helper's report: the helper sees the desktop,
// the service runs the installer.
func (j Job) Apply(h proto.HostInfo) proto.HostInfo {
	if j.Driver == "" || h.VirtualDisplay == nil {
		return h
	}
	v := *h.VirtualDisplay
	v.Driver, v.Error = j.Driver, j.Err
	h.VirtualDisplay = &v
	return h
}

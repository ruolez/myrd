package vdd

import (
	"encoding/xml"
	"reflect"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

func TestSettingsXML(t *testing.T) {
	var got struct {
		Count int    `xml:"monitors>count"`
		GPU   string `xml:"gpu>friendlyname"`
		Modes []struct {
			W  int `xml:"width"`
			H  int `xml:"height"`
			Hz int `xml:"refresh_rate"`
		} `xml:"resolutions>resolution"`
		Refresh []int `xml:"global>g_refresh_rate"`
	}
	if err := xml.Unmarshal(SettingsXML(Modes), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.GPU != "default" || !reflect.DeepEqual(got.Refresh, []int{60}) || len(got.Modes) != len(Modes) {
		t.Fatalf("settings = %+v", got)
	}
	first := got.Modes[0]
	if [3]int{first.W, first.H, first.Hz} != [3]int{1920, 1080, 60} {
		t.Fatalf("first mode %+v, want 1920x1080 at 60 Hz (the one Windows starts with)", first)
	}
}

func TestClassify(t *testing.T) {
	const generic = `MONITOR\Default_Monitor\{4d36e96e-e325-11ce-bfc1-08002be10318}\0000`
	const virtual = `MONITOR\MTT1337\{4d36e96e-e325-11ce-bfc1-08002be10318}\0001`
	const dell = `MONITOR\DEL40F0\{4d36e96e-e325-11ce-bfc1-08002be10318}\0002`
	cases := []struct {
		name             string
		ids              []string
		headless, active bool
	}{
		{"nothing listed", nil, true, false},
		{"generic monitor only", []string{generic}, true, false},
		{"virtual monitor", []string{generic, virtual}, true, true},
		{"real monitor", []string{dell}, false, false},
		{"real and virtual", []string{dell, virtual}, false, true},
		{"case and odd ids", []string{`monitor\mtt1337\x`, `garbage`}, true, true},
	}
	for _, c := range cases {
		h, a := Classify(c.ids)
		if h != c.headless || a != c.active {
			t.Errorf("%s: headless %v active %v, want %v %v", c.name, h, a, c.headless, c.active)
		}
	}
}

func TestSupported(t *testing.T) {
	cases := []struct {
		major, build uint32
		arch         string
		want         bool
	}{
		{10, 17763, "amd64", true}, // Server 2019
		{10, 17134, "amd64", true}, // 1803: UMDF 2.25
		{10, 16299, "amd64", false},
		{10, 26100, "arm64", false}, // the bundled driver is x64 only
		{6, 7601, "amd64", false},
	}
	for _, c := range cases {
		if got := Supported(c.major, c.build, c.arch); got != c.want {
			t.Errorf("Supported(%d, %d, %s) = %v, want %v", c.major, c.build, c.arch, got, c.want)
		}
	}
}

func TestJobApply(t *testing.T) {
	report := func() proto.HostInfo {
		return proto.HostInfo{OSName: "Windows", VirtualDisplay: &proto.VirtualDisplayInfo{Supported: true, Headless: true,
			Driver: proto.VDriverAbsent}}
	}
	cases := []struct {
		name string
		job  Job
		want *proto.VirtualDisplayInfo
	}{
		{"idle keeps the helper's view", Job{}, report().VirtualDisplay},
		{"installing", Job{Driver: proto.VDriverInstalling},
			&proto.VirtualDisplayInfo{Supported: true, Headless: true, Driver: proto.VDriverInstalling}},
		{"failed with its error", Job{Driver: proto.VDriverFailed, Err: "boom"},
			&proto.VirtualDisplayInfo{Supported: true, Headless: true, Driver: proto.VDriverFailed, Error: "boom"}},
	}
	for _, c := range cases {
		got := c.job.Apply(report())
		if !reflect.DeepEqual(got.VirtualDisplay, c.want) || got.OSName != "Windows" {
			t.Errorf("%s: %+v, want %+v", c.name, got.VirtualDisplay, c.want)
		}
	}
	if got := (Job{Driver: proto.VDriverInstalling}).Apply(proto.HostInfo{}); got.VirtualDisplay != nil {
		t.Fatalf("a report without virtual_display gained one: %+v", got.VirtualDisplay)
	}
}

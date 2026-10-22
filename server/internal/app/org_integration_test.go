package app

import (
	"net/http"
	"slices"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

type folderJSON struct {
	ID       string  `json:"id"`
	ParentID *string `json:"parent_id"`
	Name     string  `json:"name"`
	Position int     `json:"position"`
}

type orgDevice struct {
	ID              string   `json:"id"`
	FolderID        *string  `json:"folder_id"`
	TagIDs          []string `json:"tag_ids"`
	Favorite        bool     `json:"favorite"`
	Notes           string   `json:"notes"`
	LastConnectedAt *string  `json:"last_connected_at"`
	Busy            bool     `json:"busy"`
}

func (e *env) folder(c *http.Client, name string, parent *string) folderJSON {
	e.t.Helper()
	var out struct{ Folder folderJSON }
	if st := e.call(c, "POST", "/v1/folders", map[string]any{"name": name, "parent_id": parent}, &out); st != 200 {
		e.t.Fatalf("create folder %q: %d", name, st)
	}
	return out.Folder
}

func (e *env) tag(c *http.Client, name string, color int) string {
	e.t.Helper()
	var out struct{ Tag struct{ ID string } }
	if st := e.call(c, "POST", "/v1/tags", map[string]any{"name": name, "color": color}, &out); st != 200 {
		e.t.Fatalf("create tag %q: %d", name, st)
	}
	return out.Tag.ID
}

func (e *env) orgDevice(c *http.Client, id string) orgDevice {
	e.t.Helper()
	var out struct{ Device orgDevice }
	if st := e.call(c, "GET", "/v1/devices/"+id, nil, &out); st != 200 {
		e.t.Fatalf("get device: %d", st)
	}
	slices.Sort(out.Device.TagIDs)
	return out.Device
}

func TestFoldersTagsAndBulk(t *testing.T) {
	e := newEnv(t)
	admin := e.login(adminEmail, adminPass)
	other := e.createUser(admin, "other@example.com")
	d1, d2 := e.enroll(admin), e.enroll(admin)
	strangerDevice := e.enroll(other)

	office := e.folder(admin, "Office", nil)
	floor := e.folder(admin, "Floor 2", &office.ID)
	if st := e.call(admin, "POST", "/v1/folders", map[string]any{"name": "office"}, nil); st != 409 {
		t.Fatalf("duplicate top-level name (case-insensitive): %d, want 409", st)
	}
	if e.folder(admin, "Office", &floor.ID).ParentID == nil {
		t.Fatal("a nested folder may reuse a name used elsewhere")
	}

	// Cycles and foreign parents are refused.
	if st := e.call(admin, "PATCH", "/v1/folders/"+office.ID, map[string]any{"parent_id": floor.ID}, nil); st != 400 {
		t.Fatalf("move into own subfolder: %d, want 400", st)
	}
	if st := e.call(admin, "PATCH", "/v1/folders/"+office.ID, map[string]any{"parent_id": office.ID}, nil); st != 400 {
		t.Fatalf("move into itself: %d, want 400", st)
	}
	theirs := e.folder(other, "Theirs", nil)
	if st := e.call(admin, "PATCH", "/v1/folders/"+floor.ID, map[string]any{"parent_id": theirs.ID}, nil); st != 404 {
		t.Fatalf("move under another user's folder: %d, want 404", st)
	}
	if st := e.call(other, "DELETE", "/v1/folders/"+office.ID, nil, nil); st != 404 {
		t.Fatalf("delete another user's folder: %d, want 404", st)
	}

	// Depth limit: Office(1) > Floor 2(2) > Office(3) … up to maxFolderDepth.
	parent := floor.ID
	for depth := 3; depth <= maxFolderDepth; depth++ {
		parent = e.folder(admin, "L", &parent).ID
	}
	if st := e.call(admin, "POST", "/v1/folders", map[string]any{"name": "too deep", "parent_id": parent}, nil); st != 400 {
		t.Fatalf("folder past the depth limit: %d, want 400", st)
	}

	// Explicit null moves a folder to the top level.
	var moved struct{ Folder folderJSON }
	if st := e.call(admin, "PATCH", "/v1/folders/"+floor.ID, map[string]any{"parent_id": nil, "name": "Floor 2"}, &moved); st != 200 || moved.Folder.ParentID != nil {
		t.Fatalf("move to root: %d %+v", st, moved.Folder)
	}
	if st := e.call(admin, "PATCH", "/v1/folders/"+floor.ID, map[string]any{"parent_id": office.ID}, nil); st != 200 {
		t.Fatalf("move back: %d", st)
	}

	win, lab := e.tag(admin, "Windows", 3), e.tag(admin, "Lab", 7)
	if st := e.call(admin, "POST", "/v1/tags", map[string]any{"name": "windows"}, nil); st != 409 {
		t.Fatalf("duplicate tag: %d, want 409", st)
	}
	if st := e.call(admin, "POST", "/v1/tags", map[string]any{"name": "Bad", "color": tagColors}, nil); st != 400 {
		t.Fatalf("color out of range: %d, want 400", st)
	}
	theirTag := e.tag(other, "Mine", 0)

	// Per-device patch: folder, tags (full replace), favorite, notes.
	notes := "  Front desk PC, ask Dana before rebooting  "
	if st := e.call(admin, "PATCH", "/v1/devices/"+d1.id, map[string]any{"folder_id": floor.ID, "tag_ids": []string{win, lab, win},
		"favorite": true, "notes": notes}, nil); st != 200 {
		t.Fatalf("patch device: %d", st)
	}
	want := []string{win, lab}
	slices.Sort(want)
	got := e.orgDevice(admin, d1.id)
	if got.FolderID == nil || *got.FolderID != floor.ID || !slices.Equal(got.TagIDs, want) || !got.Favorite ||
		got.Notes != "Front desk PC, ask Dana before rebooting" {
		t.Fatalf("device after patch: %+v", got)
	}
	if st := e.call(admin, "PATCH", "/v1/devices/"+d1.id, map[string]any{"tag_ids": []string{theirTag}}, nil); st != 404 {
		t.Fatalf("another user's tag: %d, want 404", st)
	}
	if st := e.call(admin, "PATCH", "/v1/devices/"+d1.id, map[string]any{"folder_id": theirs.ID}, nil); st != 404 {
		t.Fatalf("another user's folder: %d, want 404", st)
	}
	if st := e.call(admin, "PATCH", "/v1/devices/"+d1.id, map[string]any{"tag_ids": []string{lab}, "folder_id": nil}, nil); st != 200 {
		t.Fatalf("replace tags: %d", st)
	}
	if got := e.orgDevice(admin, d1.id); got.FolderID != nil || !slices.Equal(got.TagIDs, []string{lab}) || !got.Favorite {
		t.Fatalf("after replace: %+v", got)
	}

	// Bulk: one foreign device fails the whole request.
	if st := e.call(admin, "POST", "/v1/devices/bulk", map[string]any{"device_ids": []string{d1.id, strangerDevice.id},
		"folder_id": office.ID}, nil); st != 404 {
		t.Fatalf("bulk with a foreign device: %d, want 404", st)
	}
	var bulk struct{ Items []orgDevice }
	if st := e.call(admin, "POST", "/v1/devices/bulk", map[string]any{"device_ids": []string{d1.id, d2.id},
		"folder_id": office.ID, "add_tag_ids": []string{win}, "remove_tag_ids": []string{lab}}, &bulk); st != 200 || len(bulk.Items) != 2 {
		t.Fatalf("bulk: %d %+v", st, bulk)
	}
	for _, id := range []string{d1.id, d2.id} {
		if got := e.orgDevice(admin, id); got.FolderID == nil || *got.FolderID != office.ID || !slices.Equal(got.TagIDs, []string{win}) {
			t.Fatalf("after bulk %s: %+v", id, got)
		}
	}

	// Deleting a folder moves its devices and subfolders to its parent; a
	// subfolder whose name is taken there gets a suffix.
	e.folder(admin, "Floor 2", nil)
	if st := e.call(admin, "PATCH", "/v1/devices/"+d2.id, map[string]any{"folder_id": floor.ID}, nil); st != 200 {
		t.Fatalf("move d2: %d", st)
	}
	if st := e.call(admin, "DELETE", "/v1/folders/"+office.ID, nil, nil); st != 200 {
		t.Fatalf("delete office: %d", st)
	}
	if got := e.orgDevice(admin, d1.id); got.FolderID != nil {
		t.Fatalf("d1 should be at the top level: %+v", got)
	}
	if got := e.orgDevice(admin, d2.id); got.FolderID == nil || *got.FolderID != floor.ID {
		t.Fatalf("d2 should stay in its folder: %+v", got)
	}
	var fl struct{ Items []folderJSON }
	e.call(admin, "GET", "/v1/folders", nil, &fl)
	var top []string
	for _, f := range fl.Items {
		if f.ParentID == nil {
			top = append(top, f.Name)
		}
	}
	slices.Sort(top)
	if !slices.Equal(top, []string{"Floor 2", "Floor 2 (2)"}) {
		t.Fatalf("top-level folders after delete: %v", top)
	}

	// Deleting a tag drops it from devices.
	if st := e.call(admin, "DELETE", "/v1/tags/"+win, nil, nil); st != 200 {
		t.Fatalf("delete tag: %d", st)
	}
	if got := e.orgDevice(admin, d1.id); len(got.TagIDs) != 0 {
		t.Fatalf("tags after delete: %v", got.TagIDs)
	}
}

func TestOrgChangesArePushed(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	browser := e.dialBrowser(c)
	browser.send(proto.TypePing, map[string]any{"ts": 1})
	browser.expect(proto.TypePong, nil)

	e.call(c, "PATCH", "/v1/devices/"+ag.id, map[string]any{"favorite": true}, nil)
	var m proto.OrgChanged
	browser.expect(proto.TypeOrgChanged, &m)
	if m.Kind != "devices" || !slices.Equal(m.IDs, []string{ag.id}) {
		t.Fatalf("org.changed: %+v", m)
	}
	id := e.tag(c, "Lab", 1)
	browser.expect(proto.TypeOrgChanged, &m)
	if m.Kind != "tags" || !slices.Equal(m.IDs, []string{id}) {
		t.Fatalf("org.changed: %+v", m)
	}
}

func TestDeviceStatusPushesBusyAndRecents(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	keyID := e.registerClientKey(c)
	agent := e.dialAgent(ag)
	agent.send(proto.TypeHello, proto.Hello{OS: "linux", Arch: "amd64", ActiveSessions: []string{}})
	agent.expect(proto.TypeHelloAck, nil)
	browser := e.dialBrowser(c)
	watcher := e.dialBrowser(c) // another tab of the same user

	if got := e.orgDevice(c, ag.id); got.LastConnectedAt != nil {
		t.Fatalf("never connected, last_connected_at = %v", *got.LastConnectedAt)
	}
	expectBusy := func(want bool) {
		t.Helper()
		var st proto.DeviceStatus
		watcher.expect(proto.TypeDeviceStatus, &st)
		if st.DeviceID != ag.id || !st.Online || st.Busy != want {
			t.Fatalf("device.status %+v, want online busy=%v", st, want)
		}
	}

	browser.send(proto.TypeSessionCreate, proto.SessionCreate{Req: "r1", DeviceID: ag.id, ClientKeyID: keyID})
	var created proto.SessionCreated
	browser.expect(proto.TypeSessionCreated, &created)
	expectBusy(true)

	agent.send(proto.TypeSessionState, proto.SessionState{SessionID: created.SessionID, Connected: true, Path: proto.PathHost})
	browser.expect(proto.TypeSessionState, nil)
	if got := e.orgDevice(c, ag.id); got.LastConnectedAt == nil || !got.Busy {
		t.Fatalf("after connecting: %+v", got)
	}

	browser.send(proto.TypeSessionEnd, proto.SessionEnd{SessionID: created.SessionID, Reason: proto.EndUserHangup})
	agent.expect(proto.TypeSessionEnd, nil)
	expectBusy(false)
}

func TestDeviceInfoStoredAndPushed(t *testing.T) {
	e := newEnv(t)
	c := e.login(adminEmail, adminPass)
	ag := e.enroll(c)
	agent := e.dialAgent(ag)
	browser := e.dialBrowser(c)
	agent.send(proto.TypeHello, proto.Hello{OS: "darwin", Arch: "arm64", ActiveSessions: []string{},
		Capabilities: proto.Capabilities{Features: []string{proto.FeatureDeviceInfo}}})
	var ack proto.HelloAck
	agent.expect(proto.TypeHelloAck, &ack)
	if !slices.Contains(ack.Features, proto.FeatureDeviceInfo) {
		t.Fatalf("hello_ack features %v", ack.Features)
	}

	user, locked := "eugene", true
	agent.send(proto.TypeDeviceInfo, proto.HostInfoReport{Info: proto.HostInfo{OSName: "macOS", OSVersion: "15.1",
		User: &user, Locked: &locked, UptimeS: 3600, MemTotal: 16 << 30, MemUsed: 9 << 30,
		Disks: []proto.DiskInfo{{Name: "Macintosh HD", Mount: "/", FS: "apfs", Total: 500 << 30, Free: 120 << 30}}}})
	var push proto.HostInfoPush
	browser.expect(proto.TypeDeviceInfo, &push)
	if push.DeviceID != ag.id || push.Info.OSVersion != "15.1" || *push.Info.User != "eugene" || push.At == "" {
		t.Fatalf("push %+v", push)
	}

	// A second report within the interval is dropped.
	user2 := "someone-else"
	agent.send(proto.TypeDeviceInfo, proto.HostInfoReport{Info: proto.HostInfo{OSName: "macOS", User: &user2}})
	agent.send(proto.TypePing, proto.Ping{TS: 1})
	agent.expect(proto.TypePong, nil)

	var got struct {
		Device struct {
			Info     *proto.HostInfo `json:"info"`
			InfoAt   *string         `json:"info_at"`
			Features []string        `json:"features"`
		} `json:"device"`
	}
	if st := e.call(c, "GET", "/v1/devices/"+ag.id, nil, &got); st != 200 {
		t.Fatalf("get device: %d", st)
	}
	d := got.Device
	if d.Info == nil || *d.Info.User != "eugene" || d.Info.Disks[0].Free != 120<<30 || d.InfoAt == nil ||
		!slices.Equal(d.Features, []string{proto.FeatureDeviceInfo}) {
		t.Fatalf("stored info %+v at %v features %v", d.Info, d.InfoAt, d.Features)
	}
}

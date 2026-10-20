package app

import (
	"strings"
	"testing"

	"github.com/ruolez/remotectl/proto"
)

const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func okUpload() proto.FileRecord {
	return proto.FileRecord{Xfer: 7, Direction: "upload", Name: "a (1).txt", Dir: "/home/device/Downloads",
		Size: 5, Bytes: 5, SHA256: helloSHA, Result: "ok", DurationMS: 12}
}

func TestValidFileRecord(t *testing.T) {
	with := func(f func(*proto.FileRecord)) proto.FileRecord {
		r := okUpload()
		f(&r)
		return r
	}
	failed := func(f func(*proto.FileRecord)) proto.FileRecord {
		return with(func(r *proto.FileRecord) {
			r.Result, r.Error, r.SHA256, r.Bytes = "error", "checksum", "", 3
			f(r)
		})
	}
	cases := []struct {
		name string
		r    proto.FileRecord
		want bool
	}{
		{"ok upload", okUpload(), true},
		{"ok download", with(func(r *proto.FileRecord) { r.Direction = "download" }), true},
		{"failed", failed(func(*proto.FileRecord) {}), true},
		{"cancelled", failed(func(r *proto.FileRecord) { r.Result, r.Error = "cancelled", "cancelled" }), true},
		{"failed before a name was known", failed(func(r *proto.FileRecord) { r.Name, r.Dir, r.Size, r.Bytes = "", "", 0, 0 }), true},
		{"empty file", with(func(r *proto.FileRecord) {
			r.Size, r.Bytes, r.SHA256 = 0, 0, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		}), true},
		{"unknown direction", with(func(r *proto.FileRecord) { r.Direction = "sideways" }), false},
		{"unknown result", with(func(r *proto.FileRecord) { r.Result = "maybe" }), false},
		{"ok without name", with(func(r *proto.FileRecord) { r.Name = "" }), false},
		{"name too long", with(func(r *proto.FileRecord) { r.Name = strings.Repeat("n", 1025) }), false},
		{"name not utf-8", with(func(r *proto.FileRecord) { r.Name = "a\xff" }), false},
		{"dir too long", with(func(r *proto.FileRecord) { r.Dir = strings.Repeat("d", 4097) }), false},
		{"negative size", with(func(r *proto.FileRecord) { r.Size = -1 }), false},
		{"bytes over size", failed(func(r *proto.FileRecord) { r.Bytes = 6 }), false},
		{"negative duration", with(func(r *proto.FileRecord) { r.DurationMS = -1 }), false},
		{"ok but short", with(func(r *proto.FileRecord) { r.Bytes = 4 }), false},
		{"ok without sha", with(func(r *proto.FileRecord) { r.SHA256 = "" }), false},
		{"ok with error", with(func(r *proto.FileRecord) { r.Error = "io" }), false},
		{"sha upper case", with(func(r *proto.FileRecord) { r.SHA256 = strings.ToUpper(helloSHA) }), false},
		{"sha short", with(func(r *proto.FileRecord) { r.SHA256 = helloSHA[:10] }), false},
		{"failed without error", failed(func(r *proto.FileRecord) { r.Error = "" }), false},
		{"failed with sha", failed(func(r *proto.FileRecord) { r.SHA256 = helloSHA }), false},
		{"error not a code", failed(func(r *proto.FileRecord) { r.Error = "disk full!" }), false},
		{"error too long", failed(func(r *proto.FileRecord) { r.Error = strings.Repeat("x", 33) }), false},
	}
	for _, c := range cases {
		if got := validFileRecord(c.r); got != c.want {
			t.Errorf("%s: validFileRecord = %v, want %v (%+v)", c.name, got, c.want, c.r)
		}
	}
}

func TestFileEvent(t *testing.T) {
	cases := []struct{ dir, result, want string }{
		{"upload", "ok", "file.uploaded"},
		{"upload", "error", "file.upload_failed"},
		{"upload", "cancelled", "file.upload_failed"},
		{"download", "ok", "file.downloaded"},
		{"download", "cancelled", "file.download_failed"},
		{"download", "error", "file.download_failed"},
	}
	for _, c := range cases {
		if got := fileEvent(proto.FileRecord{Direction: c.dir, Result: c.result}); got != c.want {
			t.Errorf("fileEvent(%s, %s) = %s, want %s", c.dir, c.result, got, c.want)
		}
	}
}

package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (h *harness) opRecords() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		out = append(out, strings.Join([]string{r.Direction, r.Name, r.To, r.Kind, r.Result, r.Error}, ":"))
	}
	return out
}

// waitRecords waits until n records exist.
func (h *harness) waitRecords(n int) []string {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := h.opRecords(); len(r) >= n {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("records = %v, want %d", h.opRecords(), n)
	return nil
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestMkdir(t *testing.T) {
	h := newHarness(t, nil)
	_ = os.WriteFile(filepath.Join(h.home, "file"), nil, 0o644)

	h.send(`{"t":"mkdir","id":1,"dir":%q,"name":"Reports"}`, h.home)
	if m := h.ch.wait(t, "ok", 1); m["path"] != filepath.Join(h.home, "Reports") || m["existed"] != nil {
		t.Fatalf("ok %v", m)
	}
	if !isDir(filepath.Join(h.home, "Reports")) {
		t.Fatal("folder not created")
	}
	h.send(`{"t":"mkdir","id":2,"dir":%q,"name":"a:b"}`, h.home)
	if m := h.ch.wait(t, "ok", 2); m["path"] != filepath.Join(h.home, "a_b") {
		t.Fatalf("sanitized %v", m)
	}
	h.send(`{"t":"mkdir","id":3,"dir":%q,"name":"Reports","exist_ok":true}`, h.home)
	if m := h.ch.wait(t, "ok", 3); m["existed"] != true {
		t.Fatalf("exist_ok %v", m)
	}

	errs := []struct {
		id         uint32
		dir, name  string
		existOK    bool
		code       string
		wantRecord bool
	}{
		{10, h.home, "Reports", false, CodeExists, true},
		{11, h.home, "file", true, CodeExists, true},
		{12, filepath.Join(h.home, "missing"), "x", false, CodeNotFound, true},
		{13, filepath.Join(h.home, "state"), "x", false, CodeDenied, true},
		{14, h.home, "state", true, CodeDenied, true},
		{15, h.home, "..", false, CodeBadName, true},
		{16, "relative", "x", false, CodeBadPath, true},
	}
	for _, e := range errs {
		h.send(`{"t":"mkdir","id":%d,"dir":%q,"name":%q,"exist_ok":%v}`, e.id, e.dir, e.name, e.existOK)
		if m := h.ch.wait(t, "error", e.id); m["code"] != e.code {
			t.Fatalf("mkdir %s/%s: %v, want %s", e.dir, e.name, m, e.code)
		}
	}
	got := strings.Join(h.waitRecords(9), " ")
	want := "mkdir:Reports::dir:ok: mkdir:a_b::dir:ok: " +
		"mkdir:Reports::dir:error:exists mkdir:file::dir:error:exists mkdir:x::dir:error:not_found " +
		"mkdir:x::dir:error:denied mkdir:state::dir:error:denied mkdir:..::dir:error:bad_name mkdir:x::dir:error:bad_path"
	if got != want {
		t.Fatalf("records\n got %s\nwant %s", got, want)
	}
}

func TestRename(t *testing.T) {
	h := newHarness(t, nil)
	docs := filepath.Join(h.home, "Docs")
	_ = os.MkdirAll(filepath.Join(docs, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(docs, "a.txt"), []byte("a"), 0o644)
	_ = os.WriteFile(filepath.Join(docs, "b.txt"), []byte("b"), 0o644)

	h.send(`{"t":"rename","id":1,"path":%q,"name":"c.txt"}`, filepath.Join(docs, "a.txt"))
	if m := h.ch.wait(t, "ok", 1); m["path"] != filepath.Join(docs, "c.txt") {
		t.Fatalf("ok %v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "c.txt")); string(b) != "a" || exists(filepath.Join(docs, "a.txt")) {
		t.Fatal("file not renamed")
	}
	h.send(`{"t":"rename","id":2,"path":%q,"name":"Sub2"}`, filepath.Join(docs, "sub"))
	h.ch.wait(t, "ok", 2)
	if !isDir(filepath.Join(docs, "Sub2")) {
		t.Fatal("folder not renamed")
	}
	h.send(`{"t":"rename","id":3,"path":%q,"name":"B.TXT"}`, filepath.Join(docs, "b.txt"))
	h.ch.wait(t, "ok", 3)
	if names, _ := os.ReadDir(docs); !hasName(names, "B.TXT") || hasName(names, "b.txt") {
		t.Fatal("case-only rename did not apply")
	}

	errs := []struct {
		id         uint32
		path, name string
		code       string
	}{
		{10, filepath.Join(docs, "c.txt"), "B.TXT", CodeExists},
		{11, filepath.Join(docs, "missing"), "x", CodeNotFound},
		{12, h.home, "x", CodeProtected},
		{13, filepath.Join(h.home, "Downloads"), "x", CodeProtected},
		{14, filepath.Join(h.home, "state"), "x", CodeDenied},
		{15, filepath.Join(docs, "c.txt"), "state", CodeExists},
		{16, filepath.Join(docs, "c.txt"), "", CodeBadName},
		{17, "/", "x", CodeProtected},
	}
	_ = os.Mkdir(filepath.Join(docs, "state"), 0o755)
	for _, e := range errs {
		h.send(`{"t":"rename","id":%d,"path":%q,"name":%q}`, e.id, e.path, e.name)
		if m := h.ch.wait(t, "error", e.id); m["code"] != e.code {
			t.Fatalf("rename %s → %s: %v, want %s", e.path, e.name, m, e.code)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(docs, "c.txt")); string(b) != "a" {
		t.Fatal("a refused rename changed the file")
	}
	h.send(`{"t":"rename","id":20,"path":%q,"name":"state"}`, filepath.Join(h.home, "Docs", "Sub2"))
	if m := h.ch.wait(t, "error", 20); m["code"] != CodeExists {
		t.Fatalf("rename onto a folder: %v", m)
	}
	recs := h.waitRecords(11)
	if recs[0] != "rename:a.txt:c.txt:file:ok:" || recs[1] != "rename:sub:Sub2:dir:ok:" || recs[2] != "rename:b.txt:B.TXT:file:ok:" {
		t.Fatalf("records %v", recs)
	}
}

func hasName(des []os.DirEntry, name string) bool {
	for _, de := range des {
		if de.Name() == name {
			return true
		}
	}
	return false
}

func TestDelete(t *testing.T) {
	h := newHarness(t, nil)
	docs := filepath.Join(h.home, "Docs")
	tree := filepath.Join(docs, "tree")
	_ = os.MkdirAll(filepath.Join(tree, "a", "b"), 0o755)
	_ = os.WriteFile(filepath.Join(tree, "a", "b", "f.txt"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(docs, "one.txt"), []byte("x"), 0o644)
	keep := filepath.Join(h.home, "keep")
	_ = os.MkdirAll(keep, 0o755)
	_ = os.WriteFile(filepath.Join(keep, "k.txt"), []byte("k"), 0o644)
	_ = os.Symlink(keep, filepath.Join(docs, "link"))

	for id, p := range map[uint32]string{1: filepath.Join(docs, "one.txt"), 2: tree, 3: filepath.Join(docs, "link")} {
		h.send(`{"t":"delete","id":%d,"path":%q}`, id, p)
		if m := h.ch.wait(t, "ok", id); m["path"] != p {
			t.Fatalf("ok %v", m)
		}
		if exists(p) {
			t.Fatalf("%s still exists", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(keep, "k.txt")); string(b) != "k" {
		t.Fatal("deleting a link removed its target")
	}

	errs := []struct {
		id   uint32
		path string
		code string
	}{
		{10, filepath.Join(docs, "missing"), CodeNotFound},
		{11, h.home, CodeProtected},
		{12, filepath.Join(h.home, "Downloads"), CodeProtected},
		{13, filepath.Dir(h.home), CodeProtected},
		{14, "/", CodeProtected},
		{15, filepath.Join(h.home, "state"), CodeDenied},
		{16, filepath.Join(h.home, "state", "device.key"), CodeDenied},
		{17, "relative", CodeBadPath},
	}
	for _, e := range errs {
		h.send(`{"t":"delete","id":%d,"path":%q}`, e.id, e.path)
		if m := h.ch.wait(t, "error", e.id); m["code"] != e.code {
			t.Fatalf("delete %s: %v, want %s", e.path, m, e.code)
		}
	}
	if !exists(filepath.Join(h.home, "state", "device.key")) || !isDir(filepath.Join(h.home, "Downloads")) {
		t.Fatal("a refused delete removed something")
	}
	recs := h.waitRecords(3)
	got := map[string]bool{}
	for _, r := range recs[:3] {
		got[r] = true
	}
	if !got["delete:one.txt::file:ok:"] || !got["delete:tree::dir:ok:"] || !got["delete:link::other:ok:"] {
		t.Fatalf("records %v", recs)
	}
}

func TestDeleteRefusesAncestorOfDenied(t *testing.T) {
	var deny string
	h := newHarness(t, func(c *Config) {
		home := c.Host.(testHost).home
		deny = filepath.Join(home, "Library", "Application Support", "RemoteAgent")
		_ = os.MkdirAll(deny, 0o700)
		c.Policy.Deny = append(c.Policy.Deny, deny)
	})
	for id, p := range map[uint32]string{1: filepath.Join(h.home, "Library"), 2: filepath.Dir(deny)} {
		h.send(`{"t":"delete","id":%d,"path":%q}`, id, p)
		if m := h.ch.wait(t, "error", id); m["code"] != CodeDenied {
			t.Fatalf("delete %s: %v", p, m)
		}
		h.send(`{"t":"rename","id":%d,"path":%q,"name":"x"}`, id+10, p)
		if m := h.ch.wait(t, "error", id+10); m["code"] != CodeDenied {
			t.Fatalf("rename %s: %v", p, m)
		}
	}
	if !isDir(deny) {
		t.Fatal("state directory removed")
	}
}

func TestOpsRefusedWithoutAudit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AuditOK = func() bool { return false } })
	_ = os.WriteFile(filepath.Join(h.home, "a"), nil, 0o644)
	h.send(`{"t":"mkdir","id":1,"dir":%q,"name":"x"}`, h.home)
	h.send(`{"t":"delete","id":2,"path":%q}`, filepath.Join(h.home, "a"))
	for _, id := range []uint32{1, 2} {
		if m := h.ch.wait(t, "error", id); m["code"] != CodeAuditUnavailable {
			t.Fatalf("error %v", m)
		}
	}
	if exists(filepath.Join(h.home, "x")) || !exists(filepath.Join(h.home, "a")) {
		t.Fatal("a refused operation ran")
	}
}

func TestUploadReplace(t *testing.T) {
	h := newHarness(t, nil)
	target := filepath.Join(h.home, "a.txt")
	_ = os.WriteFile(target, []byte("old"), 0o644)
	_ = os.Mkdir(filepath.Join(h.home, "d.txt"), 0o755)
	content := []byte("new content")

	h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":%d,"replace":true}`, h.home, len(content))
	if m := h.ch.wait(t, "ready", 1); m["name"] != "a.txt" {
		t.Fatalf("ready %v", m)
	}
	h.data(1, content)
	h.send(`{"t":"end","xfer":1,"sha256":%q}`, sum(content))
	if m := h.ch.wait(t, "done", 1); m["path"] != target {
		t.Fatalf("done %v", m)
	}
	if b, _ := os.ReadFile(target); string(b) != string(content) {
		t.Fatalf("target = %q", b)
	}
	if r := h.record(1); !r.Replaced || r.Result != StateOK {
		t.Fatalf("record %+v", r)
	}

	// A folder of that name is never replaced; the upload keeps both.
	h.send(`{"t":"put","xfer":2,"dir":%q,"name":"d.txt","size":1,"replace":true}`, h.home)
	if m := h.ch.wait(t, "ready", 2); m["name"] != "d (1).txt" {
		t.Fatalf("ready %v", m)
	}
	// A failed replacing upload leaves the old file alone.
	h.send(`{"t":"put","xfer":3,"dir":%q,"name":"a.txt","size":3,"replace":true}`, h.home)
	h.ch.wait(t, "ready", 3)
	h.data(3, []byte("bad"))
	h.send(`{"t":"end","xfer":3,"sha256":%q}`, sum([]byte("other")))
	if m := h.ch.wait(t, "error", 3); m["code"] != CodeChecksum {
		t.Fatalf("error %v", m)
	}
	if b, _ := os.ReadFile(target); string(b) != string(content) {
		t.Fatalf("target after failed replace = %q", b)
	}
	if r := h.record(3); r.Replaced {
		t.Fatalf("failed upload recorded as replacing: %+v", r)
	}
}

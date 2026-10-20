package files

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/proto"
)

var _ Channel = (*webrtc.DataChannel)(nil)

// fakeChan records what the service sends. With autoAck it acks every
// download frame back into the service like a browser that keeps up.
type fakeChan struct {
	mu       sync.Mutex
	texts    []map[string]any
	data     map[uint32]*bytes.Buffer
	buffered uint64
	low      func()
	svc      *Service
	autoAck  bool
	sent     chan struct{}
}

func newFakeChan() *fakeChan {
	return &fakeChan{data: map[uint32]*bytes.Buffer{}, sent: make(chan struct{}, 1)}
}

func (f *fakeChan) poke() {
	select {
	case f.sent <- struct{}{}:
	default:
	}
}

func (f *fakeChan) Send(b []byte) error {
	xfer, p, err := DecodeData(b)
	if err != nil {
		return err
	}
	f.mu.Lock()
	buf := f.data[xfer]
	if buf == nil {
		buf = &bytes.Buffer{}
		f.data[xfer] = buf
	}
	buf.Write(p)
	n, ack := buf.Len(), f.autoAck
	f.mu.Unlock()
	if ack {
		f.svc.Handle(true, []byte(fmt.Sprintf(`{"t":"ack","xfer":%d,"bytes":%d}`, xfer, n)))
	}
	f.poke()
	return nil
}

func (f *fakeChan) SendText(s string) error {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return err
	}
	f.mu.Lock()
	f.texts = append(f.texts, m)
	f.mu.Unlock()
	f.poke()
	return nil
}

func (f *fakeChan) BufferedAmount() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buffered
}

func (f *fakeChan) SetBufferedAmountLowThreshold(uint64) {}

func (f *fakeChan) OnBufferedAmountLow(fn func()) { f.low = fn }

func (f *fakeChan) received(xfer uint32) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b := f.data[xfer]; b != nil {
		return append([]byte(nil), b.Bytes()...)
	}
	return nil
}

func (f *fakeChan) count(match func(map[string]any) bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.texts {
		if match(m) {
			n++
		}
	}
	return n
}

// wait returns the first sent JSON frame matching t and xfer/id.
func (f *fakeChan) wait(tb testing.TB, typ string, ref uint32) map[string]any {
	tb.Helper()
	deadline := time.After(5 * time.Second)
	for {
		f.mu.Lock()
		for _, m := range f.texts {
			if m["t"] == typ && (m["xfer"] == float64(ref) || m["id"] == float64(ref)) {
				f.mu.Unlock()
				return m
			}
		}
		f.mu.Unlock()
		select {
		case <-f.sent:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			f.mu.Lock()
			defer f.mu.Unlock()
			tb.Fatalf("no %s for %d; sent %v", typ, ref, f.texts)
			return nil
		}
	}
}

// testHost is LocalHost rooted at a temp home.
type testHost struct {
	home string
	err  error // returned by Roots and Do
}

func (h testHost) Do(fn func() error) error {
	if h.err != nil {
		return h.err
	}
	return fn()
}

func (h testHost) Roots() ([]Root, string, error) {
	if h.err != nil {
		return nil, "", h.err
	}
	roots, def := userRoots(h.home, isDir)
	return roots, def, nil
}

type harness struct {
	t    *testing.T
	home string
	ch   *fakeChan
	svc  *Service

	mu       sync.Mutex
	records  []proto.FileRecord
	activity []Activity
}

func newHarness(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, "state") // stands in for the agent's DataDir
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(state, "device.key"), []byte("secret"), 0o600)
	h := &harness{t: t, home: home, ch: newFakeChan()}
	c := Config{Host: testHost{home: home}, Policy: Policy{Deny: []string{state}}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Record:   func(r proto.FileRecord) { h.mu.Lock(); h.records = append(h.records, r); h.mu.Unlock() },
		Activity: func(a Activity) { h.mu.Lock(); h.activity = append(h.activity, a); h.mu.Unlock() }}
	if tweak != nil {
		tweak(&c)
	}
	h.svc = New(c)
	h.ch.svc = h.svc
	h.svc.Attach(h.ch)
	t.Cleanup(func() { h.svc.Close("test over") })
	return h
}

func (h *harness) send(format string, args ...any) {
	h.svc.Handle(true, []byte(fmt.Sprintf(format, args...)))
}

func (h *harness) data(xfer uint32, p []byte) {
	for len(p) > 0 {
		n := min(len(p), MaxChunk)
		h.svc.Handle(false, EncodeData(xfer, p[:n]))
		p = p[n:]
	}
}

func (h *harness) record(xfer uint32) proto.FileRecord {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, r := range h.records {
			if r.Xfer == xfer {
				h.mu.Unlock()
				return r
			}
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("no record for xfer %d", xfer)
	return proto.FileRecord{}
}

func (h *harness) activities(xfer uint32) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, a := range h.activity {
		if a.Xfer == xfer {
			out = append(out, a.State)
		}
	}
	return out
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func partFiles(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, de := range des {
		if IsPartName(de.Name()) {
			out = append(out, de.Name())
		}
	}
	return out
}

// upload sends a complete upload of content, ending with sha.
func (h *harness) upload(xfer uint32, dir, name string, content []byte, sha string) {
	h.t.Helper()
	h.send(`{"t":"put","xfer":%d,"dir":%q,"name":%q,"size":%d}`, xfer, dir, name, len(content))
	h.ch.wait(h.t, "ready", xfer)
	h.data(xfer, content)
	h.send(`{"t":"end","xfer":%d,"sha256":%q}`, xfer, sha)
}

// ---------- uploads ----------

func TestUploadCommitsVerifiedFile(t *testing.T) {
	h := newHarness(t, nil)
	content := randomBytes(t, 1<<20+12345)
	downloads := filepath.Join(h.home, "Downloads")
	h.upload(7, "", "report.pdf", content, sum(content))
	done := h.ch.wait(t, "done", 7)
	want := map[string]any{"t": "done", "xfer": float64(7), "name": "report.pdf", "path": filepath.Join(downloads, "report.pdf"),
		"size": float64(len(content)), "sha256": sum(content)}
	if fmt.Sprint(done) != fmt.Sprint(want) {
		t.Fatalf("done = %v\nwant %v", done, want)
	}
	got, err := os.ReadFile(filepath.Join(downloads, "report.pdf"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("file on disk: %d bytes, %v", len(got), err)
	}
	if p := partFiles(t, downloads); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
	r := h.record(7)
	if r.Direction != DirUpload || r.Result != StateOK || r.Name != "report.pdf" || r.Dir != downloads ||
		r.Size != int64(len(content)) || r.Bytes != int64(len(content)) || r.SHA256 != sum(content) || r.Error != "" {
		t.Fatalf("record %+v", r)
	}
	acks := h.ch.count(func(m map[string]any) bool { return m["t"] == "ack" && m["xfer"] == float64(7) })
	if min := len(content) / AckEvery; acks < min {
		t.Fatalf("%d acks for %d bytes, want ≥ %d", acks, len(content), min)
	}
	if a := h.activities(7); len(a) < 2 || a[0] != StateActive || a[len(a)-1] != StateOK {
		t.Fatalf("activity = %v", a)
	}
}

func TestUploadPicksFreeName(t *testing.T) {
	h := newHarness(t, nil)
	if err := os.WriteFile(filepath.Join(h.home, "a.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two uploads of the same name in flight at once get distinct names.
	h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":3}`, h.home)
	h.send(`{"t":"put","xfer":2,"dir":%q,"name":"a.txt","size":3}`, h.home)
	names := map[string]bool{}
	for _, x := range []uint32{1, 2} {
		names[h.ch.wait(t, "ready", x)["name"].(string)] = true
	}
	if !names["a (1).txt"] || !names["a (2).txt"] {
		t.Fatalf("ready names = %v", names)
	}
	for _, x := range []uint32{1, 2} {
		h.data(x, []byte("new"))
		h.send(`{"t":"end","xfer":%d,"sha256":%q}`, x, sum([]byte("new")))
		h.ch.wait(t, "done", x)
	}
	if b, _ := os.ReadFile(filepath.Join(h.home, "a.txt")); string(b) != "old" {
		t.Fatalf("existing file replaced: %q", b)
	}
}

func TestUploadRenamesAroundLateCollision(t *testing.T) {
	h := newHarness(t, nil)
	h.send(`{"t":"put","xfer":3,"dir":%q,"name":"b.txt","size":2}`, h.home)
	if n := h.ch.wait(t, "ready", 3)["name"]; n != "b.txt" {
		t.Fatalf("ready name %v", n)
	}
	// Created by someone else between ready and commit.
	if err := os.WriteFile(filepath.Join(h.home, "b.txt"), []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.data(3, []byte("hi"))
	h.send(`{"t":"end","xfer":3,"sha256":%q}`, sum([]byte("hi")))
	if done := h.ch.wait(t, "done", 3); done["name"] != "b (1).txt" {
		t.Fatalf("done %v", done)
	}
	if b, _ := os.ReadFile(filepath.Join(h.home, "b.txt")); string(b) != "theirs" {
		t.Fatalf("existing file replaced: %q", b)
	}
}

func TestUploadReplacesLeftoverPart(t *testing.T) {
	h := newHarness(t, nil)
	leftover := filepath.Join(h.home, PartName("c.txt", 5))
	if err := os.WriteFile(leftover, []byte("stale bytes from a crashed agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.upload(5, h.home, "c.txt", []byte("fresh"), sum([]byte("fresh")))
	h.ch.wait(t, "done", 5)
	if b, _ := os.ReadFile(filepath.Join(h.home, "c.txt")); string(b) != "fresh" {
		t.Fatalf("content %q", b)
	}
	if p := partFiles(t, h.home); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
}

func TestUploadEmptyFile(t *testing.T) {
	h := newHarness(t, nil)
	h.upload(4, h.home, "empty", nil, sum(nil))
	h.ch.wait(t, "done", 4)
	if st, err := os.Stat(filepath.Join(h.home, "empty")); err != nil || st.Size() != 0 {
		t.Fatalf("empty file: %v %v", st, err)
	}
}

func TestUploadFailures(t *testing.T) {
	type tc struct {
		name   string
		tweak  func(*Config)
		run    func(h *harness)
		code   string
		result string
	}
	content := []byte("hello")
	cases := []tc{
		{name: "checksum mismatch", code: CodeChecksum, result: StateError, run: func(h *harness) {
			h.upload(1, h.home, "a.txt", content, sum([]byte("other")))
		}},
		{name: "short end", code: CodeProtocol, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":10}`, h.home)
			h.ch.wait(h.t, "ready", 1)
			h.data(1, content)
			h.send(`{"t":"end","xfer":1,"sha256":%q}`, sum(content))
		}},
		{name: "more than size", code: CodeProtocol, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":2}`, h.home)
			h.ch.wait(h.t, "ready", 1)
			h.data(1, content)
		}},
		{name: "window exceeded", code: CodeProtocol, result: StateError,
			tweak: func(c *Config) { c.window = 1024 },
			run: func(h *harness) {
				h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":4096}`, h.home)
				if w := h.ch.wait(h.t, "ready", 1)["window"]; w != float64(1024) {
					h.t.Fatalf("ready window %v", w)
				}
				h.data(1, make([]byte, 1025))
			}},
		{name: "data after end", code: CodeProtocol, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":5}`, h.home)
			h.ch.wait(h.t, "ready", 1)
			h.send(`{"t":"end","xfer":1,"sha256":%q}`, sum(content))
			h.data(1, content)
		}},
		{name: "bad end frame", code: CodeProtocol, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":5}`, h.home)
			h.ch.wait(h.t, "ready", 1)
			h.send(`{"t":"end","xfer":1,"sha256":"nope"}`)
		}},
		{name: "ack on an upload", code: CodeProtocol, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":5}`, h.home)
			h.ch.wait(h.t, "ready", 1)
			h.send(`{"t":"ack","xfer":1,"bytes":0}`)
		}},
		{name: "bad name", code: CodeBadName, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"..","size":5}`, h.home)
		}},
		{name: "relative dir", code: CodeBadPath, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":"Downloads","name":"a.txt","size":5}`)
		}},
		{name: "missing dir", code: CodeNotFound, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":5}`, filepath.Join(h.home, "nope"))
		}},
		{name: "dir is a file", code: CodeNotDir, result: StateError, run: func(h *harness) {
			p := filepath.Join(h.home, "f")
			_ = os.WriteFile(p, nil, 0o644)
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":5}`, p)
		}},
		{name: "denied dir", code: CodeDenied, result: StateError, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":5}`, filepath.Join(h.home, "state"))
		}},
		{name: "browser cancel", code: CodeCancelled, result: StateCancelled, run: func(h *harness) {
			h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":10}`, h.home)
			h.ch.wait(h.t, "ready", 1)
			h.data(1, content)
			h.send(`{"t":"cancel","xfer":1}`)
		}},
		{name: "stall", code: CodeStalled, result: StateError, tweak: func(c *Config) { c.stallTimeout = 150 * time.Millisecond },
			run: func(h *harness) {
				h.send(`{"t":"put","xfer":1,"dir":%q,"name":"a.txt","size":10}`, h.home)
				h.ch.wait(h.t, "ready", 1)
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(cfg *Config) {
				if c.tweak != nil {
					c.tweak(cfg)
				}
			})
			c.run(h)
			r := h.record(1)
			if r.Result != c.result || r.Error != c.code || r.SHA256 != "" {
				t.Fatalf("record %+v, want %s/%s", r, c.result, c.code)
			}
			errs := h.ch.count(func(m map[string]any) bool { return m["t"] == "error" && m["xfer"] == float64(1) })
			if wantErr := c.code != CodeCancelled; (errs == 1) != wantErr {
				t.Fatalf("%d error frames sent", errs)
			}
			if wantErr := c.code != CodeCancelled; wantErr && h.ch.wait(t, "error", 1)["code"] != c.code {
				t.Fatalf("error frame %v", h.ch.wait(t, "error", 1))
			}
			if p := partFiles(t, h.home); len(p) != 0 {
				t.Fatalf("part files left: %v", p)
			}
			if _, err := os.Stat(filepath.Join(h.home, "a.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final file exists after failure: %v", err)
			}
		})
	}
}

func TestCloseCancelsAndRemovesParts(t *testing.T) {
	h := newHarness(t, nil)
	src := filepath.Join(h.home, "big.bin")
	if err := os.WriteFile(src, randomBytes(t, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"t":"put","xfer":1,"dir":%q,"name":"up.bin","size":100}`, h.home)
	h.ch.wait(t, "ready", 1)
	h.data(1, []byte("partial"))
	h.send(`{"t":"get","xfer":2,"path":%q}`, src) // no acks: stalls on credit
	h.ch.wait(t, "begin", 2)
	h.svc.Close("session ended")
	for _, x := range []uint32{1, 2} {
		if r := h.record(x); r.Result != StateCancelled || r.Error != CodeCancelled {
			t.Fatalf("xfer %d record %+v", x, r)
		}
	}
	if p := partFiles(t, h.home); len(p) != 0 {
		t.Fatalf("part files left: %v", p)
	}
	if n := h.ch.count(func(m map[string]any) bool { return m["t"] == "error" }); n != 0 {
		t.Fatalf("%d error frames sent on close", n)
	}
	all := func(map[string]any) bool { return true }
	before := h.ch.count(all)
	h.send(`{"t":"roots","id":1}`)
	time.Sleep(20 * time.Millisecond)
	if h.ch.count(all) != before {
		t.Fatal("closed service answered a request")
	}
}

func TestStalePartsSwept(t *testing.T) {
	h := newHarness(t, nil)
	old := filepath.Join(h.home, PartName("old.bin", 9))
	fresh := filepath.Join(h.home, PartName("fresh.bin", 10))
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ancient := time.Now().Add(-StalePartAge - time.Hour)
	if err := os.Chtimes(old, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	h.upload(1, h.home, "a.txt", []byte("a"), sum([]byte("a")))
	h.ch.wait(t, "done", 1)
	h.svc.Close("done")
	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale part kept: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh part removed: %v", err)
	}
}

// ---------- downloads ----------

func TestDownloadStreamsFile(t *testing.T) {
	h := newHarness(t, nil)
	h.ch.autoAck = true
	content := randomBytes(t, 3*Window/2+777)
	p := filepath.Join(h.home, "Downloads", "data.bin")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	h.send(`{"t":"get","xfer":8,"path":%q}`, p)
	begin := h.ch.wait(t, "begin", 8)
	if begin["name"] != "data.bin" || begin["size"] != float64(len(content)) || begin["window"] != float64(Window) {
		t.Fatalf("begin %v", begin)
	}
	end := h.ch.wait(t, "end", 8)
	if end["sha256"] != sum(content) || end["size"] != float64(len(content)) {
		t.Fatalf("end %v", end)
	}
	if got := h.ch.received(8); !bytes.Equal(got, content) {
		t.Fatalf("received %d bytes, want %d", len(got), len(content))
	}
	r := h.record(8)
	if r.Direction != DirDownload || r.Result != StateOK || r.Bytes != int64(len(content)) || r.SHA256 != sum(content) ||
		r.Name != "data.bin" || r.Dir != filepath.Join(h.home, "Downloads") {
		t.Fatalf("record %+v", r)
	}
}

func TestDownloadRespectsCreditWindow(t *testing.T) {
	const window = 64 << 10
	h := newHarness(t, func(c *Config) { c.window = window })
	p := filepath.Join(h.home, "f.bin")
	content := randomBytes(t, 4*window)
	_ = os.WriteFile(p, content, 0o644)
	h.send(`{"t":"get","xfer":1,"path":%q}`, p)
	h.ch.wait(t, "begin", 1)
	waitLen := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(h.ch.received(1)) < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(30 * time.Millisecond) // nothing more may follow
		if got := len(h.ch.received(1)); got != n {
			t.Fatalf("sent %d bytes, want exactly %d", got, n)
		}
	}
	waitLen(window)
	h.send(`{"t":"ack","xfer":1,"bytes":%d}`, window/2)
	waitLen(window + window/2)
	h.send(`{"t":"ack","xfer":1,"bytes":%d}`, window+window/2+1) // past what was sent
	if r := h.record(1); r.Error != CodeProtocol {
		t.Fatalf("record %+v", r)
	}
}

func TestDownloadWaitsForBufferedAmount(t *testing.T) {
	h := newHarness(t, nil)
	h.ch.autoAck = true
	h.ch.buffered = bufferHigh + 1
	p := filepath.Join(h.home, "f.bin")
	_ = os.WriteFile(p, []byte("hello"), 0o644)
	h.send(`{"t":"get","xfer":1,"path":%q}`, p)
	h.ch.wait(t, "begin", 1)
	time.Sleep(50 * time.Millisecond)
	if n := len(h.ch.received(1)); n != 0 {
		t.Fatalf("sent %d bytes while the channel was over the high-water mark", n)
	}
	h.ch.mu.Lock()
	h.ch.buffered = 0
	h.ch.mu.Unlock()
	h.ch.low()
	h.ch.wait(t, "end", 1)
	if r := h.record(1); r.Result != StateOK {
		t.Fatalf("record %+v", r)
	}
}

func TestDownloadDetectsChangedFile(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p string)
	}{
		{"shrinks", func(p string) { _ = os.Truncate(p, 20<<10) }},
		{"grows", func(p string) {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			_, _ = f.Write(make([]byte, 50<<10))
			f.Close()
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(cfg *Config) { cfg.window = MaxChunk })
			p := filepath.Join(h.home, "f.bin")
			_ = os.WriteFile(p, make([]byte, 100<<10), 0o644)
			h.send(`{"t":"get","xfer":1,"path":%q}`, p)
			h.ch.wait(t, "begin", 1)
			for len(h.ch.received(1)) < MaxChunk {
				time.Sleep(time.Millisecond)
			}
			c.mutate(p)
			h.ch.mu.Lock()
			h.ch.autoAck = true
			h.ch.mu.Unlock()
			h.send(`{"t":"ack","xfer":1,"bytes":%d}`, MaxChunk)
			if r := h.record(1); r.Error != CodeChanged || r.Result != StateError {
				t.Fatalf("record %+v", r)
			}
			if e := h.ch.wait(t, "error", 1); e["code"] != CodeChanged {
				t.Fatalf("error %v", e)
			}
		})
	}
}

func TestDownloadFailures(t *testing.T) {
	cases := []struct {
		name string
		path func(h *harness) string
		code string
	}{
		{"directory", func(h *harness) string { return h.home }, CodeNotFile},
		{"missing", func(h *harness) string { return filepath.Join(h.home, "nope") }, CodeNotFound},
		{"relative", func(*harness) string { return "f.bin" }, CodeBadPath},
		{"denied", func(h *harness) string { return filepath.Join(h.home, "state", "device.key") }, CodeDenied},
		{"denied via symlink", func(h *harness) string {
			l := filepath.Join(h.home, "innocent")
			_ = os.Symlink(filepath.Join(h.home, "state"), l)
			return filepath.Join(l, "device.key")
		}, CodeDenied},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.send(`{"t":"get","xfer":1,"path":%q}`, c.path(h))
			if e := h.ch.wait(t, "error", 1); e["code"] != c.code {
				t.Fatalf("error %v, want %s", e, c.code)
			}
			if r := h.record(1); r.Error != c.code || r.Direction != DirDownload {
				t.Fatalf("record %+v", r)
			}
			if h.ch.received(1) != nil {
				t.Fatal("data sent")
			}
		})
	}
}

func TestRelayPacing(t *testing.T) {
	const rate = 256 << 10
	h := newHarness(t, func(c *Config) {
		c.relayRate = rate
		c.Path = func() string { return proto.PathRelay }
	})
	h.ch.autoAck = true
	p := filepath.Join(h.home, "f.bin")
	burst := 4 * MaxChunk
	_ = os.WriteFile(p, make([]byte, burst+rate), 0o644)
	start := time.Now()
	h.send(`{"t":"get","xfer":1,"path":%q}`, p)
	h.ch.wait(t, "end", 1)
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Fatalf("relayed download of %d bytes at %d B/s took %v", burst+rate, rate, d)
	}
}

// ---------- admission and protocol ----------

func TestAdmission(t *testing.T) {
	t.Run("max active", func(t *testing.T) {
		h := newHarness(t, nil)
		for x := uint32(1); x <= MaxActive; x++ {
			h.send(`{"t":"put","xfer":%d,"dir":%q,"name":"f%d","size":1}`, x, h.home, x)
			h.ch.wait(t, "ready", x)
		}
		h.send(`{"t":"put","xfer":99,"dir":%q,"name":"g","size":1}`, h.home)
		if e := h.ch.wait(t, "error", 99); e["code"] != CodeTooMany {
			t.Fatalf("error %v", e)
		}
	})
	t.Run("duplicate xfer", func(t *testing.T) {
		h := newHarness(t, nil)
		h.send(`{"t":"put","xfer":1,"dir":%q,"name":"f","size":1}`, h.home)
		h.ch.wait(t, "ready", 1)
		h.send(`{"t":"get","xfer":1,"path":%q}`, h.home)
		if e := h.ch.wait(t, "error", 1); e["code"] != CodeProtocol {
			t.Fatalf("error %v", e)
		}
	})
	t.Run("audit unavailable", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.AuditOK = func() bool { return false } })
		h.send(`{"t":"put","xfer":1,"dir":%q,"name":"f","size":1}`, h.home)
		if e := h.ch.wait(t, "error", 1); e["code"] != CodeAuditUnavailable {
			t.Fatalf("error %v", e)
		}
		time.Sleep(20 * time.Millisecond)
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.records) != 0 {
			t.Fatalf("refused transfer recorded: %v", h.records)
		}
	})
}

type gateHost struct {
	testHost
	gate chan struct{}
}

func (g gateHost) Roots() ([]Root, string, error) {
	<-g.gate
	return g.testHost.Roots()
}

func TestRequestLimit(t *testing.T) {
	gate := make(chan struct{})
	h := newHarness(t, func(c *Config) { c.Host = gateHost{testHost: c.Host.(testHost), gate: gate} })
	for id := 1; id <= MaxRequests+1; id++ {
		h.send(`{"t":"roots","id":%d}`, id)
	}
	if e := h.ch.wait(t, "error", MaxRequests+1); e["code"] != CodeTooMany {
		t.Fatalf("error %v", e)
	}
	close(gate)
	for id := uint32(1); id <= MaxRequests; id++ {
		h.ch.wait(t, "roots", id)
	}
}

func TestNoUser(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Host = testHost{err: ErrNoUser} })
	h.send(`{"t":"roots","id":1}`)
	h.send(`{"t":"put","xfer":2,"dir":"","name":"a","size":1}`)
	for _, ref := range []uint32{1, 2} {
		if e := h.ch.wait(t, "error", ref); e["code"] != CodeNoUser {
			t.Fatalf("error %v", e)
		}
	}
}

func TestProtocolErrors(t *testing.T) {
	cases := []struct {
		name   string
		str    bool
		frame  []byte
		answer bool
	}{
		{"bad json", true, []byte(`{"t":`), true},
		{"unknown message", true, []byte(`{"t":"rm","id":3}`), true},
		{"bad data frame", false, []byte{TypeData, 1}, true},
		{"data for unknown xfer", false, EncodeData(42, []byte("x")), false},
		{"ack for unknown xfer", true, []byte(`{"t":"ack","xfer":42,"bytes":1}`), false},
		{"cancel for unknown xfer", true, []byte(`{"t":"cancel","xfer":42}`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.svc.Handle(c.str, c.frame)
			time.Sleep(10 * time.Millisecond)
			n := h.ch.count(func(m map[string]any) bool { return m["t"] == "error" && m["code"] == CodeProtocol })
			if (n == 1) != c.answer {
				t.Fatalf("%d protocol errors sent", n)
			}
		})
	}
}

// ---------- roots and listings ----------

func TestRoots(t *testing.T) {
	h := newHarness(t, nil)
	h.send(`{"t":"roots","id":5}`)
	m := h.ch.wait(t, "roots", 5)
	downloads := filepath.Join(h.home, "Downloads")
	if m["default_dir"] != downloads || m["sep"] != string(filepath.Separator) {
		t.Fatalf("roots %v", m)
	}
	if got := fmt.Sprint(m["roots"]); got != fmt.Sprint([]any{
		map[string]any{"kind": "home", "name": "Home", "path": h.home},
		map[string]any{"kind": "downloads", "name": "Downloads", "path": downloads}}) {
		t.Fatalf("roots %s", got)
	}
}

func TestListing(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(h.home, "Docs")
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	for _, n := range []string{"file10.txt", "file2.txt", ".hidden", "B.md"} {
		_ = os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644)
	}
	_ = os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "link"))
	_ = os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "broken"))
	names := func(m map[string]any) string {
		var out []string
		for _, e := range m["entries"].([]any) {
			e := e.(map[string]any)
			out = append(out, fmt.Sprintf("%s:%s:%v", e["name"], e["kind"], e["link"]))
		}
		return strings.Join(out, " ")
	}
	h.send(`{"t":"list","id":1,"path":%q,"offset":0,"hidden":false}`, dir+"/./")
	m := h.ch.wait(t, "listing", 1)
	if got := names(m); got != "link:dir:true sub:dir:false B.md:file:false broken:other:true file2.txt:file:false file10.txt:file:false" {
		t.Fatalf("entries %s", got)
	}
	if m["path"] != dir || m["parent"] != h.home || m["total"] != float64(6) || m["next_offset"] != nil || m["truncated"] != false {
		t.Fatalf("listing %v", m)
	}
	h.send(`{"t":"list","id":2,"path":%q,"offset":0,"hidden":true}`, dir)
	if m := h.ch.wait(t, "listing", 2); m["total"] != float64(7) {
		t.Fatalf("with hidden: %v", names(m))
	}
	h.send(`{"t":"list","id":3,"path":%q,"offset":5,"hidden":false}`, dir)
	if m := h.ch.wait(t, "listing", 3); names(m) != "file10.txt:file:false" || m["offset"] != float64(5) {
		t.Fatalf("offset page: %v", m)
	}

	errs := []struct {
		id   uint32
		path string
		code string
	}{
		{10, filepath.Join(dir, "file2.txt"), CodeNotDir},
		{11, filepath.Join(dir, "missing"), CodeNotFound},
		{12, "relative", CodeBadPath},
	}
	for _, e := range errs {
		h.send(`{"t":"list","id":%d,"path":%q}`, e.id, e.path)
		if m := h.ch.wait(t, "error", e.id); m["code"] != e.code {
			t.Fatalf("list %s: %v, want %s", e.path, m, e.code)
		}
	}
	h.send(`{"t":"list","id":13,"path":%q}`, filepath.Join(h.home, "state"))
	if m := h.ch.wait(t, "error", 13); m["code"] != CodeDenied {
		t.Fatalf("denied list: %v", m)
	}
}

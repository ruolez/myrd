package session

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/agent/internal/platform"
	"github.com/ruolez/remotectl/proto"
)

// dirHost is a files.Host rooted at a temp directory.
type dirHost struct{ dir string }

func (d dirHost) Do(fn func() error) error { return fn() }

func (d dirHost) Roots() ([]files.Root, string, error) {
	return []files.Root{{Name: "Home", Path: d.dir, Kind: "home"}}, d.dir, nil
}

func withFiles(dir string) func(*Options) {
	return func(o *Options) {
		o.Files = dirHost{dir: dir}
		o.FileDeny = []string{filepath.Join(dir, "state")}
	}
}

// connectFiles runs a session to an open file channel and checks hello.
func (h *harness) connectFiles(t *testing.T, wantFeatures string) {
	t.Helper()
	h.offer(false)
	if err := <-h.answerOK; err != nil {
		t.Fatal(err)
	}
	if hello := h.waitControl(t, "hello"); fmt.Sprint(hello["features"]) != wantFeatures {
		t.Fatalf("hello features = %v, want %s", hello["features"], wantFeatures)
	}
}

func (h *harness) waitFile(t *testing.T, kind string, xfer int) map[string]any {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m := <-h.fileText:
			if m["t"] == "error" {
				t.Fatalf("file error waiting for %s: %v", kind, m)
			}
			if m["t"] == kind && m["xfer"] == float64(xfer) {
				return m
			}
		case <-timeout:
			t.Fatalf("no file %q for xfer %d", kind, xfer)
		}
	}
}

func (h *harness) sendFileData(t *testing.T, xfer uint32, b []byte) {
	t.Helper()
	for len(b) > 0 {
		n := min(len(b), files.MaxChunk)
		if err := h.file.Send(files.EncodeData(xfer, b[:n])); err != nil {
			t.Fatal(err)
		}
		b = b[n:]
	}
}

func (h *harness) fileRecords() []proto.FileRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []proto.FileRecord
	for _, m := range h.fileRecs {
		if m.SessionID == sessionID {
			out = append(out, m.Files...)
		}
	}
	return out
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestLoopbackFileUploadAndDownload(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, platform.ConsentOnce, withFiles(dir))
	h.connectFiles(t, "[stream move files file_ops]")
	waitFor(t, "file channel open", func() bool { return h.file.ReadyState() == webrtc.DataChannelStateOpen })

	content := make([]byte, 3<<19+333) // ~1.5 MiB
	_, _ = rand.Read(content)
	sum := hexSum(content)

	_ = h.file.SendText(fmt.Sprintf(`{"t":"put","xfer":1,"dir":"","name":"blob.bin","size":%d}`, len(content)))
	ready := h.waitFile(t, "ready", 1)
	if ready["name"] != "blob.bin" || ready["path"] != filepath.Join(dir, "blob.bin") {
		t.Fatalf("ready %v", ready)
	}
	h.sendFileData(t, 1, content)
	_ = h.file.SendText(fmt.Sprintf(`{"t":"end","xfer":1,"sha256":%q}`, sum))
	if done := h.waitFile(t, "done", 1); done["sha256"] != sum || done["size"] != float64(len(content)) {
		t.Fatalf("done %v", done)
	}
	onDisk, err := os.ReadFile(filepath.Join(dir, "blob.bin"))
	if err != nil || hexSum(onDisk) != sum {
		t.Fatalf("file on disk: %d bytes, %v", len(onDisk), err)
	}
	if parts, _ := filepath.Glob(filepath.Join(dir, ".*.part")); len(parts) != 0 {
		t.Fatalf("part files left: %v", parts)
	}

	_ = h.file.SendText(fmt.Sprintf(`{"t":"get","xfer":2,"path":%q}`, filepath.Join(dir, "blob.bin")))
	if begin := h.waitFile(t, "begin", 2); begin["size"] != float64(len(content)) {
		t.Fatalf("begin %v", begin)
	}
	var got bytes.Buffer
	for got.Len() < len(content) {
		select {
		case b := <-h.fileData:
			xfer, p, err := files.DecodeData(b)
			if err != nil || xfer != 2 {
				t.Fatalf("data frame xfer %d: %v", xfer, err)
			}
			got.Write(p)
		case <-time.After(10 * time.Second):
			t.Fatalf("download stalled at %d bytes", got.Len())
		}
	}
	end := h.waitFile(t, "end", 2)
	if end["sha256"] != sum || hexSum(got.Bytes()) != sum {
		t.Fatalf("download sha: wire %v, computed %s, want %s", end["sha256"], hexSum(got.Bytes()), sum)
	}
	_ = h.file.SendText(fmt.Sprintf(`{"t":"ack","xfer":2,"bytes":%d}`, len(content)))

	waitFor(t, "audit records", func() bool { return len(h.fileRecords()) == 2 })
	for _, r := range h.fileRecords() {
		if r.Result != "ok" || r.SHA256 != sum || r.Bytes != int64(len(content)) || r.Name != "blob.bin" || r.Dir != dir {
			t.Fatalf("record %+v", r)
		}
	}
	if recs := h.fileRecords(); recs[0].Direction != "upload" || recs[1].Direction != "download" {
		t.Fatalf("record order %+v", recs)
	}
}

func TestLoopbackSessionEndMidUpload(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, platform.ConsentOnce, withFiles(dir))
	h.connectFiles(t, "[stream move files file_ops]")
	waitFor(t, "file channel open", func() bool { return h.file.ReadyState() == webrtc.DataChannelStateOpen })

	_ = h.file.SendText(`{"t":"put","xfer":5,"dir":"","name":"big.iso","size":10485760}`)
	h.waitFile(t, "ready", 5)
	h.sendFileData(t, 5, make([]byte, 1<<20))
	part := filepath.Join(dir, files.PartName("big.iso", 5))
	waitFor(t, "part file written", func() bool { st, err := os.Stat(part); return err == nil && st.Size() > 0 })

	end, _ := proto.Encode(proto.TypeSessionEnd, proto.SessionEnd{SessionID: sessionID, Reason: proto.EndUserHangup})
	h.mgr.HandleSignal(proto.TypeSessionEnd, end)
	waitFor(t, "session.end", func() bool { _, _, e := h.snapshot(); return len(e) > 0 })

	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("part file still there: %v", err)
	}
	recs := h.fileRecords()
	if len(recs) != 1 || recs[0].Result != "cancelled" || recs[0].Error != "cancelled" || recs[0].Name != "big.iso" {
		t.Fatalf("records %+v", recs)
	}
	h.mu.Lock()
	order := strings.Join(h.signalOrder, ",")
	h.mu.Unlock()
	if order != proto.TypeSessionFile+","+proto.TypeSessionEnd {
		t.Fatalf("signal order %s", order)
	}
}

func TestLoopbackFileChannelClosedWithoutFiles(t *testing.T) {
	h := newHarness(t, platform.ConsentOnce)
	h.connectFiles(t, "[stream move]")
	waitFor(t, "file channel closed by the agent", func() bool { return h.file.ReadyState() == webrtc.DataChannelStateClosed })
	if h.control.ReadyState() != webrtc.DataChannelStateOpen {
		t.Fatal("control channel affected")
	}
}

package session

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/proto"
)

func TestNextBatch(t *testing.T) {
	q := func(spec string) []auditRecord { // "a:100 a:100 b:10" = session:size
		var out []auditRecord
		for _, f := range strings.Fields(spec) {
			id, size, _ := strings.Cut(f, ":")
			n := 0
			for _, c := range size {
				n = n*10 + int(c-'0')
			}
			out = append(out, auditRecord{sessionID: id, size: n})
		}
		return out
	}
	cases := []struct {
		name       string
		q          []auditRecord
		maxRecords int
		maxBytes   int
		want       int
	}{
		{"empty", nil, 50, 1000, 0},
		{"all fit", q("a:10 a:10 a:10"), 50, 1000, 3},
		{"record cap", q("a:1 a:1 a:1 a:1"), 3, 1000, 3},
		{"byte cap", q("a:400 a:400 a:400"), 50, 1000, 2},
		{"exact byte fit", q("a:500 a:500 a:1"), 50, 1000, 2},
		{"oversized single still sent", q("a:5000 a:1"), 50, 1000, 1},
		{"stops at the next session", q("a:1 a:1 b:1 a:1"), 50, 1000, 2},
		{"other session first", q("b:1 a:1"), 50, 1000, 1},
	}
	for _, c := range cases {
		if got := nextBatch(c.q, c.maxRecords, c.maxBytes); got != c.want {
			t.Errorf("%s: nextBatch = %d, want %d", c.name, got, c.want)
		}
	}
}

// fakeSignal captures session.file messages; fail makes sends error.
type fakeSignal struct {
	mu   sync.Mutex
	msgs []proto.SessionFile
	fail bool
}

func (f *fakeSignal) send(typ string, msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("not connected")
	}
	if typ == proto.TypeSessionFile {
		f.msgs = append(f.msgs, msg.(proto.SessionFile))
	}
	return nil
}

func (f *fakeSignal) batches() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.msgs {
		var xs []string
		for _, r := range m.Files {
			xs = append(xs, r.Name)
		}
		out = append(out, m.SessionID+"["+strings.Join(xs, ",")+"]")
	}
	return strings.Join(out, " ")
}

func newTestAudit(f *fakeSignal) *fileAudit {
	return newFileAudit(f.send, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func rec(name string) proto.FileRecord {
	return proto.FileRecord{Direction: "upload", Name: name, Result: "ok", SHA256: strings.Repeat("0", 64)}
}

func TestFileAuditFlushSendsOnlyThatSession(t *testing.T) {
	f := &fakeSignal{}
	a := newTestAudit(f)
	a.record("s1", rec("a"))
	a.record("s2", rec("b"))
	a.record("s1", rec("c"))
	a.flush("s1")
	if got := f.batches(); got != "s1[a] s1[c]" {
		t.Fatalf("flush(s1) sent %q", got)
	}
	if a.pending("s2") != 1 || a.pending("") != 1 {
		t.Fatalf("pending s2=%d all=%d", a.pending("s2"), a.pending(""))
	}
}

func TestFileAuditBatchesAtTheRecordCap(t *testing.T) {
	f := &fakeSignal{}
	a := newTestAudit(f)
	for i := 0; i < proto.MaxFileRecords+3; i++ {
		a.record("s1", rec("r"))
	}
	a.flush("s1")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) != 2 || len(f.msgs[0].Files) != proto.MaxFileRecords || len(f.msgs[1].Files) != 3 {
		t.Fatalf("batches %d", len(f.msgs))
	}
	for _, m := range f.msgs {
		if b, _ := json.Marshal(m); len(b) > proto.MaxMessageBytes {
			t.Fatalf("message of %d bytes", len(b))
		}
	}
}

func TestFileAuditBatchesBySize(t *testing.T) {
	f := &fakeSignal{}
	a := newTestAudit(f)
	big := rec(strings.Repeat("n", 1000))
	for i := 0; i < proto.MaxFileRecords; i++ {
		a.record("s1", big)
	}
	a.flush("s1")
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, m := range f.msgs {
		b, _ := json.Marshal(m)
		if len(b) > fileAuditBytes+1024 {
			t.Fatalf("message of %d bytes", len(b))
		}
		total += len(m.Files)
	}
	if len(f.msgs) < 2 || total != proto.MaxFileRecords {
		t.Fatalf("%d messages, %d records", len(f.msgs), total)
	}
}

func TestFileAuditRetriesAndRefusesWhenFull(t *testing.T) {
	f := &fakeSignal{fail: true}
	a := newTestAudit(f)
	a.capacity = 10
	for i := 0; i < a.capacity-files.MaxActive-files.MaxRequests-1; i++ {
		a.record("s1", rec("r"))
	}
	if !a.ok() {
		t.Fatal("refusing transfers with room in the queue")
	}
	a.record("s1", rec("r"))
	if a.ok() {
		t.Fatal("accepting transfers without room for their records")
	}
	// Running transfers still get their records in, up to the hard cap.
	for i := 0; i < files.MaxActive+files.MaxRequests+2; i++ {
		a.record("s1", rec("r"))
	}
	if n := a.pending(""); n != a.capacity {
		t.Fatalf("pending %d, want the cap %d", n, a.capacity)
	}
	a.flush("s1")
	if a.pending("") != a.capacity {
		t.Fatal("failed send dropped records")
	}
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	a.flush("s1")
	if a.pending("") != 0 || !a.ok() {
		t.Fatalf("after reconnect: pending %d ok %v", a.pending(""), a.ok())
	}
}

func TestFileAuditRunPacesAndCloseFlushes(t *testing.T) {
	f := &fakeSignal{}
	a := newTestAudit(f)
	a.interval = 20 * time.Millisecond
	go a.run()
	a.record("s1", rec("a"))
	a.record("s2", rec("b"))
	waitFor(t, "background sends", func() bool { return a.pending("") == 0 })
	if got := f.batches(); got != "s1[a] s2[b]" {
		t.Fatalf("sent %q", got)
	}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	a.record("s3", rec("c"))
	time.Sleep(3 * a.interval)
	f.mu.Lock()
	f.fail = false
	f.mu.Unlock()
	a.close()
	if got := f.batches(); got != "s1[a] s2[b] s3[c]" {
		t.Fatalf("after close sent %q", got)
	}
	a.close() // idempotent
}

package session

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/files"
	"github.com/ruolez/remotectl/agent/internal/terminal"
	"github.com/ruolez/remotectl/proto"
)

const (
	fileAuditQueue    = 1000
	fileAuditBytes    = 48 << 10
	fileAuditEvery    = 500 * time.Millisecond // ~2 msg/s, well under the hub's 20 msg/s
	fileAuditFlushMax = 5                      // batches sent inline before session.end
)

type auditRecord struct {
	sessionID string
	rec       proto.FileRecord
	term      *proto.TerminalRecord // set: a terminal record (session.terminal) instead of rec
	size      int                   // JSON bytes
}

// fileAudit reports finished transfers to the server (session.file), and
// terminal events (session.terminal), in batches of ≤ 50 records / 48 KiB,
// one session and one kind per message. It belongs to the Manager, so unsent records survive the session
// and a signaling reconnect; the queue is bounded, and while it is nearly
// full ok() refuses new transfers (audit_unavailable) so that every running
// transfer still has room for its record.
type fileAudit struct {
	send     Outbound
	log      *slog.Logger
	interval time.Duration
	capacity int

	mu    sync.Mutex
	queue []auditRecord
	sends sync.Mutex // one batch on the wire at a time

	stop chan struct{}
	done chan struct{}
}

func newFileAudit(send Outbound, log *slog.Logger) *fileAudit {
	return &fileAudit{send: send, log: log, interval: fileAuditEvery, capacity: fileAuditQueue,
		stop: make(chan struct{}), done: make(chan struct{})}
}

// record queues one transfer record; it never blocks.
func (a *fileAudit) record(sessionID string, r proto.FileRecord) {
	b, _ := json.Marshal(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queue) >= a.capacity {
		a.log.Error("file audit queue full, record dropped", "session", sessionID, "xfer", r.Xfer, "name", r.Name)
		return
	}
	a.queue = append(a.queue, auditRecord{sessionID: sessionID, rec: r, size: len(b) + 1})
}

// recordTerminal queues one terminal record; it never blocks.
func (a *fileAudit) recordTerminal(sessionID string, r proto.TerminalRecord) {
	b, _ := json.Marshal(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queue) >= a.capacity {
		a.log.Error("terminal audit queue full, record dropped", "session", sessionID, "term", r.Term, "action", r.Action)
		return
	}
	a.queue = append(a.queue, auditRecord{sessionID: sessionID, term: &r, size: len(b) + 1})
}

// auditMargin keeps room for the records of everything already running:
// each transfer and request ends with one, each terminal with two.
const auditMargin = files.MaxActive + files.MaxRequests + 2*terminal.MaxTerms + terminal.MaxRequests

// ok reports whether a new transfer or terminal may start.
func (a *fileAudit) ok() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.queue)+auditMargin < a.capacity
}

func (a *fileAudit) pending(sessionID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, q := range a.queue {
		if sessionID == "" || q.sessionID == sessionID {
			n++
		}
	}
	return n
}

// nextBatch returns how many leading queue entries form the next message:
// the same session and kind, at most maxRecords records and maxBytes of
// records.
func nextBatch(q []auditRecord, maxRecords, maxBytes int) int {
	n, size := 0, 0
	for n < len(q) && n < maxRecords && q[n].sessionID == q[0].sessionID && (q[n].term == nil) == (q[0].term == nil) {
		if n > 0 && size+q[n].size > maxBytes {
			break
		}
		size += q[n].size
		n++
	}
	return n
}

// sendNext sends the oldest batch (of sessionID, if not "") and drops it
// from the queue once the send succeeded. false: nothing sent.
func (a *fileAudit) sendNext(sessionID string) bool {
	a.sends.Lock()
	defer a.sends.Unlock()
	a.mu.Lock()
	start := 0
	if sessionID != "" {
		for start < len(a.queue) && a.queue[start].sessionID != sessionID {
			start++
		}
	}
	n := nextBatch(a.queue[start:], proto.MaxFileRecords, fileAuditBytes)
	if n == 0 {
		a.mu.Unlock()
		return false
	}
	id := a.queue[start].sessionID
	typ, msg := proto.TypeSessionFile, any(nil)
	if a.queue[start].term != nil {
		batch := make([]proto.TerminalRecord, n)
		for i := range batch {
			batch[i] = *a.queue[start+i].term
		}
		typ, msg = proto.TypeSessionTerminal, proto.SessionTerminal{SessionID: id, Terminals: batch}
	} else {
		batch := make([]proto.FileRecord, n)
		for i := range batch {
			batch[i] = a.queue[start+i].rec
		}
		msg = proto.SessionFile{SessionID: id, Files: batch}
	}
	a.mu.Unlock()
	if err := a.send(typ, msg); err != nil {
		a.log.Debug("file audit send failed, will retry", "session", id, "records", n, "err", err)
		return false
	}
	// Only this goroutine removes entries and record only appends, so the
	// batch is still at start.
	a.mu.Lock()
	a.queue = append(a.queue[:start:start], a.queue[start+n:]...)
	a.mu.Unlock()
	return true
}

// flush sends a session's records before its session.end, a few batches
// inline; anything left (or unsendable now) goes out later from run.
func (a *fileAudit) flush(sessionID string) {
	for i := 0; i < fileAuditFlushMax && a.sendNext(sessionID); i++ {
	}
}

// run sends one batch per interval until close.
func (a *fileAudit) run() {
	defer close(a.done)
	tick := time.NewTicker(a.interval)
	defer tick.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-tick.C:
			a.sendNext("")
		}
	}
}

// close stops run after a last flush attempt.
func (a *fileAudit) close() {
	select {
	case <-a.stop:
		return
	default:
	}
	close(a.stop)
	<-a.done
	for i := 0; i < fileAuditFlushMax && a.sendNext(""); i++ {
	}
	if n := a.pending(""); n > 0 {
		a.log.Warn("file audit records not delivered", "records", n)
	}
}

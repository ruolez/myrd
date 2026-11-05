package terminal

import (
	"io"
	"sync"
)

// queueCap bounds a viewer's unsent output. A full queue blocks the shell's
// reader, so a slow viewer slows the program instead of losing output.
const queueCap = 1 << 20

// byteQueue is a viewer's pending output: one producer (the shell reader),
// one consumer (the viewer's pump).
type byteQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	closed bool
}

func newByteQueue() *byteQueue {
	q := &byteQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends p, waiting while the queue is full. false: the queue closed.
func (q *byteQueue) push(p []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for !q.closed && len(q.data)+len(p) > queueCap && len(q.data) > 0 {
		q.cond.Wait()
	}
	if q.closed {
		return false
	}
	q.data = append(q.data, p...)
	q.cond.Broadcast()
	return true
}

// read waits for data; io.EOF once closed and drained.
func (q *byteQueue) read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.data) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, q.data)
	q.data = q.data[n:]
	if len(q.data) == 0 {
		q.data = nil // let a large backlog's array go
	}
	q.cond.Broadcast()
	return n, nil
}

// close ends the queue; read still returns what is pending first.
func (q *byteQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// drop ends the queue and discards what is pending.
func (q *byteQueue) drop() {
	q.mu.Lock()
	q.closed = true
	q.data = nil
	q.cond.Broadcast()
	q.mu.Unlock()
}

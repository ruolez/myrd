package files

import "errors"

var ErrCredit = errors.New("flow-control violation")

// sendWindow tracks a sender's credit: at most size bytes beyond the
// receiver's last ack may be in flight.
type sendWindow struct {
	size, sent, acked int64
}

func (w *sendWindow) canSend(n int) bool { return w.sent+int64(n)-w.acked <= w.size }

func (w *sendWindow) add(n int) { w.sent += int64(n) }

// ack applies a cumulative ack; it may not go backwards or past what was sent.
func (w *sendWindow) ack(bytes int64) error {
	if bytes < w.acked || bytes > w.sent {
		return ErrCredit
	}
	w.acked = bytes
	return nil
}

// recvWindow is the receiving side: it rejects data beyond the credit the
// receiver has granted (its last ack sent + size) and decides when to ack.
type recvWindow struct {
	size, received, written, ackSent int64
}

// receive accounts n bytes arriving off the wire.
func (w *recvWindow) receive(n int) error {
	if w.received+int64(n) > w.ackSent+w.size {
		return ErrCredit
	}
	w.received += int64(n)
	return nil
}

// wrote accounts n bytes written to disk and reports whether an ack is due.
func (w *recvWindow) wrote(n int) (ackDue bool) {
	w.written += int64(n)
	return w.written-w.ackSent >= AckEvery
}

// acked records that an ack for everything written was sent.
func (w *recvWindow) acked() int64 {
	w.ackSent = w.written
	return w.ackSent
}

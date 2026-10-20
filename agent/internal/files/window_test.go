package files

import (
	"math/rand"
	"testing"
)

func TestSendWindow(t *testing.T) {
	const size = 100
	type step struct {
		send  int   // try to send this many bytes (0 = skip)
		ack   int64 // then apply this ack (-1 = none)
		can   bool
		ackOK bool
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"fills exactly", []step{{send: 60, ack: -1, can: true}, {send: 40, ack: -1, can: true}, {send: 1, ack: -1, can: false}}},
		{"ack reopens", []step{{send: 100, ack: 30, can: true, ackOK: true}, {send: 30, ack: -1, can: true}, {send: 1, ack: -1, can: false}}},
		{"ack past sent", []step{{send: 10, ack: 11, can: true, ackOK: false}}},
		{"ack backwards", []step{{send: 50, ack: 40, can: true, ackOK: true}, {ack: 39, ackOK: false}}},
		{"ack equal is fine", []step{{send: 50, ack: 50, can: true, ackOK: true}, {ack: 50, ackOK: true}}},
	}
	for _, c := range cases {
		w := sendWindow{size: size}
		for i, s := range c.steps {
			if s.send > 0 {
				if got := w.canSend(s.send); got != s.can {
					t.Fatalf("%s step %d: canSend(%d) = %v", c.name, i, s.send, got)
				}
				if s.can {
					w.add(s.send)
				}
			}
			if s.ack >= 0 {
				if err := w.ack(s.ack); (err == nil) != s.ackOK {
					t.Fatalf("%s step %d: ack(%d) err = %v", c.name, i, s.ack, err)
				}
			}
		}
	}
}

func TestRecvWindow(t *testing.T) {
	w := recvWindow{size: Window}
	if err := w.receive(Window); err != nil {
		t.Fatalf("full window rejected: %v", err)
	}
	if err := w.receive(1); err == nil {
		t.Fatal("byte past the window accepted")
	}
	if due := w.wrote(AckEvery - 1); due {
		t.Fatal("ack due before AckEvery bytes")
	}
	if due := w.wrote(1); !due {
		t.Fatal("ack not due at AckEvery bytes")
	}
	if got := w.acked(); got != AckEvery {
		t.Fatalf("acked = %d", got)
	}
	if err := w.receive(AckEvery); err != nil {
		t.Fatalf("credit from the ack not granted: %v", err)
	}
	if err := w.receive(1); err == nil {
		t.Fatal("byte past the regranted window accepted")
	}
}

// A sender that respects sendWindow never overruns a receiver that acks as
// recvWindow says, whatever the chunk sizes and ack timing.
func TestWindowsAgreeProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for run := 0; run < 100; run++ {
		snd := sendWindow{size: Window}
		rcv := recvWindow{size: Window}
		var inflight []int // sent, not yet received
		var acks []int64   // acks in flight to the sender
		total := int64(0)
		for total < 32<<20 {
			switch rng.Intn(3) {
			case 0:
				n := 1 + rng.Intn(MaxChunk)
				if snd.canSend(n) {
					snd.add(n)
					inflight = append(inflight, n)
					total += int64(n)
				}
			case 1:
				if len(inflight) > 0 {
					n := inflight[0]
					inflight = inflight[1:]
					if err := rcv.receive(n); err != nil {
						t.Fatalf("run %d: receiver rejected a compliant sender", run)
					}
					if rcv.wrote(n) || rng.Intn(50) == 0 {
						acks = append(acks, rcv.acked())
					}
				}
			case 2:
				if len(acks) > 0 {
					if err := snd.ack(acks[0]); err != nil {
						t.Fatalf("run %d: sender rejected ack %d: %v", run, acks[0], err)
					}
					acks = acks[1:]
				}
			}
		}
	}
}

package update

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ruolez/remotectl/proto"
)

func TestPacerCoalescesAndKeepsTheLastState(t *testing.T) {
	const every = 60 * time.Millisecond
	var mu sync.Mutex
	var sent []string
	var at []time.Time
	p := newPacer(every, func(s proto.UpdateStatus) {
		mu.Lock()
		sent, at = append(sent, s.State), append(at, time.Now())
		mu.Unlock()
	})
	start := time.Now()
	for _, s := range []string{proto.UpdateChecking, proto.UpdateDownloading, proto.UpdateWaitingIdle} {
		p.push(proto.UpdateStatus{State: s})
	}
	p.flush(context.Background())
	mu.Lock()
	got, gap := append([]string(nil), sent...), at[len(at)-1].Sub(start)
	mu.Unlock()
	if want := []string{proto.UpdateChecking, proto.UpdateWaitingIdle}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	if gap < every {
		t.Fatalf("second status after %v, want at least %v", gap, every)
	}

	time.Sleep(every)
	p.push(proto.UpdateStatus{State: proto.UpdateInstalling})
	mu.Lock()
	got = append([]string(nil), sent...)
	mu.Unlock()
	if want := []string{proto.UpdateChecking, proto.UpdateWaitingIdle, proto.UpdateInstalling}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after a quiet spell sent %v, want %v (immediately)", got, want)
	}
}

func TestPacerUnpaced(t *testing.T) {
	var sent []string
	p := newPacer(-1, func(s proto.UpdateStatus) { sent = append(sent, s.State) })
	p.push(proto.UpdateStatus{State: proto.UpdateChecking})
	p.push(proto.UpdateStatus{State: proto.UpdateUpToDate})
	p.flush(context.Background())
	if want := []string{proto.UpdateChecking, proto.UpdateUpToDate}; !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent %v, want %v", sent, want)
	}
}

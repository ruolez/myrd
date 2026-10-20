package devconsole

import (
	"testing"
	"time"

	"github.com/ruolez/remotectl/agent/internal/files"
)

func TestTransferLog(t *testing.T) {
	t0 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	var l TransferLog
	l.Add(files.Activity{Xfer: 1, State: files.StateActive, Started: t0})
	l.Add(files.Activity{Xfer: 1, State: files.StateOK, Bytes: 5, Started: t0})
	l.Add(files.Activity{Xfer: 1, State: files.StateActive, Started: t0.Add(time.Hour)}) // next session reuses xfer 1
	got := l.List()
	if len(got) != 2 || got[0].Started != t0.Add(time.Hour) || got[1].State != files.StateOK || got[1].Bytes != 5 {
		t.Fatalf("log %+v", got)
	}
	for i := 0; i < MaxTransfers+5; i++ {
		l.Add(files.Activity{Xfer: uint32(100 + i), Started: t0})
	}
	got = l.List()
	if len(got) != MaxTransfers || got[0].Xfer != uint32(100+MaxTransfers+4) {
		t.Fatalf("capped log: %d entries, newest %d", len(got), got[0].Xfer)
	}
	got[0].Name = "mutated"
	if l.List()[0].Name == "mutated" {
		t.Fatal("List returned the internal slice")
	}
}

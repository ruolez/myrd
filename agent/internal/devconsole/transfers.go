package devconsole

import (
	"sync"

	"github.com/ruolez/remotectl/agent/internal/files"
)

// MaxTransfers is how many recent file transfers the console lists.
const MaxTransfers = 20

// TransferLog keeps the latest file transfers for Status.Transfers,
// updating each transfer in place as its activity arrives.
type TransferLog struct {
	mu    sync.Mutex
	items []files.Activity // newest first
}

func (l *TransferLog) Add(a files.Activity) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, it := range l.items {
		// xfer ids restart with every browser session; Started tells them apart.
		if it.Xfer == a.Xfer && it.Started.Equal(a.Started) {
			l.items[i] = a
			return
		}
	}
	l.items = append([]files.Activity{a}, l.items...)
	if len(l.items) > MaxTransfers {
		l.items = l.items[:MaxTransfers]
	}
}

// List returns a copy, newest first.
func (l *TransferLog) List() []files.Activity {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]files.Activity{}, l.items...)
}

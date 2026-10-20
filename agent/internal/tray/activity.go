package tray

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ruolez/remotectl/agent/internal/files"
)

const (
	// ResultLinger is how long a finished transfer's result stays shown.
	ResultLinger = 10 * time.Second
	// NotifyEvery coalesces transfer-start notifications.
	NotifyEvery = 10 * time.Second
)

// ActivityLine describes one transfer, e.g. "Receiving report.pdf (42%)".
func ActivityLine(a files.Activity) string {
	up := a.Direction == files.DirUpload
	switch a.State {
	case files.StateActive:
		verb := "Sending"
		if up {
			verb = "Receiving"
		}
		if a.Size > 0 {
			return fmt.Sprintf("%s %s (%d%%)", verb, a.Name, a.Bytes*100/a.Size)
		}
		return verb + " " + a.Name
	case files.StateOK:
		if up {
			return "Received " + a.Name
		}
		return "Sent " + a.Name
	case files.StateCancelled:
		return "Cancelled " + a.Name
	}
	if up {
		return "Failed to receive " + a.Name
	}
	return "Failed to send " + a.Name
}

// Summary is the tray's activity line: the newest running transfer (with
// a count of the others), else the newest result younger than ResultLinger.
func Summary(acts []files.Activity, now time.Time) string {
	var active, done []files.Activity
	for _, a := range acts {
		switch {
		case a.State == files.StateActive:
			active = append(active, a)
		case now.Sub(a.Updated) < ResultLinger:
			done = append(done, a)
		}
	}
	newest := func(as []files.Activity) files.Activity {
		sort.Slice(as, func(i, j int) bool { return as[i].Started.After(as[j].Started) })
		return as[0]
	}
	switch {
	case len(active) == 1:
		return ActivityLine(active[0])
	case len(active) > 1:
		return fmt.Sprintf("%s + %d more", ActivityLine(newest(active)), len(active)-1)
	case len(done) > 0:
		sort.Slice(done, func(i, j int) bool { return done[i].Updated.After(done[j].Updated) })
		return ActivityLine(done[0])
	}
	return ""
}

// ActivityFeed folds a session's transfer activity into the tray's
// Activity line (onLine is called whenever it changes, also when a result
// expires) and posts a coalesced notification when transfers start.
type ActivityFeed struct {
	onLine func(string)
	notify func(title, body string)
	now    func() time.Time

	mu         sync.Mutex
	acts       map[uint32]files.Activity
	line       string
	lastNotify time.Time
	timer      *time.Timer
}

// NewActivityFeed; notify may be nil (no notifications).
func NewActivityFeed(onLine func(string), notify func(title, body string)) *ActivityFeed {
	return &ActivityFeed{onLine: onLine, notify: notify, now: time.Now, acts: map[uint32]files.Activity{}}
}

func (f *ActivityFeed) Add(a files.Activity) {
	f.mu.Lock()
	prev, seen := f.acts[a.Xfer]
	f.acts[a.Xfer] = a
	now := f.now()
	var body string
	if a.State == files.StateActive && (!seen || !prev.Started.Equal(a.Started)) && now.Sub(f.lastNotify) >= NotifyEvery {
		f.lastNotify = now
		body = ActivityLine(files.Activity{Direction: a.Direction, Name: a.Name, State: files.StateActive})
	}
	f.mu.Unlock()
	if body != "" && f.notify != nil {
		f.notify(ProductName+": file transfer", body)
	}
	f.refresh()
}

// refresh recomputes the line, forgets expired results and schedules the
// next expiry.
func (f *ActivityFeed) refresh() {
	f.mu.Lock()
	now := f.now()
	list := make([]files.Activity, 0, len(f.acts))
	var next time.Duration
	for x, a := range f.acts {
		if a.State != files.StateActive {
			left := ResultLinger - now.Sub(a.Updated)
			if left <= 0 {
				delete(f.acts, x)
				continue
			}
			if next == 0 || left < next {
				next = left
			}
		}
		list = append(list, a)
	}
	line := Summary(list, now)
	changed := line != f.line
	f.line = line
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	if next > 0 {
		f.timer = time.AfterFunc(next, f.refresh)
	}
	f.mu.Unlock()
	if changed && f.onLine != nil {
		f.onLine(line)
	}
}

// Line is the current activity line.
func (f *ActivityFeed) Line() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.line
}

//go:build darwin

package terminal

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <CoreServices/CoreServices.h>
#include <stdlib.h>
#include <string.h>

// rc_ae_permission: may this process send Apple Events to the app with
// bundle id b? 0 yes, -1743 denied, -1744 not asked yet, -600 not running.
// ask shows the prompt and waits for the answer.
static int rc_ae_permission(const char *b, int ask) {
	AEAddressDesc t;
	OSStatus e = AECreateDesc(typeApplicationBundleID, b, strlen(b), &t);
	if (e) return e;
	e = AEDeterminePermissionToAutomateTarget(&t, typeWildCard, typeWildCard, ask ? true : false);
	AEDisposeDesc(&t);
	return e;
}

// rc_bundle_id copies this app's bundle id into out ("" outside a bundle).
static void rc_bundle_id(char *out, int n) {
	out[0] = 0;
	CFBundleRef b = CFBundleGetMainBundle();
	CFStringRef id = b ? CFBundleGetIdentifier(b) : NULL;
	if (id) CFStringGetCString(id, out, n, kCFStringEncodingUTF8);
}

static void rc_post_key(int pid, int code, unsigned int ch, unsigned long long flags) {
	CGEventRef down = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, true);
	CGEventRef up = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)code, false);
	if (!down || !up) {
		if (down) CFRelease(down);
		if (up) CFRelease(up);
		return;
	}
	if (ch) {
		UniChar u[2];
		int n = 1;
		if (ch > 0xffff) {
			ch -= 0x10000;
			u[0] = 0xd800 + (ch >> 10);
			u[1] = 0xdc00 + (ch & 0x3ff);
			n = 2;
		} else {
			u[0] = (UniChar)ch;
		}
		CGEventKeyboardSetUnicodeString(down, n, u);
		CGEventKeyboardSetUnicodeString(up, n, u);
	}
	CGEventSetFlags(down, (CGEventFlags)flags);
	CGEventSetFlags(up, (CGEventFlags)flags);
	CGEventPostToPid(pid, down);
	CGEventPostToPid(pid, up);
	CFRelease(down);
	CFRelease(up);
}
*/
import "C"

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/ruolez/remotectl/proto"
)

const osascript = "/usr/bin/osascript"

// listAppsScript prints the open Terminal.app tabs and iTerm2 sessions
// (parseAppList), without launching either app; argv: the kinds to list,
// comma separated (only apps the agent may control, or osascript would
// raise the permission prompt itself).
const listAppsScript = `
function clean(s) { return String(s || '').replace(/[\t\r\n]/g, ' ') }
function run(argv) {
	const out = [], kinds = String(argv[0] || '').split(',');
	if (kinds.includes('terminal')) try {
		const t = Application('com.apple.Terminal');
		if (t.running()) {
			for (const w of t.windows()) for (const tab of w.tabs()) {
				let p = '';
				try { const ps = tab.processes(); p = ps.length ? ps[ps.length - 1] : '' } catch (e) {}
				out.push(['terminal', clean(tab.tty()), clean(tab.customTitle()), clean(p)].join('\t'));
			}
		}
	} catch (e) {}
	if (kinds.includes('iterm')) try {
		const it = Application('com.googlecode.iterm2');
		if (it.running()) {
			for (const w of it.windows()) for (const tab of w.tabs()) for (const s of tab.sessions()) {
				out.push(['iterm', clean(s.id()), clean(s.name()), clean(s.tty())].join('\t'));
			}
		}
	} catch (e) {}
	return out.join('\n');
}`

// mirrorScript prints a tab's screen as one JSON line whenever it changes,
// until the tab closes; argv: app, key (Terminal: tty, iTerm2: session id).
const mirrorScript = `
ObjC.import('Foundation');
function emit(s) {
	$.NSFileHandle.fileHandleWithStandardOutput.writeData($(s + '\n').dataUsingEncoding($.NSUTF8StringEncoding));
}
function find(app, kind, key) {
	for (const w of app.windows()) for (const t of w.tabs()) {
		if (kind === 'terminal') { if (t.tty() === key) return {w: w, t: t} }
		else for (const s of t.sessions()) if (s.id() === key) return {w: w, t: t, s: s};
	}
	return null;
}
function run(argv) {
	const kind = argv[0], key = argv[1];
	const app = Application(kind === 'terminal' ? 'com.apple.Terminal' : 'com.googlecode.iterm2');
	// Reading a tab takes ~2 ms: poll fast while it changes (typing), slower when idle.
	let last = '', f = null, misses = 0, hot = 0;
	for (;;) {
		let m = null;
		try {
			if (!f) f = find(app, kind, key);
			if (f && kind === 'terminal') {
				m = {text: f.t.contents(), rows: f.t.numberOfRows(), cols: f.t.numberOfColumns(), title: f.t.customTitle() || '',
					front: f.w.index() === 1 && f.w.selectedTab().tty() === key};
			} else if (f) {
				m = {text: f.s.contents(), rows: f.s.rows(), cols: f.s.columns(), title: f.s.name(), front: true};
			}
		} catch (e) { f = null }
		if (!m) { if (++misses > 4) return; delay(0.3); continue }
		misses = 0;
		const s = JSON.stringify(m);
		if (s !== last) { emit(s); last = s; hot = Date.now() + 2000 }
		delay(Date.now() < hot ? 0.03 : 0.1);
	}
}`

// selectScript brings a Terminal tab to the front of Terminal's windows
// (not Terminal to the front), so key events reach it.
const selectScript = `
function run(argv) {
	const app = Application('com.apple.Terminal');
	for (const w of app.windows()) for (const t of w.tabs()) if (t.tty() === argv[0]) { t.selected = true; w.index = 1; return 'ok' }
	return '';
}`

// itermWriteScript types into an iTerm2 session (argv: session id) each
// JSON string it reads on stdin, one per line, until stdin closes: one
// process for all typing, not one per key.
const itermWriteScript = `
ObjC.import('Foundation');
function run(argv) {
	const app = Application('com.googlecode.iterm2');
	const find = () => {
		for (const w of app.windows()) for (const t of w.tabs()) for (const s of t.sessions()) if (s.id() === argv[0]) return s;
		return null;
	};
	const input = $.NSFileHandle.fileHandleWithStandardInput;
	let s = null, buf = '';
	for (;;) {
		const d = input.availableData;
		if (d.length == 0) return;
		buf += $.NSString.alloc.initWithDataEncoding(d, $.NSUTF8StringEncoding).js;
		for (let i; (i = buf.indexOf('\n')) >= 0; buf = buf.slice(i + 1)) {
			const text = JSON.parse(buf.slice(0, i));
			try { if (!s) s = find(); s.write({text: text, newline: false}) }
			catch (e) { s = find(); if (s) try { s.write({text: text, newline: false}) } catch (e2) {} }
		}
	}
}`

func runScript(timeout time.Duration, script string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, osascript, append([]string{"-l", "JavaScript", "-e", script}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// appPID is the process id of the running app whose executable is named
// name (0: not running).
func appPID(name string) int {
	out, err := exec.Command("/bin/ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return 0
	}
	return findPID(string(out), name)
}

// termApp is a terminal app whose tabs the agent mirrors.
type termApp struct{ name, exe, bundle, kind string }

var termApps = []termApp{
	{"Terminal", "Terminal", "com.apple.Terminal", appTerminal},
	{"iTerm2", "iTerm2", "com.googlecode.iterm2", appITerm},
}

const (
	aeAllowed    = 0
	aeDenied     = -1743
	aeNotAsked   = -1744
	aeNotRunning = -600
)

func aePermission(bundle string, ask bool) int {
	b := C.CString(bundle)
	defer C.free(unsafe.Pointer(b))
	a := C.int(0)
	if ask {
		a = 1
	}
	return int(C.rc_ae_permission(b, a))
}

var (
	asking   sync.Map     // bundle ids whose prompt is showing now
	checking sync.Map     // bundle ids with a status query in flight
	known    sync.Map     // bundle id → the last status seen
	prompts  atomic.Int32 // prompts showing now
)

// checkWait bounds a status query: a list must not hang on one.
const checkWait = 2 * time.Second

// askPermission shows macOS's "control <app>" prompt once at a time per app
// and waits for the person at the Mac, however long that takes: a prompt
// cut short is recorded as a denial.
func askPermission(log *slog.Logger, a termApp) {
	if _, busy := asking.LoadOrStore(a.bundle, true); busy {
		return
	}
	prompts.Add(1)
	go func() {
		defer func() {
			prompts.Add(-1)
			asking.Delete(a.bundle)
		}()
		log.Info("terminal: asking the person at the Mac to allow controlling " + a.name)
		st := aePermission(a.bundle, true)
		known.Store(a.bundle, st)
		log.Info("terminal: permission to control "+a.name, "result", st)
	}()
}

// permission is the agent's Automation status for an app, without asking.
// While one of the agent's prompts is up, macOS holds its other permission
// queries until the prompt is answered, so then, or when a query takes too
// long, it is the last status seen; ok is false when there is none.
func permission(bundle string) (st int, ok bool) {
	if prompts.Load() == 0 {
		if _, busy := checking.LoadOrStore(bundle, true); !busy {
			ch := make(chan int, 1)
			go func() {
				defer checking.Delete(bundle)
				st := aePermission(bundle, false)
				known.Store(bundle, st)
				ch <- st
			}()
			select {
			case st := <-ch:
				return st, true
			case <-time.After(checkWait):
			}
		}
	}
	if v, found := known.Load(bundle); found {
		return v.(int), true
	}
	return 0, false
}

// appSources lists Terminal.app tabs and iTerm2 sessions, and the apps it
// may not look into yet. An app never asked about gets macOS's prompt on
// the Mac's screen; until someone answers it, its tabs are reported blocked.
func appSources(log *slog.Logger) ([]Source, []Blocked) {
	var kinds []string
	var blocked []Blocked
	for _, a := range termApps {
		if appPID(a.exe) == 0 {
			continue
		}
		if _, busy := asking.Load(a.bundle); busy {
			blocked = append(blocked, Blocked{App: a.name, Reason: BlockedAsking})
			continue
		}
		st, ok := permission(a.bundle)
		if !ok {
			log.Warn("terminal: no answer about the permission to control " + a.name)
			continue
		}
		switch st {
		case aeAllowed:
			kinds = append(kinds, a.kind)
		case aeNotRunning:
		case aeNotAsked:
			askPermission(log, a)
			blocked = append(blocked, Blocked{App: a.name, Reason: BlockedAsking})
		case aeDenied:
			blocked = append(blocked, Blocked{App: a.name, Reason: BlockedDenied})
		default:
			log.Warn("terminal: permission check failed", "app", a.name, "status", st)
			kinds = append(kinds, a.kind)
		}
	}
	if len(kinds) == 0 {
		return nil, blocked
	}
	out, err := runScript(10*time.Second, listAppsScript, strings.Join(kinds, ","))
	if err != nil {
		log.Warn("terminal: listing terminal app tabs failed", "err", scriptErr(err))
		return nil, blocked
	}
	return parseAppList(out), blocked
}

// askAppsAgain clears the agent's recorded Automation answers and asks
// again for every running terminal app.
func askAppsAgain(log *slog.Logger) {
	var buf [256]C.char
	C.rc_bundle_id(&buf[0], C.int(len(buf)))
	if id := C.GoString(&buf[0]); id != "" {
		if out, err := exec.Command("/usr/bin/tccutil", "reset", "AppleEvents", id).CombinedOutput(); err != nil {
			log.Warn("terminal: resetting the Automation permission failed", "err", err, "out", strings.TrimSpace(string(out)))
		}
	}
	for _, a := range termApps {
		known.Delete(a.bundle)
	}
	for _, a := range termApps {
		if appPID(a.exe) != 0 {
			askPermission(log, a)
		}
	}
}

func scriptErr(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return strings.TrimSpace(string(ee.Stderr))
	}
	return err.Error()
}

func (l *Local) attachApp(id string, cols, rows int) (Stream, Source, error) {
	var found *Source
	apps, _ := appSources(l.log)
	for _, s := range apps {
		if s.ID == id {
			found = &s
		}
	}
	if found == nil {
		return nil, Source{}, ErrNotFound
	}
	_, rest, _ := strings.Cut(id, ":")
	kind, key, _ := strings.Cut(rest, ":")
	if kind == appTerminal {
		key = "/dev/" + key
	}
	st, _, err := l.shells.Run(func() (Proc, error) { return startMirror(kind, key, cols, rows) }, cols, rows,
		Meta{Kind: proto.TermSourceApp, Name: found.Title, Detail: found.Detail, Ephemeral: true, Mirror: true})
	if err != nil {
		return nil, Source{}, err
	}
	src := *found
	src.Attached++
	return st, src, nil
}

type mirrorMsg struct {
	Text  string `json:"text"`
	Rows  int    `json:"rows"`
	Cols  int    `json:"cols"`
	Title string `json:"title"`
	Front bool   `json:"front"`
}

// mirrorProc shows a Terminal.app tab or iTerm2 session: its text, polled
// by a JXA loop, drawn with EncodeScreen; typing goes to iTerm2 as text and
// to Terminal as key events.
type mirrorProc struct {
	kind, key string
	cmd       *exec.Cmd
	pr        *io.PipeReader
	pw        *io.PipeWriter
	wmu       sync.Mutex // one input at a time, in order
	pmu       sync.Mutex // one picture at a time, in order

	writer   *exec.Cmd      // iTerm2: the typing script
	writerIn io.WriteCloser // its stdin
	pid      int            // Terminal: its process id

	mu         sync.Mutex
	cols, rows int
	last       *Screen // the tab, unclipped
	prev       *Screen // what the viewer shows
	front      bool
	done       chan struct{}
}

func startMirror(kind, key string, cols, rows int) (Proc, error) {
	cmd := exec.Command(osascript, "-l", "JavaScript", "-e", mirrorScript, kind, key)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	m := &mirrorProc{kind: kind, key: key, cmd: cmd, pr: pr, pw: pw, cols: cols, rows: rows, done: make(chan struct{})}
	// Terminal's tab is brought forward on the first key typed (Write), not
	// here: looking at a tab from the browser leaves the Mac's windows alone.
	go m.read(out)
	return m, nil
}

func (m *mirrorProc) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var msg mirrorMsg
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		m.mu.Lock()
		m.last = mirrorScreen(msg.Text, msg.Cols, msg.Rows, msg.Title)
		m.front = msg.Front
		m.mu.Unlock()
		if m.paint() != nil {
			break
		}
	}
	_ = m.cmd.Wait()
	close(m.done)
	_ = m.pw.Close()
}

func (m *mirrorProc) paint() error {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	m.mu.Lock()
	if m.last == nil {
		m.mu.Unlock()
		return nil
	}
	s := m.last.Clip(m.cols, m.rows)
	prev := m.prev
	m.prev = s
	m.mu.Unlock()
	_, err := m.pw.Write(EncodeScreen(prev, s))
	return err
}

func (m *mirrorProc) Read(p []byte) (int, error) { return m.pr.Read(p) }

func (m *mirrorProc) selectTab() {
	_, _ = runScript(3*time.Second, selectScript, m.key)
}

func (m *mirrorProc) Write(p []byte) (int, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if m.kind == appITerm {
		return m.writeITerm(p)
	}
	// Terminal's process id, looked up once (not per key: ps takes ~30 ms).
	if m.pid == 0 || syscall.Kill(m.pid, 0) != nil {
		m.pid = appPID("Terminal")
	}
	pid := m.pid
	if pid == 0 {
		return 0, errors.New("Terminal is not running")
	}
	m.mu.Lock()
	front := m.front
	m.mu.Unlock()
	if !front {
		m.selectTab()
		m.mu.Lock()
		m.front = true
		m.mu.Unlock()
	}
	keys, _ := ParseKeys(p)
	for _, k := range keys {
		for _, e := range macKeys(k) {
			C.rc_post_key(C.int(pid), C.int(e.Code), C.uint(e.Char), C.ulonglong(e.Flags))
		}
	}
	return len(p), nil
}

// writeITerm hands p to the long-running typing script, starting it first.
func (m *mirrorProc) writeITerm(p []byte) (int, error) {
	if m.writer == nil {
		cmd := exec.Command(osascript, "-l", "JavaScript", "-e", itermWriteScript, m.key)
		in, err := cmd.StdinPipe()
		if err != nil {
			return 0, err
		}
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		m.writer, m.writerIn = cmd, in
		go func() { _ = cmd.Wait() }()
	}
	text := strings.ReplaceAll(string(p), "\x00", "")
	if _, err := m.writerIn.Write(append(asciiJSON(text), '\n')); err != nil {
		_ = m.writerIn.Close()
		m.writer, m.writerIn = nil, nil
		return 0, errors.New("iTerm2 did not take the input")
	}
	return len(p), nil
}

func (m *mirrorProc) Resize(cols, rows int) error {
	m.mu.Lock()
	m.cols, m.rows, m.prev = cols, rows, nil
	m.mu.Unlock()
	return m.paint()
}

func (m *mirrorProc) Wait() int {
	<-m.done
	return 0
}

func (m *mirrorProc) Hangup() {
	if m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
	}
	m.wmu.Lock()
	if m.writerIn != nil {
		_ = m.writerIn.Close() // the typing script ends with its stdin
		m.writer, m.writerIn = nil, nil
	}
	m.wmu.Unlock()
}

func (m *mirrorProc) Close() error { return nil }

//go:build windows

package terminal

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	user32                         = windows.NewLazySystemDLL("user32.dll")
	procCreatePseudoConsole        = kernel32.NewProc("CreatePseudoConsole")
	procAttachConsole              = kernel32.NewProc("AttachConsole")
	procFreeConsole                = kernel32.NewProc("FreeConsole")
	procReadConsoleOutputW         = kernel32.NewProc("ReadConsoleOutputW")
	procWriteConsoleInputW         = kernel32.NewProc("WriteConsoleInputW")
	procSetConsoleCtrlHandler      = kernel32.NewProc("SetConsoleCtrlHandler")
	procGetConsoleTitleW           = kernel32.NewProc("GetConsoleTitleW")
	procSetConsoleTitleW           = kernel32.NewProc("SetConsoleTitleW")
	procGetConsoleCursorInfo       = kernel32.NewProc("GetConsoleCursorInfo")
	procSetConsoleScreenBufferSize = kernel32.NewProc("SetConsoleScreenBufferSize")
	procSetConsoleWindowInfo       = kernel32.NewProc("SetConsoleWindowInfo")
	procVkKeyScanW                 = user32.NewProc("VkKeyScanW")
	procMapVirtualKeyW             = user32.NewProc("MapVirtualKeyW")
)

// HasConPTY reports whether Windows has pseudo consoles (10 1809 and later).
func HasConPTY() bool { return procCreatePseudoConsole.Find() == nil }

// RunTermHost is `remote-agent term-host`, started by the session helper
// with the identity the terminal runs as. Its stdin carries frames
// (input, resize), its stdout the terminal's output; it exits with the
// program's exit code.
//
//	term-host conpty  -cols C -rows R -dir D -- program args…   a new shell on a pseudo console
//	term-host console -cols C -rows R -dir D -- program args…   a new shell on a hidden console (Windows 7)
//	term-host attach  -cols C -rows R -pid P                    an open console window
func RunTermHost(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "term-host: mode required")
		return 2
	}
	fs := flag.NewFlagSet("term-host", flag.ContinueOnError)
	cols := fs.Int("cols", 80, "")
	rows := fs.Int("rows", 24, "")
	dir := fs.String("dir", "", "")
	pid := fs.Int("pid", 0, "")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	argv := fs.Args()
	var err error
	code := 0
	switch args[0] {
	case "conpty":
		code, err = runConPTY(*cols, *rows, *dir, argv)
	case "console":
		code, err = runConsole(0, *cols, *rows, *dir, argv)
	case "attach":
		code, err = runConsole(uint32(*pid), *cols, *rows, "", nil)
	default:
		err = errors.New("unknown mode " + args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "term-host:", err)
		return 125
	}
	return code
}

func coord(cols, rows int) windows.Coord { return windows.Coord{X: int16(cols), Y: int16(rows)} }

// envBlock is env as a CreateProcess environment block.
func envBlock(env []string) *uint16 {
	var b []uint16
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 {
			continue
		}
		b = append(b, utf16.Encode([]rune(kv))...)
		b = append(b, 0)
	}
	b = append(b, 0)
	return &b[0]
}

// killJob is a job that kills what is left of the terminal's programs
// when this process exits. Everything this process starts after joining
// it is in it, conhost included.
func killJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
		LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// startProcess starts argv suspended in dir, in job when there is one,
// and resumes it.
func startProcess(argv []string, dir string, si *windows.StartupInfo, inherit bool, flags uint32, job windows.Handle) (windows.Handle, error) {
	if len(argv) == 0 {
		return 0, errors.New("no program")
	}
	app, err := windows.UTF16PtrFromString(argv[0])
	if err != nil {
		return 0, err
	}
	cmd, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return 0, err
	}
	var dir16 *uint16
	if dir != "" {
		if dir16, err = windows.UTF16PtrFromString(dir); err != nil {
			return 0, err
		}
	}
	var pi windows.ProcessInformation
	flags |= windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED
	if err := windows.CreateProcess(app, cmd, nil, nil, inherit, flags, envBlock(os.Environ()), dir16, si, &pi); err != nil {
		return 0, fmt.Errorf("start %s: %w", argv[0], err)
	}
	if job != 0 {
		_ = windows.AssignProcessToJobObject(job, pi.Process) // already in it on Windows 8 and later
	}
	_, _ = windows.ResumeThread(pi.Thread)
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

func exitCode(h windows.Handle) int {
	var c uint32
	if windows.GetExitCodeProcess(h, &c) != nil {
		return -1
	}
	return int(int32(c))
}

// ---------------------------------------------------------------- ConPTY

type pseudoConsole struct {
	in   *os.File
	hpc  windows.Handle
	once sync.Once
}

func (c *pseudoConsole) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *pseudoConsole) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(c.hpc, coord(cols, rows))
}

func (c *pseudoConsole) close() { c.once.Do(func() { windows.ClosePseudoConsole(c.hpc) }) }

func runConPTY(cols, rows int, dir string, argv []string) (int, error) {
	if !HasConPTY() {
		return 0, errors.New("no pseudo consoles on this Windows")
	}
	job, err := killJob()
	if err == nil {
		_ = windows.AssignProcessToJobObject(job, windows.CurrentProcess())
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(coord(cols, rows), windows.Handle(inR.Fd()), windows.Handle(outW.Fd()), 0, &hpc); err != nil {
		return 0, fmt.Errorf("CreatePseudoConsole: %w", err)
	}
	inR.Close()
	outW.Close()
	pc := &pseudoConsole{in: inW, hpc: hpc}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return 0, err
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES // and no handles: none of ours reach the shell
	proc, err := startProcess(argv, dir, &si.StartupInfo, false, windows.EXTENDED_STARTUPINFO_PRESENT, job)
	if err != nil {
		pc.close()
		return 0, err
	}

	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, outR)
		close(copied)
	}()
	exited := make(chan struct{})
	go func() {
		_, _ = windows.WaitForSingleObject(proc, windows.INFINITE)
		close(exited)
	}()
	go func() {
		_ = ServeFrames(os.Stdin, pc)
		// The agent hung up: close the console like a closed window does,
		// then the job kills whatever is left.
		pc.close()
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
		os.Exit(exitCode(proc))
	}()
	<-exited
	pc.close() // the output pipe ends only once the console is closed
	select {
	case <-copied:
	case <-time.After(time.Second):
	}
	return exitCode(proc), nil
}

// ---------------------------------------------------------------- console bridge

type charInfo struct {
	Char uint16
	Attr uint16
}

type keyEventRecord struct {
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	UnicodeChar     uint16
	ControlKeyState uint32
}

type inputRecord struct {
	EventType uint16
	_         uint16
	Key       keyEventRecord
}

type cursorInfo struct {
	Size    uint32
	Visible int32
}

const (
	keyEvent        = 0x0001
	leftAltPressed  = 0x0002
	leftCtrlPressed = 0x0008
	shiftPressed    = 0x0010
	enhancedKey     = 0x0100
	enableProcessed = 0x0001 // ENABLE_PROCESSED_INPUT
	lvbTrailingByte = 0x0200
	readChunkCells  = 12000 // ReadConsoleOutputW is limited to about 64 KB
	pollFast        = 30 * time.Millisecond
	pollSlow        = 250 * time.Millisecond
	ctrlCEvent      = 0
)

// bridge mirrors a console's screen as terminal output and types input
// into it as key presses.
type bridge struct {
	in, out windows.Handle
	own     bool // the console is ours (a new shell): resizes resize it

	mu         sync.Mutex
	cols, rows int // the viewer's size
	rest       []byte
	prev       *Screen
	kick       chan struct{}
}

func openConsole(name string) (windows.Handle, error) {
	n, _ := windows.UTF16PtrFromString(name)
	sa := windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(sa))
	return windows.CreateFile(n, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, 0, 0)
}

// runConsole attaches to the console of process pid, or (pid 0) starts
// argv on this process's own hidden console.
func runConsole(pid uint32, cols, rows int, dir string, argv []string) (int, error) {
	// Ctrl+C typed into the console reaches every process on it, this one too.
	_, _, _ = procSetConsoleCtrlHandler.Call(0, 1)
	var target windows.Handle
	var err error
	if pid != 0 {
		target, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			return 0, fmt.Errorf("open process %d: %w", pid, err)
		}
		_, _, _ = procFreeConsole.Call()
		if r, _, e := procAttachConsole.Call(uintptr(pid)); r == 0 {
			return 0, fmt.Errorf("AttachConsole(%d): %w", pid, e)
		}
	}
	in, err := openConsole("CONIN$")
	if err != nil {
		return 0, fmt.Errorf("CONIN$: %w", err)
	}
	out, err := openConsole("CONOUT$")
	if err != nil {
		return 0, fmt.Errorf("CONOUT$: %w", err)
	}
	b := &bridge{in: in, out: out, own: pid == 0, cols: cols, rows: rows, kick: make(chan struct{}, 1)}
	if !b.own {
		// The window's title tells the person at the device it is shared.
		setConsoleTitle(remoteTitle + consoleTitle())
	}
	if b.own {
		_ = b.Resize(cols, rows)
		for _, f := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
			_ = windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT, 0)
		}
		job, _ := killJob()
		si := windows.StartupInfo{Flags: windows.STARTF_USESTDHANDLES, StdInput: in, StdOutput: out, StdErr: out}
		si.Cb = uint32(unsafe.Sizeof(si))
		if target, err = startProcess(argv, dir, &si, true, 0, job); err != nil {
			return 0, err
		}
	}
	go func() {
		_ = ServeFrames(os.Stdin, b)
		if b.own { // hung up: end the shell (the job kills it with this process)
			_ = windows.TerminateProcess(target, 1)
		} else if t := consoleTitle(); strings.HasPrefix(t, remoteTitle) {
			setConsoleTitle(strings.TrimPrefix(t, remoteTitle))
		}
		os.Exit(0) // detached from a console window: it carries on
	}()
	wait := pollFast
	for {
		if ev, _ := windows.WaitForSingleObject(target, 0); ev == windows.WAIT_OBJECT_0 {
			b.paint()
			return exitCode(target), nil
		}
		changed, err := b.paint()
		if err != nil {
			return 0, err
		}
		if changed {
			wait = pollFast
		} else {
			wait = min(wait*3/2, pollSlow)
		}
		select {
		case <-b.kick:
			wait = pollFast
		case <-time.After(wait):
		}
	}
}

// snapshot reads the console's visible window.
func (b *bridge) snapshot() (*Screen, error) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(b.out, &info); err != nil {
		return nil, err
	}
	w := info.Window
	cols, rows := int(w.Right-w.Left)+1, int(w.Bottom-w.Top)+1
	if cols < 1 || rows < 1 {
		return nil, errors.New("empty console window")
	}
	s := NewScreen(cols, rows)
	per := max(1, readChunkCells/cols)
	buf := make([]charInfo, cols*per)
	for top := 0; top < rows; top += per {
		n := min(per, rows-top)
		region := windows.SmallRect{Left: w.Left, Top: w.Top + int16(top), Right: w.Right, Bottom: w.Top + int16(top+n-1)}
		r, _, e := procReadConsoleOutputW.Call(uintptr(b.out), uintptr(unsafe.Pointer(&buf[0])),
			uintptr(*(*uint32)(unsafe.Pointer(&windows.Coord{X: int16(cols), Y: int16(n)}))), 0, uintptr(unsafe.Pointer(&region)))
		if r == 0 {
			return nil, fmt.Errorf("ReadConsoleOutputW: %w", e)
		}
		for i := 0; i < cols*n; i++ {
			c := buf[i]
			cell := Cell{Ch: rune(c.Char), A: ConsoleAttr(c.Attr)}
			switch {
			case c.Attr&lvbTrailingByte != 0:
				cell.Ch = 0
			case utf16.IsSurrogate(cell.Ch):
				cell.Ch = ' '
			case cell.Ch == 0:
				cell.Ch = ' '
			}
			s.Cells[top*cols+i] = cell
		}
	}
	s.CurX, s.CurY = int(info.CursorPosition.X-w.Left), int(info.CursorPosition.Y-w.Top)
	var ci cursorInfo
	if r, _, _ := procGetConsoleCursorInfo.Call(uintptr(b.out), uintptr(unsafe.Pointer(&ci))); r != 0 {
		s.Cursor = ci.Visible != 0
	}
	if s.CurX < 0 || s.CurX >= cols || s.CurY < 0 || s.CurY >= rows {
		s.CurX, s.CurY, s.Cursor = max(0, min(s.CurX, cols-1)), max(0, min(s.CurY, rows-1)), false
	}
	s.Title = strings.TrimPrefix(consoleTitle(), remoteTitle)
	return s, nil
}

// remoteTitle marks a console window a browser has joined.
const remoteTitle = "[Remote] "

func consoleTitle() string {
	title := make([]uint16, 1024)
	n, _, _ := procGetConsoleTitleW.Call(uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
	return windows.UTF16ToString(title[:n])
}

func setConsoleTitle(t string) {
	if p, err := windows.UTF16PtrFromString(t); err == nil {
		_, _, _ = procSetConsoleTitleW.Call(uintptr(unsafe.Pointer(p)))
	}
}

// paint sends what changed since the last picture.
func (b *bridge) paint() (bool, error) {
	s, err := b.snapshot()
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	s = s.Clip(b.cols, b.rows)
	prev := b.prev
	b.prev = s
	b.mu.Unlock()
	if prev != nil && screensEqual(prev, s) {
		return false, nil
	}
	_, err = os.Stdout.Write(EncodeScreen(prev, s))
	return true, err
}

func screensEqual(a, b *Screen) bool {
	return a.Cols == b.Cols && a.Rows == b.Rows && a.CurX == b.CurX && a.CurY == b.CurY && a.Cursor == b.Cursor &&
		a.Title == b.Title && rowsEqual(a.Cells, b.Cells)
}

func (b *bridge) Resize(cols, rows int) error {
	b.mu.Lock()
	b.cols, b.rows, b.prev = cols, rows, nil
	b.mu.Unlock()
	if b.own {
		// Shrink the window before the buffer, grow the buffer before the window.
		win := windows.SmallRect{Right: int16(cols - 1), Bottom: int16(rows - 1)}
		_, _, _ = procSetConsoleWindowInfo.Call(uintptr(b.out), 1, uintptr(unsafe.Pointer(&win)))
		_, _, _ = procSetConsoleScreenBufferSize.Call(uintptr(b.out), uintptr(*(*uint32)(unsafe.Pointer(&windows.Coord{X: int16(cols), Y: int16(rows)}))))
		_, _, _ = procSetConsoleWindowInfo.Call(uintptr(b.out), 1, uintptr(unsafe.Pointer(&win)))
	}
	b.poke()
	return nil
}

func (b *bridge) poke() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

func (b *bridge) Write(p []byte) (int, error) {
	b.mu.Lock()
	keys, rest := ParseKeys(append(b.rest, p...))
	b.rest = append([]byte(nil), rest...)
	b.mu.Unlock()
	var recs []inputRecord
	flush := func() error {
		if len(recs) == 0 {
			return nil
		}
		var n uint32
		r, _, e := procWriteConsoleInputW.Call(uintptr(b.in), uintptr(unsafe.Pointer(&recs[0])), uintptr(len(recs)), uintptr(unsafe.Pointer(&n)))
		recs = recs[:0]
		if r == 0 {
			return e
		}
		return nil
	}
	for _, k := range keys {
		if k.Ctrl && k.Char == 3 && b.processedInput() {
			if err := flush(); err != nil {
				return 0, err
			}
			_ = windows.GenerateConsoleCtrlEvent(ctrlCEvent, 0)
			continue
		}
		recs = append(recs, keyRecords(k)...)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	b.poke()
	return len(p), nil
}

func (b *bridge) processedInput() bool {
	var mode uint32
	return windows.GetConsoleMode(b.in, &mode) == nil && mode&enableProcessed != 0
}

// keyRecords is a key's press and release.
func keyRecords(k Key) []inputRecord {
	vk := k.VK
	shift := k.Shift
	if vk == 0 && k.Char != 0 && k.Char < 0x10000 {
		if r, _, _ := procVkKeyScanW.Call(uintptr(k.Char)); int16(r) != -1 {
			vk = uint16(r & 0xff)
			shift = shift || r&0x100 != 0
		}
	}
	var state uint32
	if k.Ctrl {
		state |= leftCtrlPressed
	}
	if k.Alt {
		state |= leftAltPressed
	}
	if shift {
		state |= shiftPressed
	}
	switch vk {
	case VKPrior, VKNext, VKEnd, VKHome, VKLeft, VKUp, VKRight, VKDown, VKInsert, VKDelete:
		state |= enhancedKey
	}
	scan, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0)
	units := []uint16{0}
	if k.Char != 0 {
		units = utf16.Encode([]rune{k.Char})
	}
	var out []inputRecord
	for _, u := range units {
		ev := keyEventRecord{KeyDown: 1, RepeatCount: 1, VirtualKeyCode: vk, VirtualScanCode: uint16(scan), UnicodeChar: u, ControlKeyState: state}
		out = append(out, inputRecord{EventType: keyEvent, Key: ev})
		ev.KeyDown = 0
		out = append(out, inputRecord{EventType: keyEvent, Key: ev})
	}
	return out
}

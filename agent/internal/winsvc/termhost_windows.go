package winsvc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ruolez/remotectl/agent/internal/terminal"
	"github.com/ruolez/remotectl/proto"
)

// TermHost is the terminal.Host of the Windows helper. The helper runs as
// SYSTEM; every terminal runs in a `remote-agent term-host` child with the
// identity it belongs to: the user signed in to the session, their
// elevated token for an administrator shell, SYSTEM when nobody is signed
// in, or the owner of a console window being taken over.
type TermHost struct {
	SessionID uint32
	Exe       string
	Log       *slog.Logger
	shells    *terminal.Shells
}

var _ terminal.Host = (*TermHost)(nil)

func NewTermHost(sid uint32, exe string, log *slog.Logger) *TermHost {
	return &TermHost{SessionID: sid, Exe: exe, Log: log, shells: terminal.NewShells(nil, log)}
}

// Close ends the terminals (the helper is stopping).
func (t *TermHost) Close() { t.shells.Close() }

// runAs is who a terminal runs as; tok 0 is this process (SYSTEM).
type runAs struct {
	tok   windows.Token
	admin bool
	user  bool // a signed-in user (not SYSTEM)
}

func (id runAs) close() {
	if id.tok != 0 {
		id.tok.Close()
	}
}

func (t *TermHost) runAs(admin bool) (runAs, error) {
	var tok windows.Token
	if err := windows.WTSQueryUserToken(t.SessionID, &tok); err != nil {
		if errors.Is(err, windows.ERROR_NO_TOKEN) {
			return runAs{admin: true}, nil // nobody signed in: SYSTEM
		}
		return runAs{}, fmt.Errorf("WTSQueryUserToken: %w", err)
	}
	if tok.IsElevated() || !admin {
		return runAs{tok: tok, admin: tok.IsElevated(), user: true}, nil
	}
	linked, err := tok.GetLinkedToken()
	tok.Close()
	if err != nil {
		return runAs{}, terminal.ErrUnsupported
	}
	if !linked.IsElevated() {
		linked.Close()
		return runAs{}, terminal.ErrUnsupported // not an administrator
	}
	return runAs{tok: linked, admin: true, user: true}, nil
}

// Admin reports whether an elevated shell can start: the user is an
// administrator, or nobody is signed in (shells run as SYSTEM).
func (t *TermHost) Admin() bool {
	id, err := t.runAs(true)
	if err != nil {
		return false
	}
	id.close()
	return id.admin
}

func (t *TermHost) userSignedIn() bool {
	var tok windows.Token
	if windows.WTSQueryUserToken(t.SessionID, &tok) != nil {
		return false
	}
	tok.Close()
	return true
}

func (t *TermHost) shellChoices() []winShell {
	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	progFiles := os.Getenv("ProgramFiles")
	if progFiles == "" {
		progFiles = `C:\Program Files`
	}
	pwsh, _ := exec.LookPath("pwsh.exe")
	return winShellChoices(sysRoot, progFiles, pwsh, func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}, t.userSignedIn())
}

func (t *TermHost) Shells() []terminal.Shell {
	var out []terminal.Shell
	for _, s := range t.shellChoices() {
		out = append(out, s.Shell)
	}
	return out
}

func (t *TermHost) List() ([]terminal.Source, error) {
	out := t.shells.List()
	for _, p := range t.consoles() {
		out = append(out, consoleSource(p, processElevated(p.PID)))
	}
	return out, nil
}

func (t *TermHost) New(shell string, admin bool, cols, rows int) (terminal.Stream, terminal.Source, error) {
	choices := t.shellChoices()
	if len(choices) == 0 {
		return nil, terminal.Source{}, terminal.ErrUnsupported
	}
	sh := choices[0]
	if shell != "" {
		found := false
		for _, c := range choices {
			if c.ID == shell {
				sh, found = c, true
			}
		}
		if !found {
			return nil, terminal.Source{}, terminal.ErrNotFound
		}
	}
	id, err := t.runAs(admin)
	if err != nil {
		return nil, terminal.Source{}, err
	}
	defer id.close()
	dir := os.Getenv("SystemRoot") + `\System32`
	who := "SYSTEM"
	if id.user {
		if d, err := id.tok.GetUserProfileDirectory(); err == nil {
			dir = d
		}
		who = tokenUser(id.tok)
	}
	if id.admin && id.user {
		who += " (administrator)"
	}
	mode := "conpty"
	if !terminal.HasConPTY() {
		mode = "console" // Windows 7 and Windows 10 before 1809
	}
	args := append([]string{"term-host", mode, "-cols", strconv.Itoa(cols), "-rows", strconv.Itoa(rows), "-dir", dir, "--", sh.Path}, sh.Args...)
	spawn := func() (terminal.Proc, error) { return t.start(id, args, mode == "console") }
	return t.shells.Run(spawn, cols, rows, terminal.Meta{Kind: proto.TermSourceShell, Name: sh.Name, Detail: who,
		Admin: id.admin, Local: id.user && !id.admin})
}

func (t *TermHost) Attach(id string, cols, rows int) (terminal.Stream, terminal.Source, error) {
	kind, rest, _ := strings.Cut(id, ":")
	switch kind {
	case proto.TermSourceShell:
		return t.shells.Attach(id, cols, rows)
	case proto.TermSourceConsole:
	default:
		return nil, terminal.Source{}, terminal.ErrNotFound
	}
	pid, err := strconv.ParseUint(rest, 10, 32)
	if err != nil {
		return nil, terminal.Source{}, terminal.ErrNotFound
	}
	var proc *procInfo
	for _, p := range t.consoles() {
		if p.PID == uint32(pid) {
			proc = &p
		}
	}
	if proc == nil {
		return nil, terminal.Source{}, terminal.ErrNotFound
	}
	owner, err := processToken(proc.PID)
	if err != nil {
		return nil, terminal.Source{}, fmt.Errorf("console owner: %w", err)
	}
	defer owner.Close()
	src := consoleSource(*proc, owner.IsElevated())
	ident := runAs{tok: owner, admin: owner.IsElevated(), user: true}
	args := []string{"term-host", "attach", "-cols", strconv.Itoa(cols), "-rows", strconv.Itoa(rows), "-pid", rest}
	st, _, err := t.shells.Run(func() (terminal.Proc, error) { return t.start(ident, args, false) }, cols, rows,
		terminal.Meta{Kind: proto.TermSourceConsole, Name: src.Title, Detail: src.Detail, Ephemeral: true, Admin: src.Admin, Mirror: true})
	if err != nil {
		return nil, terminal.Source{}, err
	}
	src.Attached++
	return st, src, nil
}

// start runs a term-host child as id. newConsole gives it a hidden console
// of its own (the shell runs on it).
func (t *TermHost) start(id runAs, args []string, newConsole bool) (terminal.Proc, error) {
	cmd := exec.Command(t.Exe, args...)
	attr := &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if newConsole {
		attr.CreationFlags = windows.CREATE_NEW_CONSOLE
	}
	env := os.Environ()
	if id.tok != 0 {
		attr.Token = syscall.Token(id.tok)
		if e, err := id.tok.Environ(false); err == nil {
			env = e
		}
	}
	cmd.SysProcAttr = attr
	cmd.Env = append(env, "TERM_PROGRAM=remotectl", "COLORTERM=truecolor")
	cmd.Stderr = logWriter(t.Log)
	return terminal.StartChild(cmd, 3*time.Second)
}

// logWriter logs each line a child writes to stderr.
func logWriter(log *slog.Logger) io.Writer {
	r, w := io.Pipe()
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			log.Warn("terminal: " + sc.Text())
		}
	}()
	return w
}

// consoles are the console windows in this session that can be taken over.
func (t *TermHost) consoles() []procInfo {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var procs []procInfo
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		var sid uint32
		if windows.ProcessIdToSessionId(e.ProcessID, &sid) != nil || sid != t.SessionID {
			continue
		}
		procs = append(procs, procInfo{PID: e.ProcessID, PPID: e.ParentProcessID, Exe: windows.UTF16ToString(e.ExeFile[:])})
	}
	return consoleCandidates(procs, filepath.Base(t.Exe))
}

// processToken is a primary token of process pid's identity.
func processToken(pid uint32) (windows.Token, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var tok windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY, &tok); err != nil {
		return 0, err
	}
	defer tok.Close()
	var dup windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.MAXIMUM_ALLOWED, nil, windows.SecurityImpersonation,
		windows.TokenPrimary, &dup); err != nil {
		return 0, err
	}
	return dup, nil
}

func processElevated(pid uint32) bool {
	tok, err := processToken(pid)
	if err != nil {
		return false
	}
	defer tok.Close()
	return tok.IsElevated()
}

func tokenUser(tok windows.Token) string {
	u, err := tok.GetTokenUser()
	if err != nil {
		return "user"
	}
	acct, domain, _, err := u.User.Sid.LookupAccount("")
	if err != nil {
		return "user"
	}
	if domain != "" {
		return domain + `\` + acct
	}
	return acct
}

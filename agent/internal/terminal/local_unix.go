//go:build !windows

package terminal

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// extraPath is searched for tmux and screen after PATH: a LaunchAgent's PATH
// is only /usr/bin:/bin:/usr/sbin:/sbin and Homebrew installs elsewhere.
var extraPath = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin", "/usr/bin", "/bin"}

// Local is the Host of an agent that runs as the device's user (macOS,
// Linux): shells on pseudo terminals, plus tmux and screen sessions.
type Local struct {
	shells *Shells
	log    *slog.Logger
}

func NewLocal(log *slog.Logger) *Local {
	return &Local{shells: NewShells(spawnPTY, log), log: log}
}

// Default is the terminal host of an agent running as the device's user,
// and the func that hangs up its shells when the agent stops.
func Default(log *slog.Logger) (Host, func()) {
	l := NewLocal(log)
	return l, l.Close
}

var (
	_ Host       = (*Local)(nil)
	_ SharedHost = (*Local)(nil)
	_ Announcer  = (*Local)(nil)
)

func (l *Local) NewShared(cols, rows int) (Stream, Source, error) {
	return l.New("", false, cols, rows)
}

func (l *Local) JoinShared(id string, cols, rows int) (Stream, Source, error) {
	return l.shells.AttachShared(id, cols, rows)
}

func (l *Local) SharedList() []Source { return l.shells.ListShared() }

// Announce shows msg on the status line of a tmux or screen session.
func (l *Local) Announce(src Source, msg string) {
	kind, name, _ := strings.Cut(src.ID, ":")
	if !validSessionName(name) {
		return
	}
	env := shellEnv()
	switch kind {
	case proto.TermSourceTmux:
		if tmux := lookTool("tmux"); tmux != "" {
			m := strings.ReplaceAll(msg, "#", "##") // tmux expands #{…}
			if _, err := runTool(env, tmux, "display-message", "-t", "="+name+":", "-d", "8000", m); err != nil {
				_, _ = runTool(env, tmux, "display-message", "-t", "="+name+":", m) // tmux before 3.2
			}
		}
	case proto.TermSourceScreen:
		if screen := lookTool("screen"); screen != "" {
			_, _ = runTool(env, screen, "-S", name, "-X", "echo", msg)
		}
	}
}

// Close hangs up the kept shells.
func (l *Local) Close() { l.shells.Close() }

func (l *Local) Admin() bool { return false }

func (l *Local) Shells() []Shell {
	return shellChoices(loginShell(), listedShells())
}

func (l *Local) List() ([]Source, error) {
	out := l.shells.List()
	env := shellEnv()
	if tmux := lookTool("tmux"); tmux != "" {
		if s, err := runTool(env, tmux, "list-sessions", "-F", tmuxListFormat); err == nil {
			out = append(out, parseTmuxList(s)...)
		}
	}
	if screen := lookTool("screen"); screen != "" {
		s, _ := runTool(env, screen, "-ls") // exits 1 when sessions exist on some builds
		out = append(out, parseScreenList(s)...)
	}
	out = append(out, appSources()...)
	sortSources(out)
	return out, nil
}

func (l *Local) New(sh string, admin bool, cols, rows int) (Stream, Source, error) {
	if admin {
		return nil, Source{}, ErrUnsupported
	}
	path := loginShell()
	if sh != "" {
		if !l.allowedShell(sh) {
			return nil, Source{}, ErrNotFound
		}
		path = sh
	}
	home, _ := os.UserHomeDir()
	name := filepath.Base(path)
	spec := SpawnSpec{Path: path, Args: []string{"-" + name}, Dir: home, Env: shellEnv(), Cols: cols, Rows: rows}
	return l.shells.Start(spec, Meta{Kind: proto.TermSourceShell, Name: name, Detail: path, Local: true})
}

func (l *Local) allowedShell(p string) bool {
	for _, s := range l.Shells() {
		if s.ID == p {
			return true
		}
	}
	return false
}

func (l *Local) Attach(id string, cols, rows int) (Stream, Source, error) {
	kind, name, _ := strings.Cut(id, ":")
	var tool string
	var args []string
	switch kind {
	case "shell":
		return l.shells.Attach(id, cols, rows)
	case proto.TermSourceTmux:
		tool = lookTool("tmux")
		// "=" makes tmux match the name exactly, not as a prefix.
		args = []string{"tmux", "attach-session", "-t", "=" + name}
	case proto.TermSourceScreen:
		tool = lookTool("screen")
		// -x joins without detaching the people already attached.
		args = []string{"screen", "-x", name}
	case proto.TermSourceApp:
		return l.attachApp(id, cols, rows)
	default:
		return nil, Source{}, ErrNotFound
	}
	if tool == "" || !validSessionName(name) {
		return nil, Source{}, ErrNotFound
	}
	srcs, _ := l.List()
	var found *Source
	for i := range srcs {
		if srcs[i].ID == id {
			found = &srcs[i]
		}
	}
	if found == nil {
		return nil, Source{}, ErrNotFound
	}
	home, _ := os.UserHomeDir()
	st, _, err := l.shells.Start(SpawnSpec{Path: tool, Args: args, Dir: home, Env: shellEnv(), Cols: cols, Rows: rows},
		Meta{Kind: kind, Name: found.Title, Detail: found.Detail, Ephemeral: true, Cwd: l.sessionCwd(kind, name)})
	if err != nil {
		return nil, Source{}, err
	}
	src := *found
	src.Attached++
	return st, src, nil
}

// sessionCwd reports the directory of a tmux session's active pane; nil
// for screen, which does not say.
func (l *Local) sessionCwd(kind, name string) func() (string, error) {
	if kind != proto.TermSourceTmux {
		return nil
	}
	return func() (string, error) {
		tmux := lookTool("tmux")
		if tmux == "" {
			return "", ErrUnsupported
		}
		out, err := runTool(shellEnv(), tmux, "display-message", "-p", "-t", "="+name+":", "#{pane_current_path}")
		return strings.TrimSpace(out), err
	}
}

// loginShell is the user's login shell from the account database.
func loginShell() string {
	if u, err := user.Current(); err == nil {
		if s := accountShell(u.Username, u.Uid); s != "" {
			return s
		}
	}
	if s := os.Getenv("SHELL"); s != "" && isExec(s) {
		return s
	}
	for _, s := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if isExec(s) {
			return s
		}
	}
	return "/bin/sh"
}

func accountShell(name, uid string) string {
	if runtime.GOOS == "darwin" {
		out, err := runTool(nil, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UserShell")
		if err != nil {
			return ""
		}
		_, s, _ := strings.Cut(strings.TrimSpace(out), "UserShell:")
		if s = strings.TrimSpace(s); isExec(s) {
			return s
		}
		return ""
	}
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), ":")
		if len(p) == 7 && (p[0] == name || p[2] == uid) && isExec(p[6]) {
			return p[6]
		}
	}
	return ""
}

// listedShells are the shells in /etc/shells that exist.
func listedShells() []string {
	b, err := os.ReadFile("/etc/shells")
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "/") && isExec(l) {
			out = append(out, l)
		}
	}
	return out
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

func lookTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range extraPath {
		if p := filepath.Join(d, name); isExec(p) {
			return p
		}
	}
	return ""
}

func runTool(env []string, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	return string(out), err
}

// shellEnv is the environment of programs started on a terminal: the
// agent's own, without its terminal and multiplexer variables, plus a
// 256-color UTF-8 terminal and the user's identity.
func shellEnv() []string {
	return buildEnv(os.Environ(), userVars())
}

func userVars() map[string]string {
	v := map[string]string{}
	if u, err := user.Current(); err == nil {
		v["USER"], v["LOGNAME"], v["HOME"] = u.Username, u.Username, u.HomeDir
	}
	v["SHELL"] = loginShell()
	return v
}

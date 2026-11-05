package winsvc

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/ruolez/remotectl/agent/internal/terminal"
	"github.com/ruolez/remotectl/proto"
)

// winShell is a program a new Windows terminal can start.
type winShell struct {
	terminal.Shell
	Args []string
}

// winShellChoices lists the shells a new terminal can start, the default
// first: PowerShell 7 when installed, Windows PowerShell, Command Prompt,
// and WSL for a signed-in user. exists reports whether a file exists; pwsh
// is where PATH finds pwsh.exe ("" if nowhere).
func winShellChoices(sysRoot, progFiles, pwsh string, exists func(string) bool, user bool) []winShell {
	sys32 := sysRoot + `\System32`
	if pwsh == "" && exists(progFiles+`\PowerShell\7\pwsh.exe`) {
		pwsh = progFiles + `\PowerShell\7\pwsh.exe`
	}
	var out []winShell
	add := func(id, name, p string, args ...string) {
		if p != "" && exists(p) {
			out = append(out, winShell{Shell: terminal.Shell{ID: id, Name: name, Path: p}, Args: args})
		}
	}
	add("pwsh", "PowerShell 7", pwsh, "-NoLogo")
	add("powershell", "Windows PowerShell", sys32+`\WindowsPowerShell\v1.0\powershell.exe`, "-NoLogo")
	add("cmd", "Command Prompt", sys32+`\cmd.exe`)
	if user {
		add("wsl", "WSL", sys32+`\wsl.exe`, "~")
	}
	return out
}

// procInfo is a process from a process snapshot.
type procInfo struct {
	PID, PPID uint32
	Exe       string // file name, e.g. "cmd.exe"
}

// consoleShells names the console programs a console window is listed by.
var consoleShells = map[string]string{
	"cmd.exe": "Command Prompt", "powershell.exe": "Windows PowerShell", "pwsh.exe": "PowerShell 7",
	"wsl.exe": "WSL", "bash.exe": "Bash", "ssh.exe": "SSH",
}

// consoleCandidates are the shells to offer for takeover: console shells
// whose parent is not one too (a shell inside another shares its window)
// and not this agent (terminals it runs itself are listed as such).
func consoleCandidates(procs []procInfo, agentExe string) []procInfo {
	byPID := map[uint32]procInfo{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	var out []procInfo
	for _, p := range procs {
		if _, ok := consoleShells[strings.ToLower(p.Exe)]; !ok {
			continue
		}
		if parent, ok := byPID[p.PPID]; ok {
			pe := strings.ToLower(parent.Exe)
			if _, shell := consoleShells[pe]; shell || pe == strings.ToLower(agentExe) {
				continue
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// consoleSource describes a console window for the browser.
func consoleSource(p procInfo, admin bool) terminal.Source {
	return terminal.Source{ID: "console:" + strconv.FormatUint(uint64(p.PID), 10), Kind: proto.TermSourceConsole,
		Title: consoleShells[strings.ToLower(p.Exe)], Detail: path.Base(p.Exe) + " · pid " + strconv.FormatUint(uint64(p.PID), 10),
		Admin: admin, Fidelity: "mirror"}
}

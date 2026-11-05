package winsvc

import (
	"reflect"
	"testing"

	"github.com/ruolez/remotectl/agent/internal/terminal"
)

func TestWinShellChoices(t *testing.T) {
	files := map[string]bool{
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: true,
		`C:\Windows\System32\cmd.exe`:                               true,
		`C:\Windows\System32\wsl.exe`:                               true,
		`C:\Program Files\PowerShell\7\pwsh.exe`:                    true,
	}
	exists := func(p string) bool { return files[p] }
	ids := func(ss []winShell) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(winShellChoices(`C:\Windows`, `C:\Program Files`, "", exists, true)); !reflect.DeepEqual(got,
		[]string{"pwsh", "powershell", "cmd", "wsl"}) {
		t.Fatalf("user shells %v", got)
	}
	if got := ids(winShellChoices(`C:\Windows`, `D:\Apps`, "", exists, false)); !reflect.DeepEqual(got,
		[]string{"powershell", "cmd"}) {
		t.Fatalf("SYSTEM shells without pwsh %v", got)
	}
	got := winShellChoices(`C:\Windows`, `C:\Program Files`, "", exists, false)[0]
	want := winShell{Shell: terminal.Shell{ID: "pwsh", Name: "PowerShell 7", Path: `C:\Program Files\PowerShell\7\pwsh.exe`},
		Args: []string{"-NoLogo"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default %+v, want %+v", got, want)
	}
}

func TestConsoleCandidates(t *testing.T) {
	procs := []procInfo{
		{PID: 10, PPID: 1, Exe: "explorer.exe"},
		{PID: 20, PPID: 10, Exe: "cmd.exe"},        // a console window: offered
		{PID: 21, PPID: 20, Exe: "powershell.exe"}, // inside the cmd above: not
		{PID: 30, PPID: 5, Exe: "WindowsTerminal.exe"},
		{PID: 31, PPID: 30, Exe: "pwsh.exe"}, // a Windows Terminal tab: offered
		{PID: 40, PPID: 2, Exe: "remote-agent.exe"},
		{PID: 41, PPID: 40, Exe: "cmd.exe"},         // the agent's own terminal: not
		{PID: 50, PPID: 999, Exe: "PowerShell.EXE"}, // parent gone: offered
		{PID: 60, PPID: 10, Exe: "notepad.exe"},
	}
	var got []uint32
	for _, p := range consoleCandidates(procs, "remote-agent.exe") {
		got = append(got, p.PID)
	}
	if !reflect.DeepEqual(got, []uint32{20, 31, 50}) {
		t.Fatalf("candidates %v", got)
	}
	src := consoleSource(procInfo{PID: 31, Exe: "pwsh.exe"}, true)
	if want := (terminal.Source{ID: "console:31", Kind: "console", Title: "PowerShell 7", Detail: "pwsh.exe · pid 31",
		Admin: true, Fidelity: "mirror"}); src != want {
		t.Fatalf("source %+v", src)
	}
}

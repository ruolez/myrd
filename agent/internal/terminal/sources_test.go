package terminal

import (
	"reflect"
	"testing"
)

func TestParseTmuxList(t *testing.T) {
	out := "main\t3\t1\t1762250000\t/home/dev/src\nscratch\t1\t0\t1762250100\t\n\nbroken line\n"
	want := []Source{
		{ID: "tmux:main", Kind: "tmux", Title: "main", Detail: "3 windows · /home/dev/src", Attached: 1, Fidelity: "full", Created: 1762250000000},
		{ID: "tmux:scratch", Kind: "tmux", Title: "scratch", Detail: "1 window", Attached: 0, Fidelity: "full", Created: 1762250100000},
	}
	if got := parseTmuxList(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseScreenList(t *testing.T) {
	out := "There are screens on:\n" +
		"\t12345.pts-0.host\t(Detached)\n" +
		"\t678.work\t(11/04/2026 10:20:01 AM)\t(Attached)\n" +
		"\tnot.a.pid\t(Detached)\n" +
		"2 Sockets in /run/screen/S-dev.\n"
	want := []Source{
		{ID: "screen:12345.pts-0.host", Kind: "screen", Title: "pts-0.host", Detail: "pid 12345", Fidelity: "full"},
		{ID: "screen:678.work", Kind: "screen", Title: "work", Detail: "pid 678", Attached: 1, Fidelity: "full"},
	}
	if got := parseScreenList(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if got := parseScreenList("No Sockets found in /run/screen/S-dev.\n"); got != nil {
		t.Fatalf("no sockets: got %+v", got)
	}
}

func TestValidSessionName(t *testing.T) {
	for name, want := range map[string]bool{"main": true, "my session": true, "": false, "-t": false, "a\nb": false} {
		if got := validSessionName(name); got != want {
			t.Errorf("validSessionName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSortSources(t *testing.T) {
	ss := []Source{{ID: "tmux:b", Kind: "tmux", Created: 1}, {ID: "shell:2", Kind: "shell", Created: 5},
		{ID: "screen:1.x", Kind: "screen"}, {ID: "shell:1", Kind: "shell", Created: 3}}
	sortSources(ss)
	var ids []string
	for _, s := range ss {
		ids = append(ids, s.ID)
	}
	if want := []string{"shell:1", "shell:2", "tmux:b", "screen:1.x"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("order %v, want %v", ids, want)
	}
}

func TestBuildEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "TERM=dumb", "TMUX=/tmp/x", "RC_CAPTURE=gdi", "HOME=/old", "LANG=de_DE.UTF-8"}
	got := buildEnv(base, map[string]string{"HOME": "/home/u", "USER": "u"})
	want := []string{"PATH=/usr/bin", "HOME=/home/u", "LANG=de_DE.UTF-8", "TERM=xterm-256color", "COLORTERM=truecolor",
		"TERM_PROGRAM=remotectl", "USER=u"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if got := buildEnv(nil, nil); !reflect.DeepEqual(got, []string{"TERM=xterm-256color", "COLORTERM=truecolor",
		"TERM_PROGRAM=remotectl", "LANG=en_US.UTF-8"}) {
		t.Fatalf("empty base: %v", got)
	}
}

func TestShellChoices(t *testing.T) {
	listed := []string{"/bin/sh", "/usr/bin/sh", "/bin/bash", "/usr/bin/bash", "/bin/rbash", "/usr/bin/zsh", "/usr/bin/tmux", "/bin/zsh"}
	got := shellChoices("/bin/bash", listed)
	want := []Shell{{ID: "/bin/bash", Name: "bash", Path: "/bin/bash"}, {ID: "/bin/sh", Name: "sh", Path: "/bin/sh"},
		{ID: "/usr/bin/zsh", Name: "zsh", Path: "/usr/bin/zsh"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

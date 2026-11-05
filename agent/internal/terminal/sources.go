package terminal

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/ruolez/remotectl/proto"
)

var kindOrder = map[string]int{proto.TermSourceShell: 0, proto.TermSourceTmux: 1, proto.TermSourceScreen: 2,
	proto.TermSourceConsole: 3, proto.TermSourceApp: 4}

// sortSources orders by kind, then oldest first, then title.
func sortSources(ss []Source) {
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Created != b.Created {
			return a.Created < b.Created
		}
		return a.Title < b.Title
	})
}

// tmuxListFormat is passed to `tmux list-sessions -F`; parseTmuxList reads it.
const tmuxListFormat = "#{session_name}\t#{session_windows}\t#{session_attached}\t#{session_created}\t#{pane_current_path}"

// parseTmuxList reads `tmux list-sessions -F tmuxListFormat` output.
func parseTmuxList(out string) []Source {
	var ss []Source
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		windows, _ := strconv.Atoi(f[1])
		attached, _ := strconv.Atoi(f[2])
		created, _ := strconv.ParseInt(f[3], 10, 64)
		detail := strconv.Itoa(windows) + " window"
		if windows != 1 {
			detail += "s"
		}
		if len(f) > 4 && f[4] != "" {
			detail += " · " + f[4]
		}
		ss = append(ss, Source{ID: "tmux:" + f[0], Kind: proto.TermSourceTmux, Title: f[0], Detail: detail,
			Attached: attached, Fidelity: "full", Created: created * 1000})
	}
	return ss
}

// parseScreenList reads `screen -ls` output: one session per tab-indented
// line, "<pid>.<name>\t[(date)\t](Attached|Detached|Multi, attached)".
func parseScreenList(out string) []Source {
	var ss []Source
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "\t") {
			continue
		}
		f := strings.Split(strings.TrimSpace(line), "\t")
		id := f[0]
		pid, name, ok := strings.Cut(id, ".")
		if !ok || name == "" {
			continue
		}
		if _, err := strconv.Atoi(pid); err != nil {
			continue
		}
		state := strings.ToLower(f[len(f)-1])
		attached := 0
		if strings.Contains(state, "attached") {
			attached = 1
		}
		ss = append(ss, Source{ID: "screen:" + id, Kind: proto.TermSourceScreen, Title: name, Detail: "pid " + pid,
			Attached: attached, Fidelity: "full"})
	}
	return ss
}

// validSessionName accepts tmux / screen session names we pass back to the
// tool as one argv element.
func validSessionName(s string) bool {
	if s == "" || len(s) > 200 || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// skipShells are listed in /etc/shells but are no use as a terminal.
var skipShells = map[string]bool{"rbash": true, "nologin": true, "false": true, "git-shell": true, "screen": true, "tmux": true}

// shellChoices is the default shell, then the other listed shells, one per
// name (/etc/shells repeats them under /bin and /usr/bin), at most 6.
func shellChoices(def string, listed []string) []Shell {
	out := []Shell{{ID: def, Name: path.Base(def), Path: def}}
	seen := map[string]bool{path.Base(def): true}
	for _, p := range listed {
		name := path.Base(p)
		if seen[name] || skipShells[name] || len(out) >= 6 {
			continue
		}
		seen[name] = true
		out = append(out, Shell{ID: p, Name: name, Path: p})
	}
	return out
}

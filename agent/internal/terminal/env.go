package terminal

import (
	"sort"
	"strings"
)

// dropEnv are variables of the agent's own environment a terminal must not
// inherit: they describe another terminal or multiplexer, or the agent.
var dropEnv = []string{"TERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION", "TERM_SESSION_ID", "COLORTERM",
	"TMUX", "TMUX_PANE", "STY", "WINDOW", "ITERM_SESSION_ID", "LC_TERMINAL", "LC_TERMINAL_VERSION",
	"COLUMNS", "LINES"}

var dropEnvPrefix = []string{"RC_", "REMOTECTL_"}

// buildEnv filters base and sets the terminal variables and set (which wins).
func buildEnv(base []string, set map[string]string) []string {
	vars := map[string]string{}
	var order []string
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || dropped(k) {
			continue
		}
		if _, dup := vars[k]; !dup {
			order = append(order, k)
		}
		vars[k] = v
	}
	put := func(k, v string) {
		if _, ok := vars[k]; !ok {
			order = append(order, k)
		}
		vars[k] = v
	}
	put("TERM", "xterm-256color")
	put("COLORTERM", "truecolor")
	put("TERM_PROGRAM", "remotectl")
	if vars["LANG"] == "" && vars["LC_ALL"] == "" && vars["LC_CTYPE"] == "" {
		put("LANG", "en_US.UTF-8")
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if set[k] != "" {
			put(k, set[k])
		}
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out
}

func dropped(k string) bool {
	for _, d := range dropEnv {
		if k == d {
			return true
		}
	}
	for _, p := range dropEnvPrefix {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

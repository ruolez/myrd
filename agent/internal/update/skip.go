package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

const maxSkip = 50

func skipPath(dataDir string) string { return filepath.Join(dataDir, "updates", "skip.json") }

// LoadSkip returns the versions that failed or rolled back on this device;
// automatic checks do not try them again (an explicit one does).
func LoadSkip(dataDir string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(skipPath(dataDir))
	if err != nil {
		return out
	}
	var vs []string
	_ = json.Unmarshal(b, &vs)
	for _, v := range vs {
		out[v] = true
	}
	return out
}

func AddSkip(dataDir, version string) error {
	set := LoadSkip(dataDir)
	set[version] = true
	vs := make([]string, 0, len(set))
	for v := range set {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	if len(vs) > maxSkip {
		vs = vs[len(vs)-maxSkip:]
	}
	if err := os.MkdirAll(filepath.Dir(skipPath(dataDir)), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(vs)
	return writeAtomic(skipPath(dataDir), b, 0o600)
}

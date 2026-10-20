package proto

import (
	"encoding/json"
	"os"
	"testing"
)

func TestValidVersion(t *testing.T) {
	for _, c := range []struct {
		v  string
		ok bool
	}{
		{"0.3.0", true},
		{"v1.2.3", true},
		{"0.0.9-dev", true},
		{"0.3.0-dev.12", true},
		{"1.0.0-rc.1+build.5", true},
		{"10.20.30", true},
		{"", false},
		{"1.2", false},
		{"1.2.3.4", false},
		{"01.2.3", false},
		{"1.2.3-", false},
		{"1.2.3-a..b", false},
		{"1.2.3-01", false},
		{"1.2.x", false},
		{"dev", false},
		{"0.0.0-abcdef12", true},
	} {
		if got := ValidVersion(c.v); got != c.ok {
			t.Errorf("ValidVersion(%q) = %v, want %v", c.v, got, c.ok)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.3.0", "0.3.0", 0},
		{"v0.3.0", "0.3.0", 0},
		{"0.3.0+build.1", "0.3.0+build.2", 0},
		{"0.10.0", "0.9.9", 1},
		{"0.3.0-dev", "0.3.0", -1},
		{"0.3.0-dev.2", "0.3.0-dev.10", -1},
		{"0.3.0-alpha", "0.3.0-alpha.1", -1},
		{"0.3.0-1", "0.3.0-alpha", -1},
		{"0.3.0-beta", "0.3.0-alpha", 1},
		{"0.0.10-dev", "0.0.9-dev", 1},
		{"1.0.0", "0.99.99", 1},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := CompareVersions(c.b, c.a); got != -c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d (antisymmetry)", c.b, c.a, got, -c.want)
		}
	}
}

func TestCompareVersionsInvalidSortsFirst(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"dev", "0.0.1", -1},
		{"0.0.1", "", 1},
		{"dev", "garbage", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestVersionVectorsAscending(t *testing.T) {
	b, err := os.ReadFile("../docs/test-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Versions) < 2 {
		t.Fatal("versions vector missing")
	}
	for i := range v.Versions {
		for j := range v.Versions {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := CompareVersions(v.Versions[i], v.Versions[j]); got != want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", v.Versions[i], v.Versions[j], got, want)
			}
		}
	}
}

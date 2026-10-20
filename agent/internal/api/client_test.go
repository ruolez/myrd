package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveURL(t *testing.T) {
	cases := []struct {
		base, ref, want string
		ok              bool
	}{
		{"https://rc.example.com", "/v1/releases/7/file", "https://rc.example.com/v1/releases/7/file", true},
		{"https://rc.example.com/", "/v1/releases/7/file", "https://rc.example.com/v1/releases/7/file", true},
		{"http://localhost:5560", "/v1/releases/7/file?x=1", "http://localhost:5560/v1/releases/7/file?x=1", true},
		{"https://rc.example.com", "https://cdn.example.com/a.pkg", "https://cdn.example.com/a.pkg", true},
		{"https://rc.example.com", "file:///etc/passwd", "", false},
		{"https://rc.example.com", "", "", false},
		{"", "/v1/releases/7/file", "", false},
	}
	for _, c := range cases {
		got, err := ResolveURL(c.base, c.ref)
		if got != c.want || (err == nil) != c.ok {
			t.Errorf("ResolveURL(%q, %q) = %q, %v; want %q ok=%v", c.base, c.ref, got, err, c.want, c.ok)
		}
	}
}

func TestCheckUpdate(t *testing.T) {
	want := Manifest{Version: "0.3.1", OS: "darwin", Arch: "universal", URL: "/v1/releases/3/file",
		SHA256: "ab", Size: 42, Signature: "sig"}
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("version") == "0.3.1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.CheckUpdate(context.Background(), "tok", "darwin", "arm64", "0.3.0", true)
	if err != nil || got == nil || *got != want {
		t.Fatalf("CheckUpdate = %+v, %v", got, err)
	}
	none, err := c.CheckUpdate(context.Background(), "tok", "darwin", "arm64", "0.3.1", false)
	if err != nil || none != nil {
		t.Fatalf("204 → %+v, %v", none, err)
	}
	wantQ := []string{"arch=arm64&explicit=1&os=darwin&version=0.3.0", "arch=arm64&explicit=0&os=darwin&version=0.3.1"}
	if len(queries) != 2 || queries[0] != wantQ[0] || queries[1] != wantQ[1] {
		t.Fatalf("queries = %q, want %q", queries, wantQ)
	}
}

package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://x",
		"PUBLIC_URL":   "https://remote.example.com/",
		"JWT_SECRET":   "0123456789abcdef0123",
		"TURN_SECRET":  "turn",
		"TOTP_ENC_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"TURN_URLS":    "stun:a:3478, turn:a:3478?transport=udp",
		"TURN_TTL":     "90m",
		"TRUST_PROXY":  "true",
		"LOG_LEVEL":    "debug",
	}
}

func TestLoadDerivesOriginAndDefaults(t *testing.T) {
	c, err := Load(env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicOrigin != "https://remote.example.com" || c.RPID != "remote.example.com" || !c.SecureCookies {
		t.Fatalf("origin derivation: %+v", c)
	}
	if strings.Join(c.TURNURLsAgent, ",") != "stun:a:3478,turn:a:3478?transport=udp" {
		t.Fatalf("agent urls should default to browser urls, got %v", c.TURNURLsAgent)
	}
	if c.TURNTTL != 90*time.Minute || !c.TrustProxy || c.ListenAddr != ":8080" {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"TOTP_ENC_KEY": "c2hvcnQ=",
		"TURN_TTL":     "3h",
		"JWT_SECRET":   "short",
		"PUBLIC_URL":   "ftp://x",
	}
	for k, v := range cases {
		e := validEnv()
		e[k] = v
		if _, err := Load(env(e)); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%q: want error mentioning %s, got %v", k, v, k, err)
		}
	}
}

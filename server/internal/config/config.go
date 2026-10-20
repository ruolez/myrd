// Package config loads the API configuration from the environment.
package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/ruolez/remotectl/proto"
)

type Config struct {
	DatabaseURL   string
	PublicURL     string
	PublicOrigin  string // scheme://host[:port], no path
	RPID          string // hostname of PublicURL
	SecureCookies bool
	ListenAddr    string
	MetricsAddr   string
	TrustProxy    bool

	JWTSecret  []byte
	TURNSecret string
	TOTPEncKey []byte // 32 bytes, AES-256-GCM

	TURNURLs      []string
	TURNURLsAgent []string
	TURNTTL       time.Duration

	AdminEmail           string
	AdminInitialPassword string

	ReleasePublicKey string // base64 Ed25519 key release uploads must be signed with
	DownloadsDir     string // agent installers uploaded under Admin → System
	LogLevel         slog.Level
}

// Load reads configuration through getenv (os.Getenv in production).
func Load(getenv func(string) string) (*Config, error) {
	get := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	var errs []error
	c := &Config{
		DatabaseURL:          get("DATABASE_URL", ""),
		PublicURL:            strings.TrimRight(get("PUBLIC_URL", "http://localhost:5560"), "/"),
		ListenAddr:           get("LISTEN_ADDR", ":8080"),
		MetricsAddr:          get("METRICS_ADDR", ":9090"),
		TrustProxy:           get("TRUST_PROXY", "false") == "true",
		TURNSecret:           get("TURN_SECRET", ""),
		AdminEmail:           get("ADMIN_EMAIL", ""),
		AdminInitialPassword: get("ADMIN_INITIAL_PASSWORD", ""),
		DownloadsDir:         get("DOWNLOADS_DIR", "/data/downloads"),
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		errs = append(errs, fmt.Errorf("PUBLIC_URL %q is not an http(s) URL", c.PublicURL))
	} else {
		c.PublicOrigin = u.Scheme + "://" + u.Host
		c.RPID = u.Hostname()
		c.SecureCookies = u.Scheme == "https"
	}

	jwt := get("JWT_SECRET", "")
	if len(jwt) < 16 {
		errs = append(errs, errors.New("JWT_SECRET must be at least 16 characters"))
	}
	c.JWTSecret = []byte(jwt)
	if c.TURNSecret == "" {
		errs = append(errs, errors.New("TURN_SECRET is required"))
	}
	key, err := decodeB64(get("TOTP_ENC_KEY", ""))
	if err != nil || len(key) != 32 {
		errs = append(errs, errors.New("TOTP_ENC_KEY must be base64 of 32 bytes"))
	}
	c.TOTPEncKey = key

	c.TURNURLs = splitList(get("TURN_URLS", ""))
	c.TURNURLsAgent = splitList(get("TURN_URLS_AGENT", ""))
	if len(c.TURNURLsAgent) == 0 {
		c.TURNURLsAgent = c.TURNURLs
	}
	ttl, err := time.ParseDuration(get("TURN_TTL", "2h"))
	if err != nil || ttl <= 0 || ttl > 2*time.Hour {
		errs = append(errs, errors.New("TURN_TTL must be a duration in (0, 2h]"))
	}
	c.TURNTTL = ttl

	if b, err := decodeB64(get("RELEASE_PUBLIC_KEY", proto.ReleasePublicKey)); err != nil || len(b) != ed25519.PublicKeySize {
		errs = append(errs, errors.New("RELEASE_PUBLIC_KEY must be base64 of a 32-byte Ed25519 key"))
	} else {
		c.ReleasePublicKey = base64.StdEncoding.EncodeToString(b)
	}
	if err := c.LogLevel.UnmarshalText([]byte(get("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func decodeB64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

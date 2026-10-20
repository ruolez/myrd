// Package api is the agent's HTTPS client for the control plane's agent
// endpoints (docs/PROTOCOL.md §2 "Agent").
package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ruolez/remotectl/proto"
)

// Error is a non-2xx API response.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("api %d %s: %s", e.Status, e.Code, e.Message) }

// IsCode reports whether err is an *Error with one of codes.
func IsCode(err error, codes ...string) bool {
	var ae *Error
	if !errors.As(err, &ae) {
		return false
	}
	for _, c := range codes {
		if ae.Code == c {
			return true
		}
	}
	return false
}

// Fatal reports errors that retrying cannot fix (device gone/disabled).
func Fatal(err error) bool { return IsCode(err, "device_removed", "device_disabled", "not_found") }

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path, bearer string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.StatusCode, &Error{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

type EnrollRequest struct {
	Token     string `json:"token"`
	PublicKey string `json:"public_key"`
	Hostname  string `json:"hostname"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Version   string `json:"version"`
}

type EnrollResponse struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
}

func (c *Client) Enroll(ctx context.Context, r EnrollRequest) (EnrollResponse, error) {
	var out EnrollResponse
	_, err := c.do(ctx, http.MethodPost, "/v1/agent/enroll", "", r, &out)
	return out, err
}

// Authenticate runs challenge → signed nonce → JWT.
func (c *Client) Authenticate(ctx context.Context, deviceID string, key ed25519.PrivateKey) (token string, expires time.Time, err error) {
	var ch struct {
		Nonce string `json:"nonce"`
	}
	if _, err = c.do(ctx, http.MethodPost, "/v1/agent/challenge", "", map[string]string{"device_id": deviceID}, &ch); err != nil {
		return "", time.Time{}, err
	}
	sig := ed25519.Sign(key, proto.AuthChallengeMessage(deviceID, ch.Nonce))
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if _, err = c.do(ctx, http.MethodPost, "/v1/agent/auth", "", map[string]string{
		"device_id": deviceID, "nonce": ch.Nonce, "signature": base64.StdEncoding.EncodeToString(sig),
	}, &out); err != nil {
		return "", time.Time{}, err
	}
	exp, _ := time.Parse(time.RFC3339, out.ExpiresAt)
	return out.Token, exp, nil
}

// Manifest is the release offered by GET /v1/agent/update. URL is relative
// to the server (see ResolveURL); the signature covers everything else.
type Manifest struct {
	Version   string `json:"version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

// CheckUpdate returns nil when nothing newer exists for this device (204).
// explicit asks the server to ignore the rollout cohort.
func (c *Client) CheckUpdate(ctx context.Context, token, goos, arch, version string, explicit bool) (*Manifest, error) {
	q := url.Values{"os": {goos}, "arch": {arch}, "version": {version}, "explicit": {"0"}}
	if explicit {
		q.Set("explicit", "1")
	}
	var m Manifest
	status, err := c.do(ctx, http.MethodGet, "/v1/agent/update?"+q.Encode(), token, nil, &m)
	if err != nil || status == http.StatusNoContent || m.Version == "" {
		return nil, err
	}
	return &m, nil
}

// ResolveURL resolves a release URL against the server base URL; only
// http(s) results are allowed.
func ResolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil || b.Host == "" {
		return "", fmt.Errorf("bad server URL %q", base)
	}
	r, err := url.Parse(ref)
	if err != nil || ref == "" {
		return "", fmt.Errorf("bad release URL %q", ref)
	}
	u := b.ResolveReference(r)
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("release URL scheme %q not allowed", u.Scheme)
	}
	return u.String(), nil
}

func (c *Client) DeleteSelf(ctx context.Context, token string) error {
	_, err := c.do(ctx, http.MethodDelete, "/v1/agent/self", token, nil, nil)
	return err
}

// WebSocketURL turns the base URL into the agent socket URL.
func (c *Client) WebSocketURL() string {
	u := c.BaseURL
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + "/v1/agent/ws"
}

// Package pins is the agent's authoritative list of browser client keys the
// owner approved on this device's screen (trust on first use). "Allow and
// remember" trusts the account: any browser signed in as the user who approved
// a pin is trusted too.
package pins

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ruolez/remotectl/proto"
)

type Store struct {
	mu   sync.Mutex
	path string
	pins map[string]proto.PinnedClient // key = sha256 hex of SPKI
}

// Open loads the store at path, creating an empty one if missing.
func Open(path string) (*Store, error) {
	s := &Store{path: path, pins: map[string]proto.PinnedClient{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var list []proto.PinnedClient
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, err
	}
	for _, p := range list {
		s.pins[p.PublicKeySHA256] = p
	}
	return s, nil
}

// IsPinned reports whether the SPKI (base64) is pinned.
func (s *Store) IsPinned(spkiB64 string) bool {
	h, err := proto.ClientKeyHash(spkiB64)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.pins[h]
	return ok
}

// Trusted reports whether a session from the SPKI (base64), signed in as
// userID, needs no new-browser prompt: the key is pinned, or a pin was approved
// for that user. Pins from before user ids were recorded count for any user
// (only the device's owner can open sessions to it); a pinned key fills in its
// user id on use.
func (s *Store) Trusted(spkiB64, userID string) bool {
	h, err := proto.ClientKeyHash(spkiB64)
	if err != nil {
		return false
	}
	s.mu.Lock()
	p, ok := s.pins[h]
	if ok {
		if p.UserID != "" || userID == "" {
			s.mu.Unlock()
			return true
		}
		p.UserID = userID
		s.pins[h] = p
		s.mu.Unlock()
		_ = s.save()
		return true
	}
	defer s.mu.Unlock()
	for _, p := range s.pins {
		if p.UserID == "" || (userID != "" && p.UserID == userID) {
			return true
		}
	}
	return false
}

// Pin records an approval by userID and persists it.
func (s *Store) Pin(spkiB64, label, userID string, now time.Time) error {
	h, err := proto.ClientKeyHash(spkiB64)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.pins[h] = proto.PinnedClient{PublicKeySHA256: h, Label: label, ApprovedAt: now.UTC().Format(time.RFC3339), UserID: userID}
	s.mu.Unlock()
	return s.save()
}

// RemoveHash drops a pin by its hash (device console "remove").
func (s *Store) RemoveHash(hash string) (bool, error) {
	s.mu.Lock()
	_, ok := s.pins[hash]
	delete(s.pins, hash)
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, s.save()
}

// Revoke drops every pin whose SPKI is in revoked; returns how many changed.
func (s *Store) Revoke(revokedSPKI []string) (int, error) {
	n := 0
	s.mu.Lock()
	for _, spki := range revokedSPKI {
		h, err := proto.ClientKeyHash(spki)
		if err != nil {
			continue
		}
		if _, ok := s.pins[h]; ok {
			delete(s.pins, h)
			n++
		}
	}
	s.mu.Unlock()
	if n == 0 {
		return 0, nil
	}
	return n, s.save()
}

// List returns pins sorted by approval time.
func (s *Store) List() []proto.PinnedClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]proto.PinnedClient, 0, len(s.pins))
	for _, p := range s.pins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ApprovedAt < out[j].ApprovedAt })
	return out
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.List(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Package store is a small JSON-file-backed map of pairing entries, keyed by
// "<webdav-username>:<pair-code>". It is safe for concurrent use and persists
// atomically (write temp + rename) so a crash can't truncate the file.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Entry is a paired credential: the RomM token exchanged for a pair code.
type Entry struct {
	Token     string `json:"token"`
	RommUser  int    `json:"romm_user_id"`
	CreatedAt string `json:"created_at"`
}

// Store is a persistent map of pairing key -> Entry.
type Store struct {
	path    string
	mu      sync.RWMutex
	entries map[string]Entry
}

// Open loads the store at path, creating an empty one if the file is absent.
func Open(path string) (*Store, error) {
	s := &Store{path: path, entries: make(map[string]Entry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read store %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, &s.entries); err != nil {
		return nil, fmt.Errorf("parse store %s: %w", path, err)
	}
	return s, nil
}

// Get returns the entry for key, if present.
func (s *Store) Get(key string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[key]
	return e, ok
}

// Put stores an entry and persists the store.
func (s *Store) Put(key string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = e
	return s.saveLocked()
}

// DeleteByToken removes every entry holding the given token (used to evict a
// revoked/rotated token) and persists. Returns the keys removed.
func (s *Store) DeleteByToken(token string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed []string
	for k, e := range s.entries {
		if e.Token == token {
			delete(s.entries, k)
			removed = append(removed, k)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	return removed, s.saveLocked()
}

// Tokens returns the distinct tokens currently stored.
func (s *Store) Tokens() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{}, len(s.entries))
	var out []string
	for _, e := range s.entries {
		if _, ok := seen[e.Token]; !ok {
			seen[e.Token] = struct{}{}
			out = append(out, e.Token)
		}
	}
	return out
}

// saveLocked writes the store atomically; caller must hold s.mu.
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create store dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".pairings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

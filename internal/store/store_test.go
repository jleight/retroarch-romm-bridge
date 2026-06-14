package store

import (
	"path/filepath"
	"testing"
)

func TestStorePersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "pairings.json") // dir created on save

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open empty: %v", err)
	}
	if _, ok := s.Get("jon:ABCD2345"); ok {
		t.Fatal("empty store should have no entries")
	}

	if err := s.Put("jon:ABCD2345", Entry{Token: "rmm_aaa", RommUser: 1}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Put("alice:WXYZ6789", Entry{Token: "rmm_bbb", RommUser: 2}); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Reload from disk into a fresh Store.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	e, ok := s2.Get("jon:ABCD2345")
	if !ok || e.Token != "rmm_aaa" || e.RommUser != 1 {
		t.Errorf("reloaded entry = %+v,%v", e, ok)
	}
	if got := len(s2.Tokens()); got != 2 {
		t.Errorf("Tokens() len = %d, want 2", got)
	}
}

func TestStoreDeleteByToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pairings.json")
	s, _ := Open(path)
	// two pairings share rmm_aaa (e.g. same code from two usernames)
	_ = s.Put("jon:ABCD2345", Entry{Token: "rmm_aaa"})
	_ = s.Put("jon2:ABCD2345", Entry{Token: "rmm_aaa"})
	_ = s.Put("alice:WXYZ6789", Entry{Token: "rmm_bbb"})

	removed, err := s.DeleteByToken("rmm_aaa")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %d keys, want 2", len(removed))
	}
	if _, ok := s.Get("jon:ABCD2345"); ok {
		t.Error("entry should be gone")
	}
	if _, ok := s.Get("alice:WXYZ6789"); !ok {
		t.Error("other token's entry should remain")
	}
}

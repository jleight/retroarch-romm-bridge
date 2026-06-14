package romm

import (
	"context"
	"strings"
	"sync"
)

// PlatformFsSlug extracts the platform fs-slug from a RomM asset file_path,
// e.g. "users/<hash>/saves/snes/51/snes9x" -> "snes". Returns "" if unparseable.
func (a Asset) PlatformFsSlug() string {
	parts := strings.Split(a.FilePath, "/")
	// [users, <hash>, saves|states, <platform>, <rom_id>, <emulator?>]
	if len(parts) >= 4 {
		return parts[3]
	}
	return ""
}

// Index maps RetroArch save/state basenames to RomM rom IDs. It is safe for
// concurrent use and refreshed periodically (and on demand on a miss).
type Index struct {
	client *Client

	mu sync.RWMutex
	// keyed by "<platformfsslug>\x00<normalized-stem>"; value is the rom id.
	byPlatformStem map[string]int
	// normalized-stem -> set of rom ids, used for platform-less fallback and
	// ambiguity detection.
	byStem map[string]map[int]struct{}
	// rom id -> rom, for reconstructing authoritative keys from a save's rom_id.
	byID   map[int]Rom
	loaded bool
}

// NewIndex creates an empty Index backed by client.
func NewIndex(client *Client) *Index {
	return &Index{client: client}
}

func normalizeStem(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func platformStemKey(platform, stem string) string {
	return platform + "\x00" + stem
}

// Refresh rebuilds the index from RomM's rom list.
func (ix *Index) Refresh(ctx context.Context) error {
	roms, err := ix.client.ListRoms(ctx)
	if err != nil {
		return err
	}
	return ix.refreshFrom(roms)
}

// refreshFrom rebuilds the index from an in-memory rom list (also used in tests).
func (ix *Index) refreshFrom(roms []Rom) error {
	byPlatformStem := make(map[string]int, len(roms))
	byStem := make(map[string]map[int]struct{}, len(roms))
	byID := make(map[int]Rom, len(roms))

	add := func(platform, raw string, id int) {
		stem := normalizeStem(raw)
		if stem == "" {
			return
		}
		byPlatformStem[platformStemKey(platform, stem)] = id
		set, ok := byStem[stem]
		if !ok {
			set = make(map[int]struct{})
			byStem[stem] = set
		}
		set[id] = struct{}{}
	}

	for _, r := range roms {
		byID[r.ID] = r
		// RetroArch names a save after the ROM file's basename, so fs_name_no_ext
		// is the primary key. Also index no_tags and the display name as fallbacks.
		add(r.PlatformFsSlug, r.FsNameNoExt, r.ID)
		add(r.PlatformFsSlug, r.FsNameNoTags, r.ID)
		if r.Name != "" {
			add(r.PlatformFsSlug, r.Name, r.ID)
		}
	}

	ix.mu.Lock()
	ix.byPlatformStem = byPlatformStem
	ix.byStem = byStem
	ix.byID = byID
	ix.loaded = true
	ix.mu.Unlock()
	return nil
}

// Lookup resolves a (platform fs-slug, basename-stem) to a rom id.
//
//   - If platform is non-empty and matches, that wins.
//   - Otherwise it falls back to a platform-less stem match, which succeeds
//     only when exactly one rom across all platforms has that stem (ambiguous
//     matches return ok=false so the caller can log and skip).
func (ix *Index) Lookup(platform, stem string) (id int, ok bool) {
	stem = normalizeStem(stem)
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if platform != "" {
		if id, ok = ix.byPlatformStem[platformStemKey(platform, stem)]; ok {
			return id, true
		}
	}
	set := ix.byStem[stem]
	if len(set) == 1 {
		for id := range set {
			return id, true
		}
	}
	return 0, false
}

// Rom returns the ROM with the given id, if known.
func (ix *Index) Rom(id int) (Rom, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	r, ok := ix.byID[id]
	return r, ok
}

// Loaded reports whether the index has been populated at least once.
func (ix *Index) Loaded() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.loaded
}

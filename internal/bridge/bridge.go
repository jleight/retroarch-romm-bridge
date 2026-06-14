// Package bridge wires the RomM client + index together per user and exposes
// the operations the WebDAV handler needs: build the server manifest, resolve a
// download, and store an upload.
package bridge

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jleight/retroarch-romm-bridge/internal/config"
	"github.com/jleight/retroarch-romm-bridge/internal/mapping"
	"github.com/jleight/retroarch-romm-bridge/internal/romm"
	"github.com/jleight/retroarch-romm-bridge/internal/store"
)

// errUnmapped means a RetroArch filename could not be resolved to a RomM ROM.
var errUnmapped = errors.New("no matching rom")

// ErrUnauthorized means the WebDAV credentials could not be resolved to a RomM
// token (bad/expired pair code, or a revoked token).
var ErrUnauthorized = errors.New("unauthorized")

// IsUnmapped reports whether err is an unmapped-ROM error.
func IsUnmapped(err error) bool { return errors.Is(err, errUnmapped) }

// badCodeTTL is how long a failed pair code is negative-cached, to avoid
// hammering RomM's (rate-limited) exchange endpoint on RetroArch retries.
const badCodeTTL = 2 * time.Minute

// pairAlphabet matches RomM's pairing code alphabet (no ambiguous 0/O/1/I/L).
const pairAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// tagRe matches RomM's notion of a filename tag: any "(...)" or "[...]" group
// (mirrors RomM's TAG_REGEX). Used to reconcile a RetroArch ROM basename that
// carries extra tags (region/version/romhack) the RomM library entry lacks.
var tagRe = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)

// datetimeTagRe matches the timestamp tag RomM appends to slotted saves, e.g.
// " [2025-01-02_03-04-05]" (current) or " [2025-01-02 03-04-05-678]" (older).
var datetimeTagRe = regexp.MustCompile(` \[\d{4}-\d{2}-\d{2}[ _]\d{2}-\d{2}-\d{2}(?:-\d{3})?\]`)

// stripTags removes all (...)/[...] groups and collapses whitespace, matching
// RomM's fs_name_no_tags. "Pokemon - Odyssey [v4.1.1]" -> "Pokemon - Odyssey".
func stripTags(s string) string {
	return strings.Join(strings.Fields(tagRe.ReplaceAllString(s, "")), " ")
}

// stripDatetimeTag removes only RomM's datetime tag, preserving other tags so a
// save's stored filename round-trips to RetroArch's local filename.
func stripDatetimeTag(s string) string {
	return datetimeTagRe.ReplaceAllString(s, "")
}

// userBackend holds the per-token RomM client (one per paired device token).
// The ROM index is shared across all backends (see Service.index).
type userBackend struct {
	token  string
	client *romm.Client
}

func (b *userBackend) shortToken() string {
	if len(b.token) <= 8 {
		return b.token
	}
	return "…" + b.token[len(b.token)-6:]
}

// Service resolves WebDAV credentials (username + pair code) to a RomM backend,
// exchanging codes for tokens on first use and caching them in a persistent
// store. Backends are created lazily, one per distinct RomM token.
type Service struct {
	cfg    *config.Config
	store  *store.Store
	appCtx context.Context // long-lived; used for background index priming

	mu       sync.Mutex
	backends map[string]*userBackend // keyed by RomM token
	badCodes map[string]time.Time    // normalized code -> negative-cache expiry
	index    *romm.Index             // shared ROM index (set once, then stable)

	// stateHashes caches computed MD5s for states, which (unlike saves) RomM
	// does not hash. Keyed by "<id>:<updated_at>" — a state's bytes are immutable
	// for a given (id, updated_at), so the cache never goes stale.
	stateHashes sync.Map
}

// NewService constructs a Service backed by the given pairing store.
func NewService(cfg *config.Config, st *store.Store) (*Service, error) {
	return &Service{
		cfg:      cfg,
		store:    st,
		backends: make(map[string]*userBackend),
		badCodes: make(map[string]time.Time),
	}, nil
}

// Start establishes the shared ROM index, warms backends for already-paired
// tokens, and refreshes the index on the configured interval until ctx is
// cancelled. Priming runs in the background so startup never blocks on RomM.
func (s *Service) Start(ctx context.Context) {
	s.appCtx = ctx

	// Prefer a dedicated service token for the index; otherwise it is seeded
	// lazily from the first paired backend below.
	if tok := s.cfg.RomM.IndexToken; tok != "" {
		client, err := romm.New(s.cfg.RomM.BaseURL, tok)
		if err != nil {
			slog.Error("index token client", "err", err)
		} else {
			s.mu.Lock()
			s.ensureIndexLocked(client)
			s.mu.Unlock()
		}
	}

	for _, tok := range s.store.Tokens() {
		if _, err := s.backendForToken(tok); err != nil {
			slog.Warn("init backend failed", "err", err)
		}
	}

	go func() {
		ticker := time.NewTicker(s.cfg.RomM.IndexRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if idx := s.getIndex(); idx != nil {
					if err := idx.Refresh(ctx); err != nil {
						slog.Warn("index refresh failed", "err", err)
					}
				}
			}
		}
	}()
}

// getIndex returns the shared ROM index (nil until established).
func (s *Service) getIndex() *romm.Index {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index
}

// lookup resolves (platform, stem) -> rom_id via the shared index. It tries the
// stem as-is, then with tags stripped, since a RetroArch ROM basename often
// carries region/version/romhack tags the RomM library entry doesn't have.
// Returns false if the index isn't established yet or nothing matches.
func (s *Service) lookup(platform, stem string) (int, bool) {
	idx := s.getIndex()
	if idx == nil {
		return 0, false
	}
	if id, ok := idx.Lookup(platform, stem); ok {
		return id, true
	}
	if stripped := stripTags(stem); stripped != "" && stripped != stem {
		return idx.Lookup(platform, stripped)
	}
	return 0, false
}

// ensureIndexLocked establishes the shared index from client if not already
// set, priming it in the background. Caller must hold s.mu.
func (s *Service) ensureIndexLocked(client *romm.Client) {
	if s.index != nil {
		return
	}
	idx := romm.NewIndex(client)
	s.index = idx
	go func() {
		ctx := s.appCtx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := idx.Refresh(ctx); err != nil {
			slog.Warn("index prime failed", "err", err)
		} else {
			slog.Info("index primed")
		}
	}()
}

// Resolve maps WebDAV Basic credentials to a backend. The password is treated
// as a RomM pairing code: if "<username>:<code>" is already paired, the cached
// token is used; otherwise the code is exchanged for a token (once) and stored.
func (s *Service) Resolve(ctx context.Context, username, password string) (*userBackend, error) {
	code, ok := normalizeCode(password)
	if !ok {
		return nil, ErrUnauthorized // not a plausible pair code
	}
	// Fold the username to uppercase so the pairing key is case-insensitive
	// (matching the uppercase pair code).
	key := strings.ToUpper(strings.TrimSpace(username)) + ":" + code

	// Fast path: already paired.
	if e, ok := s.store.Get(key); ok {
		return s.backendForToken(e.Token)
	}

	// Miss: exchange the code. Serialize to avoid a double-exchange race (the
	// code is single-use) and to guard the negative cache.
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.store.Get(key); ok {
		return s.backendForTokenLocked(e.Token)
	}
	if exp, bad := s.badCodes[code]; bad {
		if time.Now().Before(exp) {
			return nil, ErrUnauthorized
		}
		delete(s.badCodes, code)
	}
	token, userID, err := romm.ExchangePairCode(ctx, s.cfg.RomM.BaseURL, code)
	if err != nil {
		slog.Warn("pair exchange failed", "user", username, "err", err)
		s.badCodes[code] = time.Now().Add(badCodeTTL)
		return nil, ErrUnauthorized
	}
	if err := s.store.Put(key, store.Entry{
		Token:     token,
		RommUser:  userID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, fmt.Errorf("persist pairing: %w", err)
	}
	slog.Info("device paired", "user", username, "romm_user_id", userID)
	return s.backendForTokenLocked(token)
}

func (s *Service) backendForToken(token string) (*userBackend, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backendForTokenLocked(token)
}

// backendForTokenLocked returns (creating if needed) the backend for a token.
// Caller must hold s.mu. Creating the first backend seeds the shared index if
// no dedicated index token established it.
func (s *Service) backendForTokenLocked(token string) (*userBackend, error) {
	if b, ok := s.backends[token]; ok {
		return b, nil
	}
	client, err := romm.New(s.cfg.RomM.BaseURL, token)
	if err != nil {
		return nil, err
	}
	b := &userBackend{token: token, client: client}
	s.backends[token] = b
	s.ensureIndexLocked(client)
	return b, nil
}

// afterCall evicts a backend and its pairings if RomM rejected the token, so a
// rotated/revoked token forces the user to re-pair.
func (s *Service) afterCall(b *userBackend, err error) error {
	if errors.Is(err, romm.ErrUnauthorized) {
		s.mu.Lock()
		delete(s.backends, b.token)
		s.mu.Unlock()
		if removed, derr := s.store.DeleteByToken(b.token); derr != nil {
			slog.Warn("evict token failed", "err", derr)
		} else if len(removed) > 0 {
			slog.Info("evicted revoked token", "token", b.shortToken(), "pairings", len(removed))
		}
	}
	return err
}

// normalizeCode canonicalizes a pair code the way RomM does (strip dashes/spaces,
// uppercase) and reports whether it's a plausible 8-char code. This filters out
// RetroArch's normal Basic-auth noise so we don't treat every password as a code.
func normalizeCode(s string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if r == '-' || r == ' ' {
			continue
		}
		if !strings.ContainsRune(pairAlphabet, r) {
			return "", false
		}
		b.WriteRune(r)
	}
	code := b.String()
	if len(code) != 8 {
		return "", false
	}
	return code, true
}

// ManifestEntry is one line of the server manifest RetroArch consumes.
type ManifestEntry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// Manifest builds the server manifest for a user from their current RomM saves
// and states. Each entry's hash is RomM's MD5 content_hash, which equals the
// MD5 RetroArch computes over the bytes we serve for the same file.
func (s *Service) Manifest(ctx context.Context, b *userBackend) ([]ManifestEntry, error) {
	saves, err := b.client.ListSaves(ctx)
	if err != nil {
		return nil, s.afterCall(b, fmt.Errorf("list saves: %w", err))
	}
	states, err := b.client.ListStates(ctx)
	if err != nil {
		return nil, s.afterCall(b, fmt.Errorf("list states: %w", err))
	}
	// RomM keeps timestamped history rows per game; RetroArch's keyspace is
	// flat, so collapse to the newest row per key.
	type pick struct {
		entry ManifestEntry
		asset romm.Asset
	}
	newest := make(map[string]pick)
	add := func(kind romm.AssetKind, assets []romm.Asset) {
		for _, a := range assets {
			// Saves carry RomM's MD5; states don't, so compute (and cache) it.
			// Key from the save's own filename (with RomM's datetime tag removed)
			// so it round-trips to RetroArch's local file — preserving any
			// region/version tags the matched ROM's library name may lack.
			hash := a.ContentHash
			if hash == "" {
				h, ok := s.stateHash(ctx, b, a)
				if !ok {
					continue // couldn't hash it; omit rather than advertise a wrong hash
				}
				hash = h
			}
			folder := s.cfg.ContentDirFor(a.PlatformFsSlug())
			key := mapping.CollectionPath(kind, folder, stripDatetimeTag(a.FileName))
			if prev, ok := newest[key]; ok && !a.NewerThan(prev.asset) {
				continue
			}
			newest[key] = pick{
				entry: ManifestEntry{Path: key, Hash: hash},
				asset: a,
			}
		}
	}
	add(romm.KindSave, saves)
	add(romm.KindState, states)

	entries := make([]ManifestEntry, 0, len(newest))
	for _, p := range newest {
		entries = append(entries, p.entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// Download resolves a parsed asset key to a RomM asset and returns its bytes.
// Returns (nil, nil) when no matching asset exists (RetroArch treats 404 as
// "not present", which is valid).
func (s *Service) Download(ctx context.Context, b *userBackend, key mapping.Key) (io.ReadCloser, error) {
	platform := s.cfg.PlatformFor(key.ContentDir)
	assets, err := s.listKind(ctx, b, key.Kind)
	if err != nil {
		return nil, s.afterCall(b, err)
	}
	// Prefer the index (authoritative ROM name); fall back to matching the
	// asset's own name when the index hasn't primed yet (e.g. just after
	// pairing), so the first sync can still pull existing saves.
	var match *romm.Asset
	if romID, ok := s.lookup(platform, key.Stem); ok {
		match = newestForRom(assets, romID, key.Ext)
	} else {
		match = findByName(assets, platform, key.Stem, key.Ext)
	}
	if match == nil {
		return nil, nil // not found -> 404 (valid "not present")
	}
	body, err := b.client.Download(ctx, match.FullPath)
	return body, s.afterCall(b, err)
}

// Store handles an upload (PUT): resolve the ROM, then update the existing
// asset or create a new one. Returns errUnmapped if the filename can't be
// matched to a ROM (the handler reports this as a non-fatal 404 to RetroArch).
func (s *Service) Store(ctx context.Context, b *userBackend, key mapping.Key, content []byte) error {
	platform := s.cfg.PlatformFor(key.ContentDir)

	romID, ok := s.lookup(platform, key.Stem)
	if !ok {
		// Maybe the ROM was added since the last refresh; try once more.
		if idx := s.getIndex(); idx != nil {
			if err := idx.Refresh(ctx); err != nil {
				slog.Warn("index refresh on miss failed", "err", err)
			}
		}
		romID, ok = s.lookup(platform, key.Stem)
	}
	if !ok {
		return fmt.Errorf("%w: platform=%q stem=%q", errUnmapped, platform, key.Stem)
	}

	// Look for an existing asset for this ROM with the same stable filename.
	assets, err := s.listKind(ctx, b, key.Kind)
	if err != nil {
		return s.afterCall(b, err)
	}
	// Update the newest existing asset for this ROM+extension, else create one.
	var saved *romm.Asset
	if existing := newestForRom(assets, romID, key.Ext); existing != nil {
		saved, err = b.client.Update(ctx, key.Kind, existing.ID, key.FileName, content)
	} else {
		saved, err = b.client.Upload(ctx, key.Kind, romID, key.FileName, content)
	}
	if err != nil {
		return s.afterCall(b, err)
	}
	// States have no RomM hash; pre-seed the cache with the bytes we just stored
	// so the next manifest doesn't have to download them back to hash them.
	if key.Kind == romm.KindState && saved != nil {
		s.stateHashes.Store(fmt.Sprintf("%d:%s", saved.ID, saved.UpdatedAt), md5hex(content))
	}
	return nil
}

func (s *Service) listKind(ctx context.Context, b *userBackend, kind romm.AssetKind) ([]romm.Asset, error) {
	if kind == romm.KindState {
		return b.client.ListStates(ctx)
	}
	return b.client.ListSaves(ctx)
}

// findByName is the cold-index fallback for downloads: it matches the requested
// stem (tags stripped, mirroring RomM) against the asset's file_name_no_tags +
// extension, preferring a platform match. Used until the shared index primes.
func findByName(assets []romm.Asset, platform, stem, ext string) *romm.Asset {
	stem = stripTags(stem)
	var best *romm.Asset
	bestPlatform := false
	for i := range assets {
		a := &assets[i]
		if !strings.EqualFold(a.FileExtension, ext) || !strings.EqualFold(a.FileNameNoTags, stem) {
			continue
		}
		samePlatform := platform != "" && a.PlatformFsSlug() == platform
		switch {
		case best == nil:
			best, bestPlatform = a, samePlatform
		case samePlatform && !bestPlatform:
			best, bestPlatform = a, true
		case samePlatform == bestPlatform && a.NewerThan(*best):
			best = a
		}
	}
	return best
}

// stateHash returns the MD5 of a state's bytes (RomM doesn't hash states),
// using the cache when possible and otherwise downloading and hashing once.
func (s *Service) stateHash(ctx context.Context, b *userBackend, a romm.Asset) (string, bool) {
	cacheKey := fmt.Sprintf("%d:%s", a.ID, a.UpdatedAt)
	if h, ok := s.stateHashes.Load(cacheKey); ok {
		return h.(string), true
	}
	body, err := b.client.Download(ctx, a.FullPath)
	if err != nil {
		s.afterCall(b, err)
		slog.Warn("hash state: download failed", "state_id", a.ID, "err", err)
		return "", false
	}
	defer body.Close()
	sum := md5.New()
	if _, err := io.Copy(sum, body); err != nil {
		return "", false
	}
	h := hex.EncodeToString(sum.Sum(nil))
	s.stateHashes.Store(cacheKey, h)
	return h, true
}

func md5hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// newestForRom returns the most recently updated asset for the given ROM whose
// file extension matches ext (case-insensitive). Matching on rom_id + extension
// avoids RomM's filename sanitization entirely. A RetroArch save has a single
// extension ("srm"); states use the slot as the extension ("state", "state1").
func newestForRom(assets []romm.Asset, romID int, ext string) *romm.Asset {
	var best *romm.Asset
	for i := range assets {
		a := &assets[i]
		if a.RomID != romID || !strings.EqualFold(a.FileExtension, ext) {
			continue
		}
		if best == nil || a.NewerThan(*best) {
			best = a
		}
	}
	return best
}

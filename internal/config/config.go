// Package config loads configuration from environment variables (with
// defaults). There is no config file: the only required value is ROMM_BASE_URL.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Environment variables (all optional except ROMM_BASE_URL):
//
//	ROMM_BASE_URL           RomM instance base URL (required), e.g. https://romm.example.com
//	ROMM_API_TOKEN          service token with scope roms.read; keeps the shared
//	                        ROM index warm. If unset, the index is seeded from the
//	                        first paired device (whose token then needs roms.read).
//	LISTEN_ADDR             HTTP listen address (default ":8080")
//	INDEX_REFRESH_INTERVAL  ROM index refresh interval (default "1h")
//	STORE_PATH              pairing store file path (default "/data/pairings.json")
//	PLATFORM_MAP            optional content-dir→fs-slug overrides, comma-separated
//	                        "dir=slug" pairs, e.g. "Nintendo - Game Boy Advance=gba"
const (
	envBaseURL       = "ROMM_BASE_URL"
	envAPIToken      = "ROMM_API_TOKEN"
	envListen        = "LISTEN_ADDR"
	envRefresh       = "INDEX_REFRESH_INTERVAL"
	envStorePath     = "STORE_PATH"
	envPlatformMap   = "PLATFORM_MAP"
	defaultListen    = ":8080"
	defaultRefresh   = time.Hour
	defaultStorePath = "/data/pairings.json"
)

// Config is the resolved runtime configuration.
type Config struct {
	Server      Server
	RomM        RomM
	Store       Store
	PlatformMap map[string]string
}

// Server holds HTTP listener settings.
type Server struct {
	Listen string
}

// RomM holds settings for talking to the upstream RomM instance.
type RomM struct {
	BaseURL              string
	IndexRefreshInterval time.Duration
	// IndexToken is an optional service token (scope roms.read) used to keep a
	// single shared ROM index warm, independent of device pairings.
	IndexToken string
}

// Store holds settings for the persistent pairing store.
type Store struct {
	Path string
}

// Load builds the configuration from environment variables.
func Load() (*Config, error) {
	c := &Config{
		Server: Server{Listen: envOr(envListen, defaultListen)},
		RomM: RomM{
			BaseURL:              strings.TrimRight(os.Getenv(envBaseURL), "/"),
			IndexRefreshInterval: defaultRefresh,
			IndexToken:           os.Getenv(envAPIToken),
		},
		Store:       Store{Path: envOr(envStorePath, defaultStorePath)},
		PlatformMap: parsePlatformMap(os.Getenv(envPlatformMap)),
	}

	if v := os.Getenv(envRefresh); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envRefresh, err)
		}
		c.RomM.IndexRefreshInterval = d
	}

	if c.RomM.BaseURL == "" {
		return nil, fmt.Errorf("%s is required", envBaseURL)
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parsePlatformMap parses "dir=slug,dir2=slug2" into a map. Whitespace around
// keys/values is trimmed; malformed entries are skipped.
func parsePlatformMap(s string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			continue
		}
		m[k] = v
	}
	return m
}

// PlatformFor resolves a RetroArch content-directory name to a RomM platform
// fs-slug. If the directory isn't in the explicit map, it is returned verbatim
// (the common case where the content dir already equals the fs-slug, e.g. "gba").
func (c *Config) PlatformFor(contentDir string) string {
	if slug, ok := c.PlatformMap[contentDir]; ok {
		return slug
	}
	return contentDir
}

// ContentDirFor is the inverse of PlatformFor: given a RomM platform fs-slug,
// it returns the content-directory name RetroArch expects in manifest keys.
// Defaults to the slug itself (identity), which is the common case. If multiple
// content dirs map to the same slug, the lexically-first is chosen for stability.
func (c *Config) ContentDirFor(slug string) string {
	best := ""
	for dir, s := range c.PlatformMap {
		if s == slug && (best == "" || dir < best) {
			best = dir
		}
	}
	if best != "" {
		return best
	}
	return slug
}

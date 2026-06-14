package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	// Only ROMM_BASE_URL is required; the rest take defaults.
	t.Setenv("ROMM_BASE_URL", "https://romm.example.com/") // trailing slash trimmed
	t.Setenv("ROMM_API_TOKEN", "")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("INDEX_REFRESH_INTERVAL", "")
	t.Setenv("STORE_PATH", "")
	t.Setenv("PLATFORM_MAP", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.RomM.BaseURL != "https://romm.example.com" {
		t.Errorf("BaseURL = %q (trailing slash should be trimmed)", c.RomM.BaseURL)
	}
	if c.Server.Listen != defaultListen {
		t.Errorf("Listen = %q, want default %q", c.Server.Listen, defaultListen)
	}
	if c.RomM.IndexRefreshInterval != defaultRefresh {
		t.Errorf("refresh = %v, want default", c.RomM.IndexRefreshInterval)
	}
	if c.Store.Path != defaultStorePath {
		t.Errorf("store path = %q, want default", c.Store.Path)
	}

	// Overrides
	t.Setenv("ROMM_API_TOKEN", "rmm_svc")
	t.Setenv("LISTEN_ADDR", ":9000")
	t.Setenv("INDEX_REFRESH_INTERVAL", "30m")
	t.Setenv("STORE_PATH", "/tmp/p.json")
	t.Setenv("PLATFORM_MAP", "Nintendo - Game Boy Advance=gba, snes=snes")
	c, err = Load()
	if err != nil {
		t.Fatalf("Load overrides: %v", err)
	}
	if c.RomM.IndexToken != "rmm_svc" || c.Server.Listen != ":9000" ||
		c.RomM.IndexRefreshInterval != 30*time.Minute || c.Store.Path != "/tmp/p.json" {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.PlatformMap["Nintendo - Game Boy Advance"] != "gba" || c.PlatformMap["snes"] != "snes" {
		t.Errorf("PLATFORM_MAP parse = %v", c.PlatformMap)
	}
}

func TestLoadRequiresBaseURL(t *testing.T) {
	t.Setenv("ROMM_BASE_URL", "")
	if _, err := Load(); err == nil {
		t.Error("expected error when ROMM_BASE_URL is unset")
	}
}

func TestLoadBadRefresh(t *testing.T) {
	t.Setenv("ROMM_BASE_URL", "https://romm.example.com")
	t.Setenv("INDEX_REFRESH_INTERVAL", "nonsense")
	if _, err := Load(); err == nil {
		t.Error("expected error for bad INDEX_REFRESH_INTERVAL")
	}
}

func TestPlatformForAndContentDirFor(t *testing.T) {
	c := &Config{PlatformMap: map[string]string{
		"Nintendo - Game Boy Advance": "gba",
		"gba":                         "gba",
		"Sega - Mega Drive - Genesis": "genesis-slash-megadrive",
	}}

	// mapped names resolve to the slug
	if got := c.PlatformFor("Nintendo - Game Boy Advance"); got != "gba" {
		t.Errorf("PlatformFor(mapped) = %q, want gba", got)
	}
	// unmapped name passes through verbatim (identity)
	if got := c.PlatformFor("psx"); got != "psx" {
		t.Errorf("PlatformFor(unmapped) = %q, want psx", got)
	}

	// reverse: lexically-first content dir mapping to the slug
	if got := c.ContentDirFor("gba"); got != "Nintendo - Game Boy Advance" {
		t.Errorf("ContentDirFor(gba) = %q, want lexically-first mapping", got)
	}
	// reverse of an unmapped slug is the slug itself
	if got := c.ContentDirFor("snes"); got != "snes" {
		t.Errorf("ContentDirFor(snes) = %q, want snes", got)
	}
}

func TestPlatformForNilMap(t *testing.T) {
	c := &Config{}
	if got := c.PlatformFor("gba"); got != "gba" {
		t.Errorf("PlatformFor with nil map = %q, want gba", got)
	}
}

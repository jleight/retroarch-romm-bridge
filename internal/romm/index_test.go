package romm

import "testing"

func TestPlatformFsSlug(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"users/557365723a31/saves/snes/51/snes9x", "snes"},
		{"users/abc/states/gba/92", "gba"},
		{"too/short", ""},
	}
	for _, tt := range tests {
		if got := (Asset{FilePath: tt.path}).PlatformFsSlug(); got != tt.want {
			t.Errorf("PlatformFsSlug(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestAssetNewerThan(t *testing.T) {
	a := Asset{UpdatedAt: "2026-06-13T13:51:21.089532+00:00"}
	b := Asset{UpdatedAt: "2026-06-13T13:51:21.002708+00:00"}
	if !a.NewerThan(b) {
		t.Error("a should be newer than b")
	}
	if b.NewerThan(a) {
		t.Error("b should not be newer than a")
	}
}

func TestIndexLookup(t *testing.T) {
	ix := NewIndex(nil)
	if err := ix.refreshFrom([]Rom{
		{ID: 51, PlatformFsSlug: "snes", FsNameNoExt: "Secret of Mana", FsNameNoTags: "Secret of Mana"},
		{ID: 92, PlatformFsSlug: "gba", FsNameNoExt: "Sonic Advance", FsNameNoTags: "Sonic Advance"},
		{ID: 7, PlatformFsSlug: "genesis", FsNameNoExt: "Sonic", FsNameNoTags: "Sonic"},
		{ID: 8, PlatformFsSlug: "gba", FsNameNoExt: "Sonic", FsNameNoTags: "Sonic"},
	}); err != nil {
		t.Fatal(err)
	}

	// exact platform+stem match (case-insensitive)
	if id, ok := ix.Lookup("snes", "secret of mana"); !ok || id != 51 {
		t.Errorf("snes/secret of mana = %d,%v want 51,true", id, ok)
	}
	// platform disambiguates a colliding stem
	if id, ok := ix.Lookup("gba", "Sonic"); !ok || id != 8 {
		t.Errorf("gba/Sonic = %d,%v want 8,true", id, ok)
	}
	if id, ok := ix.Lookup("genesis", "Sonic"); !ok || id != 7 {
		t.Errorf("genesis/Sonic = %d,%v want 7,true", id, ok)
	}
	// platform-less lookup is ambiguous for "Sonic" -> no match
	if _, ok := ix.Lookup("", "Sonic"); ok {
		t.Error("ambiguous platform-less Sonic should not resolve")
	}
	// platform-less lookup of a unique stem resolves
	if id, ok := ix.Lookup("", "Sonic Advance"); !ok || id != 92 {
		t.Errorf("''/Sonic Advance = %d,%v want 92,true", id, ok)
	}
	// rom metadata is retrievable by id
	if r, ok := ix.Rom(51); !ok || r.FsNameNoExt != "Secret of Mana" {
		t.Errorf("Rom(51) = %+v,%v", r, ok)
	}
}

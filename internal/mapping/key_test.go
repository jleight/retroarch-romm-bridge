package mapping

import (
	"testing"

	"github.com/jleight/retroarch-romm-bridge/internal/romm"
)

func TestParse(t *testing.T) {
	tests := []struct {
		path       string
		wantResult ParseResult
		wantKind   romm.AssetKind
		wantDir    string
		wantStem   string
		wantExt    string
	}{
		{"/manifest.server", ResultManifest, 0, "", "", ""},
		{"manifest.server", ResultManifest, 0, "", "", ""},
		{"/saves/gba/Pokemon Emerald.srm", ResultAsset, romm.KindSave, "gba", "Pokemon Emerald", "srm"},
		{"saves/Pokemon Emerald.srm", ResultAsset, romm.KindSave, "", "Pokemon Emerald", "srm"},
		{"/states/snes/Game v1.1 (USA).state1", ResultAsset, romm.KindState, "snes", "Game v1.1 (USA)", "state1"},
		{"/states/snes/Game.state", ResultAsset, romm.KindState, "snes", "Game", "state"},
		{"/config/retroarch.cfg", ResultUnknown, 0, "", "", ""},
		{"/thumbnails/x.png", ResultUnknown, 0, "", "", ""},
		{"/saves/", ResultUnknown, 0, "", "", ""},
		{"/saves", ResultUnknown, 0, "", "", ""},
		{"/", ResultUnknown, 0, "", "", ""},
	}
	for _, tt := range tests {
		gotResult, key := Parse(tt.path)
		if gotResult != tt.wantResult {
			t.Errorf("Parse(%q) result = %v, want %v", tt.path, gotResult, tt.wantResult)
			continue
		}
		if tt.wantResult != ResultAsset {
			continue
		}
		if key.Kind != tt.wantKind || key.ContentDir != tt.wantDir || key.Stem != tt.wantStem || key.Ext != tt.wantExt {
			t.Errorf("Parse(%q) = {kind:%v dir:%q stem:%q ext:%q}, want {kind:%v dir:%q stem:%q ext:%q}",
				tt.path, key.Kind, key.ContentDir, key.Stem, key.Ext, tt.wantKind, tt.wantDir, tt.wantStem, tt.wantExt)
		}
	}
}

func TestAssetKey(t *testing.T) {
	tests := []struct {
		kind     romm.AssetKind
		folder   string
		basename string
		ext      string
		want     string
	}{
		// key must use the ROM basename verbatim, including "+" that RomM strips.
		{romm.KindSave, "gba", "2 Game Pack! - Uno + Skip-Bo", "srm", "saves/gba/2 Game Pack! - Uno + Skip-Bo.srm"},
		{romm.KindSave, "", "Sonic", "srm", "saves/Sonic.srm"},
		{romm.KindState, "snes", "Zelda", "state1", "states/snes/Zelda.state1"},
	}
	for _, tt := range tests {
		if got := AssetKey(tt.kind, tt.folder, tt.basename, tt.ext); got != tt.want {
			t.Errorf("AssetKey(%v,%q,%q,%q) = %q, want %q", tt.kind, tt.folder, tt.basename, tt.ext, got, tt.want)
		}
	}
}

// A parsed key fed back through AssetKey must round-trip to the original path.
func TestParseAssetKeyRoundTrip(t *testing.T) {
	paths := []string{
		"saves/gba/2 Game Pack! - Uno + Skip-Bo.srm",
		"states/snes/Game v1.1 (USA).state1",
		"saves/Sonic.srm",
	}
	for _, p := range paths {
		_, key := Parse(p)
		got := AssetKey(key.Kind, key.ContentDir, key.Stem, key.Ext)
		if got != p {
			t.Errorf("round-trip %q -> %q", p, got)
		}
	}
}

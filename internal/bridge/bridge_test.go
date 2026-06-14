package bridge

import (
	"testing"

	"github.com/jleight/retroarch-romm-bridge/internal/romm"
)

func TestNewestForRom(t *testing.T) {
	assets := []romm.Asset{
		{ID: 1, RomID: 51, FileExtension: "srm", UpdatedAt: "2026-01-01T00:00:00+00:00"},
		{ID: 2, RomID: 51, FileExtension: "srm", UpdatedAt: "2026-06-01T00:00:00+00:00"}, // newest srm
		{ID: 3, RomID: 51, FileExtension: "state", UpdatedAt: "2026-03-01T00:00:00+00:00"},
		{ID: 4, RomID: 99, FileExtension: "srm", UpdatedAt: "2026-09-01T00:00:00+00:00"}, // other rom
	}

	if got := newestForRom(assets, 51, "srm"); got == nil || got.ID != 2 {
		t.Errorf("newest srm for rom 51 = %v, want id 2", got)
	}
	// extension match is case-insensitive
	if got := newestForRom(assets, 51, "SRM"); got == nil || got.ID != 2 {
		t.Errorf("case-insensitive ext match = %v, want id 2", got)
	}
	if got := newestForRom(assets, 51, "state"); got == nil || got.ID != 3 {
		t.Errorf("state for rom 51 = %v, want id 3", got)
	}
	if got := newestForRom(assets, 51, "state1"); got != nil {
		t.Errorf("missing extension should be nil, got %v", got)
	}
	if got := newestForRom(assets, 12345, "srm"); got != nil {
		t.Errorf("unknown rom should be nil, got %v", got)
	}
}

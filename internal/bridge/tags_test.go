package bridge

import "testing"

func TestStripTags(t *testing.T) {
	tests := map[string]string{
		"Pokemon - Odyssey [v4.1.1]": "Pokemon - Odyssey",
		"Super Mario World (USA)":    "Super Mario World",
		"Game (USA) [!] [v1.2]":      "Game",
		"Sonic the Hedgehog":         "Sonic the Hedgehog",
		"Zelda (USA) (Rev 1)":        "Zelda",
		"  Padded  [tag]  Name ":     "Padded Name",
	}
	for in, want := range tests {
		if got := stripTags(in); got != want {
			t.Errorf("stripTags(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripDatetimeTag(t *testing.T) {
	// datetime tag is removed; other tags (e.g. version) are preserved
	tests := map[string]string{
		"Game [2026-01-02_03-04-05].srm":                       "Game.srm",
		"Game [2026-03-31 13-18-28-747].srm":                   "Game.srm", // older format
		"Pokemon - Odyssey [v4.1.1].srm":                       "Pokemon - Odyssey [v4.1.1].srm",
		"Pokemon - Odyssey [v4.1.1] [2026-01-02_03-04-05].srm": "Pokemon - Odyssey [v4.1.1].srm",
		"NoTag.srm": "NoTag.srm",
	}
	for in, want := range tests {
		if got := stripDatetimeTag(in); got != want {
			t.Errorf("stripDatetimeTag(%q) = %q, want %q", in, got, want)
		}
	}
}

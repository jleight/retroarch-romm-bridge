package bridge

import "testing"

func TestNormalizeCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"ABCD2345", "ABCD2345", true},
		{"abcd2345", "ABCD2345", true},    // lowercased
		{"ABCD-2345", "ABCD2345", true},   // dashes stripped
		{" abcd 2345 ", "ABCD2345", true}, // spaces stripped/trimmed
		{"ABCD234", "", false},            // too short
		{"ABCD23456", "", false},          // too long
		{"ABCD2340", "", false},           // '0' not in alphabet
		{"ABCD234I", "", false},           // 'I' not in alphabet
		{"a-very-long-random-password", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeCode(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("normalizeCode(%q) = (%q,%v), want (%q,%v)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

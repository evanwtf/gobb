package password

import "testing"

func TestPhpassKnownAnswer(t *testing.T) {
	got, ok := phpassDigest("9IQRaTwmf", "test12345")
	if !ok || got != "eRo7ud9Fh4E2PdI0S3r.L0" {
		t.Errorf("phpassDigest known answer: digest mismatch, ok = %t", ok)
	}
}

func TestPhpassBadSetting(t *testing.T) {
	cases := []struct {
		name    string
		setting string
	}{
		{"empty", ""},
		{"too short", "9IQRaTwm"},
		{"too long", "9IQRaTwmfx"},
		{"count index 6", "4IQRaTwmf"},
		{"count index 31", "TIQRaTwmf"},
		{"count char outside alphabet", "!IQRaTwmf"},
		{"count char is a space", " IQRaTwmf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := phpassDigest(tc.setting, "test12345")
			if got != "" || ok {
				t.Errorf("phpassDigest(%q) = %q, %t; want empty, false", tc.setting, got, ok)
			}
		})
	}
}

func TestPhpassCountBounds(t *testing.T) {
	// Index 7 ('5') is the lowest valid count. Index 30 ('S') is the highest and is
	// too slow to run here, so only the low bound runs.
	if _, ok := phpassDigest("5IQRaTwmf", "x"); !ok {
		t.Error("phpassDigest rejects the lowest valid count")
	}
}

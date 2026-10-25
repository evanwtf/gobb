package version

import (
	"strings"
	"testing"
)

func TestBuildCommitIsSanitized(t *testing.T) {
	for _, tc := range []struct {
		name, commit, revision, want string
	}{
		{"a full SHA is cut to 8", "0f0f0fac1234", "", "0f0f0fac"},
		{"seven characters are enough", "0f0f0fa", "", "0f0f0fa"},
		{"upper case is lowered", "0F0F0FAC1234", "", "0f0f0fac"},
		{"empty uses the VCS revision", "", "abcdef1234567890", "abcdef12"},
		{"empty with no revision", "", "", "unknown"},
		{"empty with a bad revision", "", "<script>", "unknown"},
		{"markup", "<script>", "abcdef12", "unknown"},
		{"not hex", "zzzzzzz", "abcdef12", "unknown"},
		{"six characters", "0f0f0f", "", "unknown"},
		{"41 characters", strings.Repeat("a", 41), "", "unknown"},
		{"40 characters", strings.Repeat("a", 40), "", "aaaaaaaa"},
		{"a quote", `0f0f0fac"onload=x`, "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildFrom(tc.commit, tc.revision); got != tc.want {
				t.Errorf("buildFrom(%q, %q) = %q, want %q", tc.commit, tc.revision, got, tc.want)
			}
		})
	}
}

func TestBuildIsEightHexOrUnknown(t *testing.T) {
	got := Build()
	if got == "unknown" {
		return
	}
	if len(got) != 8 || strings.Trim(got, "0123456789abcdef") != "" {
		t.Errorf("Build() = %q, want 8 lower-case hex characters or unknown", got)
	}
}

package main

import (
	"strings"
	"testing"
)

const sample = "# comment\n" +
	"US\t+404251-0740023\tAmerica/New_York\tEastern (most areas)\n" +
	"AR\t-3436-05827\tAmerica/Argentina/Buenos_Aires\tEast Argentina\n" +
	"GB\t+513030-0000731\tEurope/London\n"

func TestGenerate(t *testing.T) {
	src, err := generate(strings.NewReader(sample), "2026c")
	if err != nil {
		t.Fatal(err)
	}
	got := string(src)
	for _, want := range []string{
		"tzdata 2026c; DO NOT EDIT.",
		`const TZDataVersion = "2026c"`,
		"\t\"America/Argentina/Buenos_Aires\",\n\t\"America/New_York\",\n\t\"Europe/London\",\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}

func TestGenerateRefuses(t *testing.T) {
	tests := []struct{ name, in, version string }{
		{"duplicate", sample + "US\t+0+0\tEurope/London\n", "2026c"},
		{"short line", "US\t+0+0\n", "2026c"},
		{"bad name", "US\t+0+0\tEtc;rm\n", "2026c"},
		{"empty", "# only a comment\n", "2026c"},
		{"bad version", sample, "latest"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := generate(strings.NewReader(tc.in), tc.version); err == nil {
				t.Error("generate returned nil, want an error")
			}
		})
	}
}

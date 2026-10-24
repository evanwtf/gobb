package web

import (
	"math"
	"regexp"
	"strconv"
	"testing"
)

// schemeTokens are the custom properties that every scheme defines.
var schemeTokens = []string{
	"--page", "--row", "--bar", "--bar-text", "--header", "--header-text",
	"--text", "--muted", "--link", "--accent", "--new",
}

// slateSpec is the Slate palette of spec 13.
var slateSpec = map[string]string{
	"--page":   "#233346",
	"--row":    "#2a3c52",
	"--bar":    "#596d86",
	"--link":   "#4fc0ff",
	"--text":   "#dbe3ec",
	"--header": "#2e425b",
	"--accent": "#009aff",
	"--new":    "#ff0000",
}

// parseSchemes reads the scheme rules of the embedded stylesheet.
func parseSchemes(t *testing.T) map[string]map[string]string {
	t.Helper()
	css, err := staticFS.ReadFile("static/gobb.css")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	rule := regexp.MustCompile(`:root\[data-scheme="([a-z-]+)"\]\s*\{([^}]*)\}`)
	decl := regexp.MustCompile(`(--[a-z-]+)\s*:\s*([^;]+);`)
	for _, m := range rule.FindAllStringSubmatch(string(css), -1) {
		tokens := map[string]string{}
		for _, d := range decl.FindAllStringSubmatch(m[2], -1) {
			tokens[d[1]] = d[2]
		}
		out[m[1]] = tokens
	}
	return out
}

// channel converts one sRGB byte, as 0 to 255, to linear light.
func channel(v float64) float64 {
	v /= 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

// luminance is the WCAG 2 relative luminance of a #rrggbb color.
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(hex) {
		t.Fatalf("%q is not a #rrggbb color", hex)
	}
	var c [3]float64
	for i := range c {
		n, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		c[i] = channel(float64(n))
	}
	return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
}

// contrast is the WCAG 2 contrast ratio of two colors.
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestContrastFormula(t *testing.T) {
	// Known values: black on white is 21, and a color on itself is 1.
	if got := contrast(t, "#000000", "#ffffff"); math.Abs(got-21) > 0.01 {
		t.Errorf("black on white = %v, want 21", got)
	}
	if got := contrast(t, "#596d86", "#596d86"); got != 1 {
		t.Errorf("same color = %v, want 1", got)
	}
	// #767676 on white is the textbook 4.54.
	if got := contrast(t, "#767676", "#ffffff"); math.Abs(got-4.54) > 0.01 {
		t.Errorf("#767676 on white = %v, want 4.54", got)
	}
}

func TestSchemesPassAA(t *testing.T) {
	schemes := parseSchemes(t)
	for _, name := range []string{"slate", "slate-deep", "slate-light"} {
		tokens, ok := schemes[name]
		if !ok {
			t.Fatalf("gobb.css has no rule for %s", name)
		}
		for _, tok := range schemeTokens {
			if tokens[tok] == "" {
				t.Errorf("%s lacks %s", name, tok)
			}
		}
		pairs := [][2]string{
			{"--text", "--page"}, {"--muted", "--page"},
			{"--text", "--row"}, {"--muted", "--row"},
			{"--link", "--page"}, {"--link", "--row"},
			{"--bar-text", "--bar"}, {"--header-text", "--header"},
		}
		for _, p := range pairs {
			fg, bg := tokens[p[0]], tokens[p[1]]
			if fg == "" || bg == "" {
				continue
			}
			if got := contrast(t, fg, bg); got < 4.5 {
				t.Errorf("%s: %s %s on %s %s = %.2f, want at least 4.5", name, p[0], fg, p[1], bg, got)
			}
		}
	}
	for tok, want := range slateSpec {
		if got := schemes["slate"][tok]; got != want {
			t.Errorf("slate %s = %q, spec says %q", tok, got, want)
		}
	}
	if got := schemes["slate-deep"]["--bar"]; got != "#2e425b" {
		t.Errorf("slate-deep --bar = %q, want #2e425b", got)
	}
	light := schemes["slate-light"]
	for tok, want := range map[string]string{"--row": "#ffffff", "--bar": "#2e425b", "--header": "#2e425b", "--accent": "#009aff"} {
		if light[tok] != want {
			t.Errorf("slate-light %s = %q, want %q", tok, light[tok], want)
		}
	}
}

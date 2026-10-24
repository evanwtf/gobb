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
	"--text", "--muted", "--link", "--accent", "--on-accent", "--new",
	"--badge", "--on-badge", "--mark", "--on-mark",
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
			{"--on-accent", "--accent"}, {"--on-badge", "--badge"}, {"--on-mark", "--mark"},
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

// mixHex mixes pct percent of fg into bg, as CSS color-mix(in srgb, fg pct%, bg)
// does, and returns #rrggbb.
func mixHex(t *testing.T, fg string, pct float64, bg string) string {
	t.Helper()
	parse := func(h string) [3]float64 {
		var c [3]float64
		for i := range c {
			n, err := strconv.ParseUint(h[1+2*i:3+2*i], 16, 8)
			if err != nil {
				t.Fatal(err)
			}
			c[i] = float64(n)
		}
		return c
	}
	f, b := parse(fg), parse(bg)
	out := "#"
	for i := range f {
		v := int(math.Round(f[i]*pct/100 + b[i]*(1-pct/100)))
		out += strconv.FormatUint(uint64(v)+0x100, 16)[1:]
	}
	return out
}

// distance is the straight distance of two #rrggbb colors in sRGB, 0 to 441.
func distance(t *testing.T, a, b string) float64 {
	t.Helper()
	var d float64
	for i := range 3 {
		x, _ := strconv.ParseUint(a[1+2*i:3+2*i], 16, 8)
		y, _ := strconv.ParseUint(b[1+2*i:3+2*i], 16, 8)
		d += (float64(x) - float64(y)) * (float64(x) - float64(y))
	}
	return math.Sqrt(d)
}

// The tint of an unread row and the tint of an announcement row differ clearly
// in every scheme, and the text on both still reaches AA.
func TestUnreadTintIsDistinct(t *testing.T) {
	css, err := staticFS.ReadFile("static/gobb.css")
	if err != nil {
		t.Fatal(err)
	}
	tint := func(selector string) (token string, pct float64) {
		t.Helper()
		re := regexp.MustCompile(regexp.QuoteMeta(selector) + `\s*\{\s*background:\s*color-mix\(in srgb,\s*var\((--[a-z-]+)\)\s+([0-9.]+)%,\s*var\(--row\)\)`)
		m := re.FindStringSubmatch(string(css))
		if m == nil {
			t.Fatalf("gobb.css has no tint rule for %q", selector)
		}
		pct, _ = strconv.ParseFloat(m[2], 64)
		return m[1], pct
	}
	unreadTok, unreadPct := tint(".tracks tr.row-new")
	annTok, annPct := tint(".topic-announce, .topic-global")
	schemes := parseSchemes(t)
	for _, name := range []string{"slate", "slate-deep", "slate-light"} {
		tok := schemes[name]
		unread := mixHex(t, tok[unreadTok], unreadPct, tok["--row"])
		ann := mixHex(t, tok[annTok], annPct, tok["--row"])
		if d := distance(t, unread, ann); d < 25 {
			t.Errorf("%s: unread tint %s and announcement tint %s are %.0f apart, want at least 25", name, unread, ann, d)
		}
		if d := distance(t, unread, tok["--row"]); d < 15 {
			t.Errorf("%s: unread tint %s is %.0f from the row color, want at least 15", name, unread, d)
		}
		for _, bg := range []string{unread, ann} {
			for _, fg := range []string{"--text", "--muted", "--link"} {
				if got := contrast(t, tok[fg], bg); got < 4.5 {
					t.Errorf("%s: %s on %s = %.2f, want at least 4.5", name, fg, bg, got)
				}
			}
		}
	}
}

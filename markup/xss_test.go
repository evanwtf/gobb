package markup_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

// payload is one line of the corpus, decoded.
type payload struct {
	line     int    // the line number in the file
	category string // the "%% category:" above it
	text     string // the decoded payload
}

var escape = regexp.MustCompile(`\{(?:U\+([0-9A-Fa-f]{4,6})|X:([0-9A-Fa-f]{2}))\}`)

// decodePayload turns the escapes {U+XXXX} and {X:hh} of a corpus line into the
// characters and bytes that they stand for.
func decodePayload(s string) (string, error) {
	var bad error
	out := escape.ReplaceAllStringFunc(s, func(m string) string {
		sub := escape.FindStringSubmatch(m)
		if sub[1] != "" {
			n, err := strconv.ParseUint(sub[1], 16, 32)
			if err != nil || n > utf8.MaxRune {
				bad = fmt.Errorf("bad code point in %s", m)
				return m
			}
			return string(rune(n))
		}
		n, _ := strconv.ParseUint(sub[2], 16, 8)
		return string([]byte{byte(n)})
	})
	return out, bad
}

// loadPayloads reads testdata/xss/payloads.txt.
func loadPayloads(t testing.TB) []payload {
	t.Helper()
	f, err := os.Open("testdata/xss/payloads.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	var out []payload
	category := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "%% category:"):
			category = strings.TrimSpace(strings.TrimPrefix(line, "%% category:"))
		case strings.HasPrefix(line, "%%"), line == "":
		default:
			text, err := decodePayload(line)
			if err != nil {
				t.Fatalf("payloads.txt:%d: %v", n, err)
			}
			out = append(out, payload{line: n, category: category, text: text})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// smileys is the smiley table that the tests render with.
func smileys() markup.RenderContext {
	return markup.RenderContext{Smiley: func(code string) (markup.Smiley, bool) {
		switch code {
		case ":)":
			return markup.Smiley{Code: code, Image: "/static/smilies/smile.gif", Alt: "smile"}, true
		case ":lol:":
			return markup.Smiley{Code: code, Image: "https://smilies.example/lol.gif", Alt: ""}, true
		}
		return markup.Smiley{}, false
	}}
}

var smileyList = []markup.Smiley{
	{Code: ":)", Image: "smile.gif", Alt: "smile"},
	{Code: ":(", Image: "sad.gif", Alt: "sad"},
	{Code: ":D", Image: "grin.gif", Alt: "grin"},
	{Code: ";)", Image: "wink.gif", Alt: "wink"},
	{Code: ":lol:", Image: "lol.gif", Alt: "lol"},
}

// flagMixes returns the eight mixes of the three flags.
func flagMixes() []markup.Options {
	var out []markup.Options
	for mask := 0; mask < 8; mask++ {
		out = append(out, markup.Options{
			Markdown: mask&1 != 0, Smilies: mask&2 != 0, Autolink: mask&4 != 0,
			SmileyList: smileyList,
		})
	}
	return out
}

// requiredCategories are the groups that the corpus must cover, with the fewest
// payloads that each must hold. They follow the plan.
var requiredCategories = map[string]int{
	"script tags":                         20,
	"event handlers":                      15,
	"javascript, vbscript and data links": 25,
	"encoded and spaced schemes":          30,
	"svg and mathml":                      15,
	"attribute breaks":                    20,
	"link titles":                         10,
	"reference links":                     10,
	"autolinks":                           20,
	"comments and cdata":                  15,
	"quotes and spoilers":                 20,
	"code spans and fences":               15,
	"embed links":                         30,
	"unicode tricks":                      30,
}

func TestPayloadCorpus(t *testing.T) {
	ps := loadPayloads(t)
	if len(ps) < 150 {
		t.Errorf("%d payloads, want at least 150", len(ps))
	}
	counts := map[string]int{}
	seen := map[string]int{}
	for _, p := range ps {
		counts[p.category]++
		if first, dup := seen[p.text]; dup {
			t.Errorf("payloads.txt:%d repeats the payload of line %d", p.line, first)
		}
		seen[p.text] = p.line
	}
	for cat, min := range requiredCategories {
		if counts[cat] < min {
			t.Errorf("category %q has %d payloads, want at least %d", cat, counts[cat], min)
		}
	}
	t.Logf("%d payloads: %v", len(ps), counts)

	// A hook that fixes white space would change a payload without a trace.
	raw, err := os.ReadFile("testdata/xss/payloads.txt")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.TrimRight(line, " \t\r") != line {
			t.Errorf("payloads.txt:%d ends with white space: write it as {U+0020}", i+1)
		}
	}
	if !strings.HasSuffix(string(raw), "\n") || strings.HasSuffix(string(raw), "\n\n") {
		t.Error("payloads.txt must end with exactly one newline")
	}
}

func TestDecodePayload(t *testing.T) {
	for in, want := range map[string]string{
		"plain":                     "plain",
		"a{U+0000}b":                "a\x00b",
		"{U+202E}x":                 "\u202ex",
		"{U+1F600}":                 "\U0001F600",
		"{X:ff}{X:00}":              "\xff\x00",
		"{U+000A}":                  "\n",
		`\{U+0041}`:                 `\A`,
		"{U+41}":                    "{U+41}",
		"{X:zz}":                    "{X:zz}",
		"[a](java{U+0009}script:x)": "[a](java\tscript:x)",
	} {
		got, err := decodePayload(in)
		if err != nil || got != want {
			t.Errorf("decodePayload(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	if _, err := decodePayload("{U+110000}"); err == nil {
		t.Error("decodePayload accepted a code point above U+10FFFF")
	}
}

// TestPayloadsRenderSafe parses every payload with every mix of the flags. The
// tree must pass Validate (or be refused by it, which Parse never lets happen).
// The tree renders, its stored JSON form renders the same, and the HTML passes
// the strict check, with and without a base path.
func TestPayloadsRenderSafe(t *testing.T) {
	ps := loadPayloads(t)
	var runs, links, images, frames, pres, quotes, spoilers int
	for _, p := range ps {
		t.Run(fmt.Sprintf("line %d", p.line), func(t *testing.T) {
			for _, opt := range flagMixes() {
				tree := markup.Parse(p.text, opt)
				if err := markup.Validate(tree); err != nil {
					t.Errorf("flags %+v: Parse gave a tree that Validate refuses: %v\npayload: %q", opt, err, p.text)
					continue
				}
				stored, err := json.Marshal(tree)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := markup.Decode(stored)
				if err != nil {
					t.Fatal(err)
				}
				for _, base := range []string{"", "/forum/"} {
					rc := smileys()
					rc.BasePath = base
					out := markup.Render(tree, rc)
					html := string(out.HTML)
					runs++
					htmlcheck.Check(t, html)
					if t.Failed() {
						t.Fatalf("flags %+v, base %q, payload %q", opt, base, p.text)
					}
					if again := string(markup.Render(decoded, rc).HTML); again != html {
						t.Errorf("flags %+v: the stored form renders differently\n got: %s\nwant: %s", opt, again, html)
					}
					if strings.ContainsAny(out.Text, "<>") {
						t.Errorf("flags %+v: Text has an angle bracket: %q", opt, out.Text)
					}
					if base == "" {
						links += strings.Count(html, "<a ")
						images += strings.Count(html, "<img ")
						frames += strings.Count(html, "<iframe ")
						pres += strings.Count(html, "<pre ")
						quotes += strings.Count(html, "<blockquote>")
						spoilers += strings.Count(html, `class="spoiler"`)
					}
				}
			}
		})
	}
	// A corpus whose payloads all become plain text would test nothing about the
	// link, image, embed, quote, spoiler and code paths. Require that some reach each.
	t.Logf("%d renders: %d links, %d images, %d frames, %d code blocks, %d quotes, %d spoilers",
		runs, links, images, frames, pres, quotes, spoilers)
	for name, n := range map[string]int{"links": links, "images": images, "frames": frames, "code blocks": pres, "quotes": quotes, "spoilers": spoilers} {
		if n < 5 {
			t.Errorf("only %d %s across the corpus: the payloads do not reach that path", n, name)
		}
	}
}

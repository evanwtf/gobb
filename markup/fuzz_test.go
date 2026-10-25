package markup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

// oddSmilies holds smiley codes that Validate would reject if they matched.
var oddSmilies = markup.Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: []markup.Smiley{
	{Code: ":\t)"}, {Code: "\xc3"}, {Code: ":\x01)"}, {Code: ":\u0085)"}, {Code: "\t"}, {Code: " "}, {Code: ":)"}, {Code: "x"},
}}

// addPayloads adds every payload of the corpus to the seeds of f.
func addPayloads(f *testing.F, asBytes bool) {
	for _, p := range loadPayloads(f) {
		if asBytes {
			f.Add([]byte(p.text))
		} else {
			f.Add(p.text)
		}
	}
}

// FuzzParse parses any text with several mixes of the flags. The tree must pass
// Validate, and its HTML must pass the strict check.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"", "a", "a\nb", "a\n\nb", "**b** *i* `c` ~~s~~", "# h\n---\n> q\n- l\n1. n",
		"||x||", "||a **b** [c](/d)||", "||a\n\nb||", "||||x||||", "|||a||",
		"[t](https://example.com)", "![a](/b.png)", "<https://example.com>", "<a@example.com>",
		"https://youtu.be/dQw4w9WgXcQ", "x\nhttps://x.com/a/status/1\ny", "www.example.com a@example.com",
		":) :( :D ;) :lol:", "a:)b :):)", "```go\nx\n```", "    code", "<script>x</script>",
		"[x](javascript:alert(1))", "![a](data:image/png;base64,x)", "\\*sigh\\*", "&amp; &#35; &bogus;",
		"a\xffb", "a\x00b", "a\r\nb\rc", "> > > > > > > > > > > > > > x", "- - - - - - - - - - - - - - - - - - - x",
		strings.Repeat("> ", 12) + "x", strings.Repeat("- ", 20) + "x", strings.Repeat("*a ", 40) + strings.Repeat("a* ", 40),
		"[a]: /b\n[a]", "\\\n", "  \n", "`` ` ``", "[[[[", "![![![", "<<<>>>", "||[x||](y)",
		"[https://example.com](https://example.org)", "<a&b@example.com>", "![a\fb](https://a.example/x.png)", "[x](https://a.example/\u200d)", "<https://a.example/\u200d>", "a&b@example.com", "[x](mailto:a?b@example.com)",
		"> " + strings.Repeat("- ", 16) + "x\n>" + strings.Repeat(" ", 33) + "y",
		"> " + strings.Repeat("1. ", 16) + "x\n>" + strings.Repeat(" ", 49) + "y",
		strings.Repeat("> - ", 11) + "x\n" + strings.Repeat(">   ", 11) + "  y",
		strings.Repeat("> 1. ", 14) + "x\n" + strings.Repeat(">    ", 14) + "    y",
		strings.Repeat("- > ", 12) + "x\n" + strings.Repeat("  ", 24) + "y\n\n" + strings.Repeat("> ", 30) + "z",
		"[x](/a?x=1&copy=2)", "![a](/a.png?x&times=1)", "https://example.com/?x=1&shy=1", "<https://example.com/?a=1&amp;b>",
		"<x\u202e@c.example>", "<x\\y@c.example>", ":\t)", "a\xffb", "[<https://a.b>](/c)", "![[a](/b)](/c)",
		// Mixed delimiters that never pair (A-I1): the shape that took 1.6 s at 60,000 bytes.
		strings.Repeat("*_a", 5000), strings.Repeat("_*a", 3000), strings.Repeat("||*_a", 2000), strings.Repeat("~a", 4000),
		// The code span ladder and the reference shapes (round 2).
		ladderSeed(60000), strings.Repeat("[x]:\n", 3000), strings.Repeat("[x]: /u 't'\n", 2000), strings.Repeat("[x]\n", 4000), strings.Repeat("![x][y]\n", 2000),
		// Definitions behind a tab or five spaces (round 3).
		"[x]: /u\n" + strings.Repeat("\t[x]: /u\n", 3000), "[x]:\n" + strings.Repeat("\t[x]:\n", 4000), "> [x]:\n" + strings.Repeat("\t[x]:\n", 4000),
		"[x]: /u\n" + strings.Repeat("     [x]: /u\n", 3000), "- [x]: /u\n" + strings.Repeat("        [x]: /u\n", 2500),
		"[a](" + strings.Repeat("[a](", 50), "[a](<" + strings.Repeat("[a](<", 50), "[a](b \"" + strings.Repeat("[a](b \"", 20),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	addPayloads(f, false)
	mixes := []markup.Options{
		{Markdown: true, Smilies: true, Autolink: true, SmileyList: smileyList},
		{Markdown: true},
		{},
		{Autolink: true, Smilies: true, SmileyList: smileyList},
		oddSmilies,
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, opt := range mixes {
			got := markup.Parse(s, opt)
			if err := markup.Validate(got); err != nil {
				t.Fatalf("invalid tree for %q with %+v: %v", s, opt, err)
			}
			htmlcheck.Check(t, string(markup.Render(got, smileys()).HTML))
		}
	})
}

// FuzzRender feeds the renderer two ways. Input that is a JSON tree goes
// through Decode, which can build a tree that Validate rejects. Every input
// also goes through Parse as Markdown source, with every flag on. In both,
// Render must not panic, and its HTML must pass the strict check: only the
// elements and attributes of the table, only URLs that pass the URL rules, only
// frames in the embed origins, and no script.
func FuzzRender(f *testing.F) {
	seeds := []string{
		`{"k":0,"c":[{"k":2,"n":"p","c":[{"k":1,"t":"hi"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"p","c":[{"k":2,"n":"url","a":{"href":"javascript:x"},"c":[{"k":1,"t":"x"}]}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"youtube","a":{"id":"\"><script>"}}]}`,
		`{"k":0,"c":[{"k":2,"n":"email","a":{"address":"a@b?c#d"}}]}`,
		`{"k":9,"c":[{"k":2,"n":"img","a":{"src":"&#106;avascript:x"}}]}`,
		`{"k":0,"c":[{"k":2,"n":"url","a":{"href":"mailto:a%40b@c.d&e=f"},"c":[{"k":1,"t":"x"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"h","a":{"level":"\"><script>"},"c":[{"k":1,"t":"x"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"pre","a":{"lang":"x\" onclick=\"y"},"c":[{"k":1,"t":"x"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"list","a":{"ordered":"true","start":"1\" onclick=\"y"},"c":[{"k":2,"n":"li"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"smiley","a":{"code":"\"><script>"}}]}`,
		`{"k":0,"c":[{"k":2,"n":"spoiler","a":{"onclick":"x"},"c":[{"k":1,"t":"x"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"script","c":[{"k":1,"t":"alert(1)"}]}]}`,
		`{"k":0,"c":[{"k":2,"n":"spotify","a":{"id":"track/../x"}},{"k":2,"n":"reddit","a":{"id":"a/b"}},{"k":2,"n":"threads","a":{"id":"u\"/p"}}]}`,
		"[x](mailto:a@b.c) <a@b.c> a@b.c",
		"![a](/p.png) ![b](https://example.com/i.png?a=1&b=2) :) :D",
		"<https://example.com/?a=1&copy=2> [x](https://example.com/&amp;)",
		"[x](javascript:alert(1)) [y](JaVaScRiPt&colon;x) ![z](data:text/html,<script>)",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ\n\nhttps://x.com/a/status/1\n\nhttps://open.spotify.com/track/abc",
		"<script>alert(1)</script> <SCRIPT src=x>",
		strings.Repeat("*_a", 5000),
		ladderSeed(30000), strings.Repeat("[x]:\n", 3000), strings.Repeat("[x]\n", 4000),
		"[x]: /u\n" + strings.Repeat("\t[x]: /u\n", 3000), "- [x]: /u\n" + strings.Repeat("        [x]: /u\n", 2500),
		"> **A** [wrote](/p/1):\n>\n> ||spoiler|| \\*x\\*\n\n```go\n<script>\n```",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	fixtures, _ := filepath.Glob(filepath.Join("testdata", "roundtrip", "*.md"))
	for _, name := range fixtures {
		if data, err := os.ReadFile(name); err == nil {
			f.Add(data)
		}
	}
	addPayloads(f, true)
	opt := markup.Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: smileyList}
	f.Fuzz(func(t *testing.T, data []byte) {
		if n, err := markup.Decode(data); err == nil {
			htmlcheck.Check(t, string(markup.Render(n, smileys()).HTML))
		}
		htmlcheck.Check(t, string(markup.Render(markup.Parse(string(data), opt), smileys()).HTML))
	})
}

// ladderSeed is runs of 1, 2, ... 200 backticks, each followed by a space, then a
// newline and lines of "a" to the size: the shape that took 147 ms and 679 MiB.
func ladderSeed(size int) string {
	var sb strings.Builder
	for k := 1; k <= 200; k++ {
		sb.WriteString(strings.Repeat("`", k))
		sb.WriteByte(' ')
	}
	sb.WriteByte('\n')
	return sb.String() + strings.Repeat("a\n", max(0, size-sb.Len())/2)
}

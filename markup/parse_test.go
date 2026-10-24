package markup

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var smileyList = []Smiley{
	{Code: ":)", Image: "smile.gif", Alt: "smile"},
	{Code: ":(", Image: "sad.gif", Alt: "sad"},
	{Code: ":D", Image: "grin.gif", Alt: "grin"},
	{Code: ";)", Image: "wink.gif", Alt: "wink"},
	{Code: ":lol:", Image: "lol.gif", Alt: "lol"},
}

// allOn turns every flag on, with a small smiley list.
var allOn = Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: smileyList}

var mdOnly = Options{Markdown: true}

func js(n *Node) string {
	b, err := json.Marshal(n)
	if err != nil {
		return err.Error()
	}
	return string(b)
}

// check parses src, requires a valid tree, and compares it with want.
func check(t *testing.T, src string, opt Options, want *Node) {
	t.Helper()
	got := Parse(src, opt)
	if err := Validate(got); err != nil {
		t.Errorf("Parse(%q) gives an invalid tree: %v\n%s", src, err, js(got))
		return
	}
	if js(got) != js(want) {
		t.Errorf("Parse(%q)\n got: %s\nwant: %s", src, js(got), js(want))
	}
}

// has reports whether the tree holds an element called name.
func has(n *Node, name string) bool {
	if n.Kind == Element && n.Name == name {
		return true
	}
	return slices.ContainsFunc(n.Children, func(c *Node) bool { return has(c, name) })
}

// allText joins every text node of the tree.
func allText(n *Node) string {
	var sb strings.Builder
	var walk func(*Node)
	walk = func(n *Node) {
		if n.Kind == Text {
			sb.WriteString(n.Text)
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// unmarked removes the quote and list markers that stay in the text when the
// nesting passes the cap.
func unmarked(s string) string { return strings.Trim(s, "-> \n") }

func p(children ...*Node) *Node { return el("p", nil, children...) }
func br() *Node                 { return el("br", nil) }

func TestParseBasic(t *testing.T) {
	const (
		yt  = "dQw4w9WgXcQ"
		pic = "https://example.com/i.png"
	)
	cases := []struct {
		name string
		src  string
		want *Node
	}{
		{"p", "hello", tree(p(txt("hello")))},
		{"two p", "a\n\nb", tree(p(txt("a")), p(txt("b")))},
		{"h1", "# Title", tree(el("h", at("level", "1"), txt("Title")))},
		{"h6", "###### Small", tree(el("h", at("level", "6"), txt("Small")))},
		{"h setext", "Foo\n---", tree(el("h", at("level", "2"), txt("Foo")))},
		{"h inline", "## a **b**", tree(el("h", at("level", "2"), txt("a "), el("b", nil, txt("b"))))},
		{"hr", "---", tree(el("hr", nil))},
		{"quote", "> q", tree(el("quote", nil, p(txt("q"))))},
		{"quote nested", "> a\n>> b", tree(el("quote", nil, p(txt("a")), el("quote", nil, p(txt("b")))))},
		{"empty quote", ">", tree(el("quote", nil))},
		{"bullets tight", "- a\n- b", tree(el("list", at("tight", "true"),
			el("li", nil, p(txt("a"))), el("li", nil, p(txt("b")))))},
		{"bullets loose", "- a\n\n- b", tree(el("list", nil,
			el("li", nil, p(txt("a"))), el("li", nil, p(txt("b")))))},
		{"star and plus", "* a\n\n+ b", tree(
			el("list", at("tight", "true"), el("li", nil, p(txt("a")))),
			el("list", at("tight", "true"), el("li", nil, p(txt("b")))))},
		{"ordered", "1. a\n2. b", tree(el("list", at("ordered", "true", "tight", "true"),
			el("li", nil, p(txt("a"))), el("li", nil, p(txt("b")))))},
		{"ordered start", "3. a", tree(el("list", at("ordered", "true", "start", "3", "tight", "true"),
			el("li", nil, p(txt("a")))))},
		{"ordered start zero", "0. a", tree(el("list", at("ordered", "true", "tight", "true"),
			el("li", nil, p(txt("a")))))},
		{"nested list", "- a\n  - b", tree(el("list", at("tight", "true"),
			el("li", nil, p(txt("a")), el("list", at("tight", "true"), el("li", nil, p(txt("b")))))))},
		{"empty li", "-", tree(el("list", at("tight", "true"), el("li", nil)))},
		{"pre lang", "```go\nx := 1\ny := 2\n```", tree(el("pre", at("lang", "go"), txt("x := 1\ny := 2")))},
		{"pre lang first word", "```c++ extra words\nx\n```", tree(el("pre", at("lang", "c++"), txt("x")))},
		{"pre no lang", "```\nx\n```", tree(el("pre", nil, txt("x")))},
		{"pre bad lang", "```{.x}\nx\n```", tree(el("pre", nil, txt("x")))},
		{"pre empty", "```\n```", tree(el("pre", nil))},
		{"pre tilde", "~~~\n<b>&amp;\n~~~", tree(el("pre", nil, txt("<b>&amp;")))},
		{"pre indented", "    a\n    b", tree(el("pre", nil, txt("a\nb")))},
		{"code", "`x := 1`", tree(p(el("code", nil, txt("x := 1"))))},
		{"code space trim", "`` a ` b ``", tree(p(el("code", nil, txt("a ` b"))))},
		{"code newline", "`a\nb`", tree(p(el("code", nil, txt("a b"))))},
		{"bold italic", "**b** *i* __b__ _i_", tree(p(
			el("b", nil, txt("b")), txt(" "), el("i", nil, txt("i")), txt(" "),
			el("b", nil, txt("b")), txt(" "), el("i", nil, txt("i"))))},
		{"bold italic nest", "***x***", tree(p(el("i", nil, el("b", nil, txt("x")))))},
		{"url", "[t](https://example.com/a?b=c)", tree(p(el("url", at("href", "https://example.com/a?b=c"), txt("t"))))},
		{"url site path", "[t](/t/5)", tree(p(el("url", at("href", "/t/5"), txt("t"))))},
		{"url mailto", "[m](mailto:a@example.com)", tree(p(el("url", at("href", "mailto:a@example.com"), txt("m"))))},
		{"url title dropped", `[t](https://example.com "title")`, tree(p(el("url", at("href", "https://example.com"), txt("t"))))},
		{"url long text", "[" + strings.Repeat("a", 80) + "](https://example.com)",
			tree(p(el("url", at("href", "https://example.com"), txt(strings.Repeat("a", 80)))))},
		{"url reference", "[t][r]\n\n[r]: https://example.com", tree(p(el("url", at("href", "https://example.com"), txt("t"))))},
		{"url markup text", "[**t**](/x)", tree(p(el("url", at("href", "/x"), el("b", nil, txt("t")))))},
		{"url empty text", "[](https://example.com)", tree(p(el("url", at("href", "https://example.com"), txt("https://example.com"))))},
		{"autolink angle", "<https://example.com/x>", tree(p(el("url", at("href", "https://example.com/x"), txt("https://example.com/x"))))},
		{"email angle", "<a@example.com>", tree(p(el("email", at("address", "a@example.com"), txt("a@example.com"))))},
		{"img", "![alt text](" + pic + ")", tree(p(el("img", at("src", pic, "alt", "alt text"))))},
		{"img site path", "![](/a/1.png)", tree(p(el("img", at("src", "/a/1.png"))))},
		{"img title dropped", `![a](` + pic + ` "t")`, tree(p(el("img", at("src", pic, "alt", "a"))))},
		{"img alt markup", "![a *b* c](" + pic + ")", tree(p(el("img", at("src", pic, "alt", "a b c"))))},
		{"img in url", "[![a](" + pic + ")](/x)", tree(p(el("url", at("href", "/x"), el("img", at("src", pic, "alt", "a")))))},
		{"spoiler", "||x||", tree(p(el("spoiler", nil, txt("x"))))},
		{"smiley", ":)", tree(p(el("smiley", at("code", ":)"))))},
		{"embed youtube", "https://youtu.be/" + yt, tree(el("youtube", at("id", yt)))},
		{"br hard", "a  \nb", tree(p(txt("a"), br(), txt("b")))},
		{"br backslash", "a\\\nb", tree(p(txt("a"), br(), txt("b")))},
		{"empty", "", tree()},
		{"blank", " \n\n  \n", tree()},
		{"link ref def only", "[r]: https://example.com", tree()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { check(t, c.src, allOn, c.want) })
	}
}

func TestParseLineBreaks(t *testing.T) {
	check(t, "a\nb", mdOnly, tree(p(txt("a"), br(), txt("b"))))
	check(t, "a\n\nb", mdOnly, tree(p(txt("a")), p(txt("b"))))
	check(t, "a \nb", mdOnly, tree(p(txt("a"), br(), txt("b"))))
	check(t, "a\r\nb\r\n\r\nc\rd", mdOnly, tree(p(txt("a"), br(), txt("b")), p(txt("c"), br(), txt("d"))))
	check(t, "- a\n  b", mdOnly, tree(el("list", at("tight", "true"),
		el("li", nil, p(txt("a"), br(), txt("b"))))))
}

func TestParseEscapes(t *testing.T) {
	check(t, `\*sigh\*`, mdOnly, tree(p(txt("*sigh*"))))
	check(t, `a \_b\_ \[c\] \# \\ \`+"`d`", mdOnly, tree(p(txt("a _b_ [c] # \\ `d`"))))
	check(t, `\a`, mdOnly, tree(p(txt(`\a`))))
	check(t, "&amp; &lt;b&gt; &copy; &#35; &#x41;", mdOnly, tree(p(txt("& <b> © # A"))))
	check(t, `\&amp;`, mdOnly, tree(p(txt("&amp;"))))
	check(t, "AT&T &nosuchentity;", mdOnly, tree(p(txt("AT&T &nosuchentity;"))))
	check(t, `\|\|x\|\|`, mdOnly, tree(p(txt("||x||"))))
	check(t, "`\\*` and ```&amp;```", mdOnly, tree(p(el("code", nil, txt(`\*`)), txt(" and "), el("code", nil, txt("&amp;")))))
	check(t, `[a\]b](/x)`, mdOnly, tree(p(el("url", at("href", "/x"), txt("a]b")))))
}

func TestParseInput(t *testing.T) {
	check(t, "a\xffb", mdOnly, tree(p(txt("a�b"))))
	check(t, "a\xff\xfe\xfdb", mdOnly, tree(p(txt("a�b"))))
	check(t, "a\x00b", mdOnly, tree(p(txt("a�b"))))
	check(t, "\xff", Options{}, tree(p(txt("�"))))
}

func TestParseSpoiler(t *testing.T) {
	check(t, "||x||", mdOnly, tree(p(el("spoiler", nil, txt("x")))))
	check(t, "||x", mdOnly, tree(p(txt("||x"))))
	check(t, "x||", mdOnly, tree(p(txt("x||"))))
	check(t, "||a **b**||", mdOnly, tree(p(el("spoiler", nil, txt("a "), el("b", nil, txt("b"))))))
	check(t, "||a\nb||", mdOnly, tree(p(el("spoiler", nil, txt("a"), br(), txt("b")))))
	check(t, "||a\n\nb||", mdOnly, tree(p(txt("||a")), p(txt("b||"))))
	check(t, "a ||x|| b", mdOnly, tree(p(txt("a "), el("spoiler", nil, txt("x")), txt(" b"))))
	check(t, `\|\|x\|\|`, mdOnly, tree(p(txt("||x||"))))
	check(t, "|| x ||", mdOnly, tree(p(txt("|| x ||"))))
	check(t, "`||x||`", mdOnly, tree(p(el("code", nil, txt("||x||")))))
	check(t, "[||x||](/y)", mdOnly, tree(p(el("url", at("href", "/y"), el("spoiler", nil, txt("x"))))))
	check(t, "||[x](/y)||", mdOnly, tree(p(el("spoiler", nil, el("url", at("href", "/y"), txt("x"))))))
	check(t, "||![a](/p.png)||", mdOnly, tree(p(el("spoiler", nil, el("img", at("src", "/p.png", "alt", "a"))))))
	check(t, "||a ||b|| c||", mdOnly, tree(p(el("spoiler", nil, txt("a b c")))))
	check(t, "||||x||||", mdOnly, tree(p(el("spoiler", nil, txt("x")))))
	check(t, "||a|| ||b||", mdOnly, tree(p(el("spoiler", nil, txt("a")), txt(" "), el("spoiler", nil, txt("b")))))
	check(t, "> ||a\n> b||", mdOnly, tree(el("quote", nil, p(el("spoiler", nil, txt("a"), br(), txt("b"))))))
	// A spoiler holds links, also a bare one at its start.
	check(t, "||see https://example.com||", allOn, tree(p(el("spoiler", nil, txt("see "),
		el("url", at("href", "https://example.com"), txt("https://example.com"))))))
	check(t, "||https://example.com||", allOn, tree(p(el("spoiler", nil,
		el("url", at("href", "https://example.com"), txt("https://example.com"))))))
	check(t, "||a :) b||", allOn, tree(p(el("spoiler", nil, txt("a "), el("smiley", at("code", ":)")), txt(" b")))))
}

func TestParseEmbeds(t *testing.T) {
	embeds := []struct{ tag, url, id string }{
		{"youtube", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"youtube", "https://youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"vimeo", "https://vimeo.com/76979871", "76979871"},
		{"tweet", "https://x.com/someone/status/1234567890", "1234567890"},
		{"tweet", "https://twitter.com/someone/status/1234567890", "1234567890"},
		{"instagram", "https://www.instagram.com/p/CxYz123AbC/", "CxYz123AbC"},
		{"reddit", "https://www.reddit.com/r/golang/comments/abc123/some_title/", "golang/abc123"},
		{"tiktok", "https://www.tiktok.com/@someone/video/7234567890123456789", "7234567890123456789"},
		{"threads", "https://www.threads.net/@someone/post/Cabc123", "someone/Cabc123"},
		{"spotify", "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC", "track/4uLU6hMCjMI75M1A2tKUQC"},
		{"soundcloud", "https://soundcloud.com/artist/track-name", "artist/track-name"},
	}
	for _, e := range embeds {
		t.Run(e.tag+" "+e.url, func(t *testing.T) {
			emb := el(e.tag, at("id", e.id))
			link := bareLink(e.url)
			check(t, e.url, allOn, tree(emb))
			check(t, "<"+e.url+">", allOn, tree(emb))
			check(t, "  "+e.url+"  ", allOn, tree(emb))
			check(t, "x\n"+e.url+"\ny", allOn, tree(p(txt("x")), emb, p(txt("y"))))
			check(t, "x\n"+e.url, allOn, tree(p(txt("x")), emb))
			check(t, e.url+"\ny", allOn, tree(emb, p(txt("y"))))
			check(t, e.url+"\n"+e.url, allOn, tree(emb, emb))
			check(t, "see "+e.url+" now", allOn, tree(p(txt("see "), link, txt(" now"))))
			check(t, "see "+e.url, allOn, tree(p(txt("see "), link)))
			check(t, "["+"clip"+"]("+e.url+")", allOn, tree(p(el("url", at("href", e.url), txt("clip")))))
			check(t, "||"+e.url+"||", allOn, tree(p(el("spoiler", nil, link))))
			check(t, "**"+e.url+"**", allOn, tree(p(el("b", nil, link))))
			check(t, "# "+e.url, allOn, tree(el("h", at("level", "1"), link)))
			check(t, "> a\n> "+e.url+"\n> b", allOn, tree(el("quote", nil, p(txt("a")), emb, p(txt("b")))))
			check(t, "- "+e.url, allOn, tree(el("list", at("tight", "true"), el("li", nil, emb))))
			check(t, "- a\n  "+e.url, allOn, tree(el("list", at("tight", "true"), el("li", nil, p(txt("a")), emb))))
			check(t, "- a\n\n  "+e.url, allOn, tree(el("list", nil, el("li", nil, p(txt("a")), emb))))
			// Autolink off: no bare link and no embed. An angle link stays a link.
			noAuto := Options{Markdown: true, Smilies: true, SmileyList: smileyList}
			check(t, e.url, noAuto, tree(p(txt(e.url))))
			check(t, "<"+e.url+">", noAuto, tree(p(link)))
			// Markdown off: a bare link alone on its line is still an embed.
			check(t, "x\n"+e.url+"\ny", Options{Autolink: true}, tree(p(txt("x")), emb, p(txt("y"))))
		})
	}
	redd := "https://redd.it/abc123"
	check(t, redd, allOn, tree(p(el("url", at("href", redd), txt(redd)))))
	// Two links on one line stay links.
	a, b := "https://youtu.be/dQw4w9WgXcQ", "https://vimeo.com/76979871"
	check(t, a+" "+b, allOn, tree(p(el("url", at("href", a), txt(a)), txt(" "), el("url", at("href", b), txt(b)))))
	// A site that EmbedID does not know stays a link.
	check(t, "https://example.com/watch", allOn, tree(p(el("url", at("href", "https://example.com/watch"), txt("https://example.com/watch")))))
}

// bareLink is the url element for a bare link with the given address.
func bareLink(u string) *Node {
	attrs := at("href", u)
	if d, ok := displayText(u); ok {
		attrs["display"] = d
	}
	return el("url", attrs, txt(u))
}

func TestEmbedTagsMatchTable(t *testing.T) {
	var want []string
	for name := range embedRules {
		want = append(want, name)
	}
	got := slices.Clone(embedTags)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("embedTags = %v, embed table = %v", got, want)
	}
}

func TestParseRawHTML(t *testing.T) {
	check(t, "<script>x</script>", mdOnly, tree(p(txt("<script>x</script>"))))
	check(t, "a <b>x</b> c", mdOnly, tree(p(txt("a <b>x</b> c"))))
	check(t, "<div>\n*x*\n</div>", mdOnly, tree(p(txt("<div>"), br(), txt("*x*"), br(), txt("</div>"))))
	check(t, "<!-- c -->", mdOnly, tree(p(txt("<!-- c -->"))))
	check(t, `<img src="x" onerror="y">`, mdOnly, tree(p(txt(`<img src="x" onerror="y">`))))
	check(t, "> <script>x</script>", mdOnly, tree(el("quote", nil, p(txt("<script>x</script>")))))
	check(t, "- <script>x</script>", mdOnly, tree(el("list", at("tight", "true"), el("li", nil, p(txt("<script>x</script>"))))))
	check(t, "a < b > c", mdOnly, tree(p(txt("a < b > c"))))
}

func TestParseDangerousLinks(t *testing.T) {
	cases := []struct{ src, text string }{
		{"[x](javascript:alert(1))", "x"},
		{"[x](JaVaScRiPt:x)", "x"},
		{"[x](data:text/html,x)", "x"},
		{"![a](data:image/png;base64,x)", "a"},
		{"<javascript:x>", "javascript:x"},
		{"[x](jav&#x09;ascript:alert(1))", "x"},
		{"[x](&#106;avascript:alert(1))", "x"},
		{"[x](vbscript:x)", "x"},
		{"[x](file:///etc/passwd)", "x"},
		{"[x](//evil.example/)", "x"},
		{"[x](ftp://example.com/)", "x"},
		{"![a](mailto:a@example.com)", "a"},
		{"![a](javascript:x)", "a"},
		{"[x](<java script:x>)", "x"},
		{"[![a](javascript:x)](javascript:y)", "a"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			check(t, c.src, allOn, tree(p(txt(c.text))))
			got := Parse(c.src, allOn)
			if has(got, "url") || has(got, "img") {
				t.Errorf("tree has url or img: %s", js(got))
			}
		})
	}
	// A bare ftp link is text.
	check(t, "ftp://example.com/x", allOn, tree(p(txt("ftp://example.com/x"))))
	// An unresolved reference is text.
	check(t, "[x][nope]", allOn, tree(p(txt("[x][nope]"))))
	// A bad link keeps its markup.
	check(t, "[**x**](javascript:y)", allOn, tree(p(el("b", nil, txt("x")))))
}

func TestParseFlagsOff(t *testing.T) {
	plain := Options{}
	check(t, "**x**", plain, tree(p(txt("**x**"))))
	check(t, "# t\n- a\n> q\n`c`\n||s||", plain, tree(p(
		txt("# t"), br(), txt("- a"), br(), txt("> q"), br(), txt("`c`"), br(), txt("||s||"))))
	check(t, "a\n\nb", plain, tree(p(txt("a")), p(txt("b"))))
	check(t, "a\nb", plain, tree(p(txt("a"), br(), txt("b"))))
	check(t, "a\n\n\n\nb", plain, tree(p(txt("a")), p(txt("b"))))
	check(t, "a\\\nb", plain, tree(p(txt("a\\"), br(), txt("b"))))
	check(t, "<b>&amp;\\*</b>", plain, tree(p(txt("<b>&amp;\\*</b>"))))
	check(t, "[t](https://example.com)", plain, tree(p(txt("[t](https://example.com)"))))
	check(t, "<https://example.com>", plain, tree(p(txt("<https://example.com>"))))
	check(t, "1. a", plain, tree(p(txt("1. a"))))
	check(t, "    code", plain, tree(p(txt("code"))))
	check(t, "", plain, tree())

	// Markdown off: the other flags still apply.
	check(t, "see https://example.com now :)", Options{Autolink: true, Smilies: true, SmileyList: smileyList},
		tree(p(txt("see "), el("url", at("href", "https://example.com"), txt("https://example.com")),
			txt(" now "), el("smiley", at("code", ":)")))))
	check(t, "a **:)** b", Options{Smilies: true, SmileyList: smileyList},
		tree(p(txt("a **"), el("smiley", at("code", ":)")), txt("** b"))))

	// Smilies off: no smiley, whatever the list holds.
	check(t, ":)", Options{Markdown: true, Autolink: true, SmileyList: smileyList}, tree(p(txt(":)"))))
	// Smilies on with no list: nothing to match.
	check(t, ":)", Options{Markdown: true, Smilies: true}, tree(p(txt(":)"))))

	// Autolink off: no bare link, no www link, no bare email, no embed.
	noAuto := Options{Markdown: true}
	for _, s := range []string{"https://example.com", "http://example.com/x", "www.example.com", "a@example.com", "https://youtu.be/dQw4w9WgXcQ"} {
		check(t, s, noAuto, tree(p(txt(s))))
		check(t, "see "+s+" now", noAuto, tree(p(txt("see "+s+" now"))))
	}
	// A Markdown link still works with Autolink off.
	check(t, "[t](https://example.com)", noAuto, tree(p(el("url", at("href", "https://example.com"), txt("t")))))
	check(t, "x\nhttps://youtu.be/dQw4w9WgXcQ\ny", Options{}, tree(p(
		txt("x"), br(), txt("https://youtu.be/dQw4w9WgXcQ"), br(), txt("y"))))
}

func TestParseSmilies(t *testing.T) {
	smile := el("smiley", at("code", ":)"))
	check(t, ":)", allOn, tree(p(smile)))
	check(t, "`:)`", allOn, tree(p(el("code", nil, txt(":)")))))
	check(t, "a:)b", allOn, tree(p(txt("a:)b"))))
	check(t, "a:)", allOn, tree(p(txt("a:)"))))
	check(t, ":)b", allOn, tree(p(txt(":)b"))))
	check(t, ":):)", allOn, tree(p(smile, smile)))
	check(t, "hi :) there", allOn, tree(p(txt("hi "), smile, txt(" there"))))
	check(t, "hi, :), x", allOn, tree(p(txt("hi, "), smile, txt(", x"))))
	check(t, "(:)", allOn, tree(p(txt("("), smile)))
	check(t, "1:)", allOn, tree(p(txt("1:)"))))
	check(t, "a :lol: b", allOn, tree(p(txt("a "), el("smiley", at("code", ":lol:")), txt(" b"))))
	check(t, ":D ;) :(", allOn, tree(p(el("smiley", at("code", ":D")), txt(" "),
		el("smiley", at("code", ";)")), txt(" "), el("smiley", at("code", ":(")))))
	check(t, "**:)** *a :)*", allOn, tree(p(el("b", nil, smile), txt(" "), el("i", nil, txt("a "), smile))))
	check(t, "# :)", allOn, tree(el("h", at("level", "1"), smile)))
	check(t, "- :)", allOn, tree(el("list", at("tight", "true"), el("li", nil, p(smile)))))
	check(t, "a\n:)", allOn, tree(p(txt("a"), br(), smile)))
	// No smiley in code, pre, url, email or an image's alt text.
	check(t, "```\n:)\n```", allOn, tree(el("pre", nil, txt(":)"))))
	check(t, "    :)", allOn, tree(el("pre", nil, txt(":)"))))
	check(t, "[:)](https://example.com)", allOn, tree(p(el("url", at("href", "https://example.com"), txt(":)")))))
	check(t, "![:)](/a.png)", allOn, tree(p(el("img", at("src", "/a.png", "alt", ":)")))))
	check(t, "<a@example.com> :)", allOn, tree(p(el("email", at("address", "a@example.com"), txt("a@example.com")), txt(" "), smile)))
	// The longest code wins.
	long := Options{Markdown: true, Smilies: true, SmileyList: []Smiley{{Code: ":)"}, {Code: ":))"}, {Code: ":)))"}}}
	check(t, ":))", long, tree(p(el("smiley", at("code", ":))")))))
	check(t, ":)))", long, tree(p(el("smiley", at("code", ":)))")))))
	check(t, ":) :))", long, tree(p(smile, txt(" "), el("smiley", at("code", ":))")))))
	// An empty code or a code over 32 characters never matches.
	odd := Options{Markdown: true, Smilies: true, SmileyList: []Smiley{{Code: ""}, {Code: strings.Repeat("a", 33)}}}
	check(t, "x "+strings.Repeat("a", 33)+" y", odd, tree(p(txt("x "+strings.Repeat("a", 33)+" y"))))
	// A word code needs word boundaries too.
	word := Options{Markdown: true, Smilies: true, SmileyList: []Smiley{{Code: "lol"}}}
	check(t, "lol lolly lol", word, tree(p(el("smiley", at("code", "lol")), txt(" lolly "), el("smiley", at("code", "lol")))))
}

func TestParseAutolink(t *testing.T) {
	check(t, "see https://example.com/x now", allOn, tree(p(txt("see "),
		el("url", at("href", "https://example.com/x"), txt("https://example.com/x")), txt(" now"))))
	check(t, "(https://example.com/x)", allOn, tree(p(txt("("),
		el("url", at("href", "https://example.com/x"), txt("https://example.com/x")), txt(")"))))
	check(t, "see https://example.com/x.", allOn, tree(p(txt("see "),
		el("url", at("href", "https://example.com/x"), txt("https://example.com/x")), txt("."))))

	long := "https://example.com/" + strings.Repeat("a", 50)
	if len(long) != 70 {
		t.Fatalf("test URL is %d characters", len(long))
	}
	display := long[:39] + " ... " + long[len(long)-10:]
	check(t, "x "+long+" y", allOn, tree(p(txt("x "), el("url", at("href", long, "display", display), txt(long)), txt(" y"))))
	check(t, "<"+long+">", allOn, tree(p(el("url", at("href", long, "display", display), txt(long)))))
	// Exactly 55 characters has no display; 56 has.
	u55 := "https://example.com/" + strings.Repeat("b", 35)
	u56 := u55 + "c"
	check(t, "x "+u55, allOn, tree(p(txt("x "), el("url", at("href", u55), txt(u55)))))
	d56 := u56[:39] + " ... " + u56[len(u56)-10:]
	check(t, "x "+u56, allOn, tree(p(txt("x "), el("url", at("href", u56, "display", d56), txt(u56)))))
	// Markdown links never get display.
	check(t, "["+long+"]("+long+")", allOn, tree(p(el("url", at("href", long), txt(long)))))
	check(t, "[x]("+long+")", allOn, tree(p(el("url", at("href", long), txt("x")))))
	// The display counts characters, not bytes.
	uni := "https://example.com/" + strings.Repeat("é", 40)
	got := Parse("x <"+uni+">", allOn)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	url := got.Children[0].Children[1]
	if d := url.Attrs["display"]; !utf8.ValidString(d) || utf8.RuneCountInString(d) != 54 {
		t.Errorf("display = %q (%d runes)", d, utf8.RuneCountInString(d))
	}

	check(t, "www.a.example", allOn, tree(p(el("url", at("href", "http://www.a.example"), txt("www.a.example")))))
	check(t, "go to www.a.example/x?y=1 now", allOn, tree(p(txt("go to "),
		el("url", at("href", "http://www.a.example/x?y=1"), txt("www.a.example/x?y=1")), txt(" now"))))
	check(t, "mail a@example.com now", allOn, tree(p(txt("mail "),
		el("email", at("address", "a@example.com"), txt("a@example.com")), txt(" now"))))
	// A link in a link label stays one link.
	check(t, "[see https://example.com](/x)", allOn, tree(p(el("url", at("href", "/x"), txt("see https://example.com")))))
	// A link in code stays code.
	check(t, "`https://example.com`", allOn, tree(p(el("code", nil, txt("https://example.com")))))
	check(t, "```\nhttps://example.com\n```", allOn, tree(el("pre", nil, txt("https://example.com"))))
	// A link with a non-ASCII or odd character is escaped as CommonMark says.
	check(t, "<https://example.com/a%20b/é>", allOn, tree(p(el("url",
		at("href", "https://example.com/a%20b/%C3%A9"), txt("https://example.com/a%20b/é")))))
}

func TestParseNestingCaps(t *testing.T) {
	limit := 200 * time.Millisecond * raceSlowdown
	type capCase struct {
		name string
		src  string
		ok   func(t *testing.T, got *Node)
	}
	cases := []capCase{
		{"10000 nested quotes", strings.Repeat(">", 10000) + " deep", func(t *testing.T, got *Node) {
			if q := maxQuotes(got); q != MaxQuoteDepth {
				t.Errorf("quote depth = %d, want %d", q, MaxQuoteDepth)
			}
			if unmarked(allText(got)) != "deep" {
				t.Errorf("text = %q", allText(got))
			}
		}},
		{"12 nested quotes", strings.Repeat("> ", 12) + "deep", func(t *testing.T, got *Node) {
			if q := maxQuotes(got); q != MaxQuoteDepth {
				t.Errorf("quote depth = %d, want %d", q, MaxQuoteDepth)
			}
			if unmarked(allText(got)) != "deep" {
				t.Errorf("text = %q", allText(got))
			}
		}},
		{"10 nested quotes", strings.Repeat("> ", 10) + "deep", func(t *testing.T, got *Node) {
			if q := maxQuotes(got); q != 10 {
				t.Errorf("quote depth = %d, want 10", q)
			}
		}},
		{"50000 open bold", strings.Repeat("**", 50000), nil},
		{"50000 open bold words", strings.Repeat("**a ", 20000), nil},
		{"50000 open italic", strings.Repeat("*", 50000) + "a", nil},
		{"50000 open spoiler", strings.Repeat("||", 25000), nil},
		{"25000 open spoiler words", strings.Repeat("||a ", 20000), nil},
		{"10000 open brackets", strings.Repeat("[", 10000), nil},
		{"10000 brackets and parens", strings.Repeat("[a](", 5000), nil},
		{"10000 open images", strings.Repeat("![", 10000), nil},
		{"40000 nested quotes", strings.Repeat(">", 40000) + " deep", nil},
		{"40000 nested list marks", strings.Repeat("- ", 40000) + "deep", nil},
		{"40000 quote and list marks", strings.Repeat("> - ", 40000) + "deep", nil},
		{"10000 nested lists", strings.Repeat("- ", 10000) + "deep", func(t *testing.T, got *Node) {
			if unmarked(allText(got)) != "deep" {
				t.Errorf("text = %q", allText(got))
			}
		}},
		{"10000 nested ordered lists", strings.Repeat("1. ", 10000) + "deep", nil},
		{"nested list lines", func() string {
			var sb strings.Builder
			for i := 0; i < 300; i++ {
				sb.WriteString(strings.Repeat("  ", i) + "- x\n")
			}
			return sb.String()
		}(), nil},
		{"nested quote lines", func() string {
			var sb strings.Builder
			for i := 0; i < 300; i++ {
				sb.WriteString(strings.Repeat("> ", i+1) + "x\n")
			}
			return sb.String()
		}(), nil},
		{"nested emphasis", strings.Repeat("*a ", 5000) + strings.Repeat("a* ", 5000), nil},
		{"nested emphasis in quote", strings.Repeat("> ", 9) + strings.Repeat("*a ", 5000) + strings.Repeat("a* ", 5000), nil},
		{"nested spoiler and emphasis", strings.Repeat("||*a ", 3000) + strings.Repeat("a*|| ", 3000), nil},
		{"mixed list and quote", strings.Repeat("> - ", 5000) + "x", nil},
		{"10000 backtick runs", strings.Repeat("` ``", 2500), nil},
		{"many backticks growing", func() string {
			var sb strings.Builder
			for i := 1; i < 140; i++ {
				sb.WriteString(strings.Repeat("`", i) + " ")
			}
			return sb.String()
		}(), nil},
		{"10000 angle brackets", strings.Repeat("<", 10000), nil},
		{"10000 open links with text", strings.Repeat("[a ", 10000), nil},
		{"many bare links", strings.Repeat("https://example.com/x ", 5000), nil},
		{"many embed lines", strings.Repeat("https://youtu.be/dQw4w9WgXcQ\n", 5000), nil},
		{"many smilies", strings.Repeat(":) ", 20000), nil},
		{"long line", strings.Repeat("a", 1<<20), nil},
		{"many paragraphs", strings.Repeat("a\n\n", 50000), nil},
		{"many headings", strings.Repeat("# a\n", 50000), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			got := Parse(c.src, allOn)
			took := time.Since(start)
			t.Logf("%d bytes in %v", len(c.src), took)
			if err := Validate(got); err != nil {
				t.Fatalf("invalid tree: %v", err)
			}
			if took > limit {
				t.Errorf("took %v, limit %v", took, limit)
			}
			if c.ok != nil {
				c.ok(t, got)
			}
		})
	}
}

// maxQuotes returns the most quote elements on one path.
func maxQuotes(n *Node) int {
	most := 0
	for _, c := range n.Children {
		if q := maxQuotes(c); q > most {
			most = q
		}
	}
	if n.Kind == Element && n.Name == "quote" {
		most++
	}
	return most
}

func TestParseDepthCap(t *testing.T) {
	// A list at the cap becomes text, and the tree stays valid.
	for _, n := range []int{14, 15, 16, 17, 30, 100} {
		src := strings.Repeat("- ", n) + "deep"
		got := Parse(src, allOn)
		if err := Validate(got); err != nil {
			t.Errorf("%d lists: %v", n, err)
		}
		if unmarked(allText(got)) != "deep" {
			t.Errorf("%d lists: text %q", n, allText(got))
		}
	}
	// Mixed quotes and lists, and inline nesting.
	for _, src := range []string{
		strings.Repeat("> - ", 20) + "deep",
		strings.Repeat("- > ", 20) + "deep",
		strings.Repeat("> ", 9) + strings.Repeat("- ", 12) + "deep",
		strings.Repeat("- ", 14) + "# *a **b** `c`*",
	} {
		got := Parse(src, allOn)
		if err := Validate(got); err != nil {
			t.Errorf("%q: %v", src, err)
		}
		if !strings.Contains(allText(got), "deep") && !strings.Contains(allText(got), "a") {
			t.Errorf("%q: lost text: %s", src, js(got))
		}
	}
	// Inline nesting beyond the depth cap flattens to text.
	src := strings.Repeat("*a ", 40) + strings.Repeat("a* ", 40)
	got := Parse(src, allOn)
	if err := Validate(got); err != nil {
		t.Errorf("nested emphasis: %v", err)
	}
	if strings.Count(allText(got), "a") != 80 {
		t.Errorf("nested emphasis lost text: %q", allText(got))
	}
	// The same inside a deep list.
	src = strings.Repeat("- ", 12) + src
	if err := Validate(Parse(src, allOn)); err != nil {
		t.Errorf("nested emphasis in lists: %v", err)
	}
}

func TestParseContainerCap(t *testing.T) {
	// Up to the cap, the markers are markup and the deep quotes are plain text.
	got := Parse(strings.Repeat("> ", 20)+"x", mdOnly)
	if allText(got) != "x" || maxQuotes(got) != MaxQuoteDepth {
		t.Errorf("20 quotes: %s", js(got))
	}
	// Past the cap, goldmark stops opening containers, so the markers stay in
	// the text. This keeps the time linear. A test on the text proves that the
	// capped parsers are in use, which a timing test cannot do reliably.
	got = Parse(strings.Repeat("> ", 40)+"x", mdOnly)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if text := allText(got); !strings.Contains(text, ">") || !strings.HasSuffix(text, "x") {
		t.Errorf("40 quotes: text %q", text)
	}
	got = Parse(strings.Repeat("- ", 80)+"x", mdOnly)
	if text := allText(got); !strings.Contains(text, "-") || !strings.HasSuffix(text, "x") {
		t.Errorf("80 list markers: text %q", text)
	}
}

// mustNotPanic parses src and requires a valid tree.
func mustNotPanic(t *testing.T, name, src string, opt Options) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: Parse panicked: %v", name, r)
		}
	}()
	if err := Validate(Parse(src, opt)); err != nil {
		t.Errorf("%s: invalid tree: %v", name, err)
	}
}

// deepMixed builds a first line of n container markers, then a second line.
func deepMixed(marks []string, n int, second func(n int) string) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(marks[i%len(marks)])
	}
	return sb.String() + "x\n" + second(n)
}

// The container cap once refused a list item but not its list. Goldmark then
// held a paragraph directly in a list and panicked on the next indented line.
func TestParseContainerCapKeepsListsWhole(t *testing.T) {
	cases := map[string]string{
		"bullets after quote":  "> " + strings.Repeat("- ", 16) + "x\n>" + strings.Repeat(" ", 33) + "y",
		"numbers after quote":  "> " + strings.Repeat("1. ", 16) + "x\n>" + strings.Repeat(" ", 49) + "y",
		"quote and bullets":    strings.Repeat("> - ", 11) + "x\n" + strings.Repeat(">   ", 11) + "  y",
		"bullets then indent":  "> " + strings.Repeat("- ", 16) + "x\n" + strings.Repeat("  ", 17) + "y",
		"bullets then quoted":  "> " + strings.Repeat("- ", 16) + "x\n> " + strings.Repeat("  ", 16) + "y",
		"bullets then tabs":    "> " + strings.Repeat("-\t", 16) + "x\n>\t\t\t\ty",
		"bullets then blank":   "> " + strings.Repeat("- ", 16) + "x\n\n>" + strings.Repeat(" ", 33) + "y",
		"bullets then bullet":  "> " + strings.Repeat("- ", 16) + "x\n> - y",
		"numbers then number":  "> " + strings.Repeat("1. ", 16) + "x\n> 1. y",
		"lists only":           strings.Repeat("- ", 40) + "x\n" + strings.Repeat("  ", 40) + "y",
		"numbers only":         strings.Repeat("1. ", 40) + "x\n" + strings.Repeat("   ", 40) + "y",
		"quote list alternate": strings.Repeat("> 1. ", 14) + "x\n" + strings.Repeat(">    ", 14) + "    y",
	}
	for name, src := range cases {
		mustNotPanic(t, name, src, allOn)
		mustNotPanic(t, name+" md only", src, mdOnly)
	}
	// Every mix of quote and list markers up to 44 containers, with a second
	// line of each kind.
	markSets := [][]string{{"> ", "- "}, {"- ", "> "}, {"> ", "1. "}, {"> ", "> ", "- "}, {"- ", "1. ", "> "}, {"> "}, {"- "}, {"1. "}}
	seconds := []func(n int) string{
		func(n int) string { return strings.Repeat(" ", 2*n) + "y" },
		func(n int) string { return strings.Repeat(" ", 3*n) + "y" },
		func(n int) string { return strings.Repeat(">", n) + strings.Repeat(" ", 2*n) + "y" },
		func(n int) string { return strings.Repeat("> ", n) + strings.Repeat("  ", n) + "y" },
		func(n int) string { return strings.Repeat(">   ", n/2) + "  y" },
		func(n int) string { return "y" },
		func(n int) string { return "\ny" },
	}
	for _, marks := range markSets {
		for n := 20; n <= 44; n++ {
			for i, second := range seconds {
				mustNotPanic(t, "mixed", deepMixed(marks, n, second), allOn)
				_ = i
			}
		}
	}
}

func TestParseSmileyCodesMatchValidate(t *testing.T) {
	odd := Options{Markdown: true, Smilies: true, SmileyList: []Smiley{
		{Code: ":\t)"}, {Code: "\xc3"}, {Code: "a\xffb"}, {Code: ":\x01)"}, {Code: ":\u0085)"}, {Code: ":)"},
	}}
	check(t, ":\t)", odd, tree(p(txt(":\t)"))))
	check(t, ":\x01)", odd, tree(p(txt(":\x01)"))))
	check(t, "a\xffb \xc3 :)", odd, tree(p(txt("a\uFFFDb \uFFFD "), el("smiley", at("code", ":)")))))
	check(t, ":\u0085)", odd, tree(p(txt(":\u0085)"))))
	check(t, ":\t)", Options{Smilies: true, SmileyList: odd.SmileyList}, tree(p(txt(":\t)"))))
}

func TestParseImageAlt(t *testing.T) {
	// Newlines and controls never reach the alt attribute.
	check(t, "![a\nb](/x.png)", mdOnly, tree(p(el("img", at("src", "/x.png", "alt", "a b")))))
	check(t, "![a\x01b\x7fc\fd\u0085e\x1bf](/x.png)", mdOnly, tree(p(el("img", at("src", "/x.png", "alt", "a b c d e f")))))
	long := strings.Repeat("é", 1500)
	got := Parse("!["+long+"](/x.png)", mdOnly)
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(got.Children[0].Children[0].Attrs["alt"]); n != 1000 {
		t.Errorf("alt has %d characters", n)
	}
	// A rejected image leaves its alt text.
	check(t, "![a *b*](javascript:x)", mdOnly, tree(p(txt("a b"))))
}

func TestParseStrictValidate(t *testing.T) {
	// CommonMark allows these characters in an email autolink. The email rule
	// does not, so the address stays text.
	check(t, "<a&b@example.com>", allOn, tree(p(txt("a&b@example.com"))))
	check(t, "<a/b@example.com>", allOn, tree(p(txt("a/b@example.com"))))
	check(t, "<a?b@example.com>", allOn, tree(p(txt("a?b@example.com"))))
	check(t, "<a#b@example.com>", allOn, tree(p(txt("a#b@example.com"))))
	check(t, "<a%b@example.com>", allOn, tree(p(txt("a%b@example.com"))))
	check(t, "<a'b@example.com>", allOn, tree(p(txt("a'b@example.com"))))
	check(t, "<a&b@example.com>", mdOnly, tree(p(txt("a&b@example.com"))))
	check(t, "[x](mailto:a&b@example.com)", allOn, tree(p(txt("x"))))
	check(t, "[x](mailto:a/b@example.com)", allOn, tree(p(txt("x"))))
	// A bare address with such a character gives no email element.
	for _, s := range []string{"a&b@example.com", "a/b@example.com", "a%b@example.com", "x a?b@example.com y"} {
		if got := Parse(s, allOn); has(got, "email") {
			t.Errorf("Parse(%q) has an email: %s", s, js(got))
		}
	}
	// A control character in alt text becomes a space.
	check(t, "![a\fb](https://a.example/x.png)", allOn, tree(p(el("img", at("src", "https://a.example/x.png", "alt", "a b")))))
	check(t, "![a\vb\x1bc\x07d\u0085e\tf](/x.png)", allOn, tree(p(el("img", at("src", "/x.png", "alt", "a b c d e f")))))
	check(t, "![a\fb](javascript:x)", allOn, tree(p(txt("a b"))))
	// A link with a format character fails the URL rule, so it becomes text.
	check(t, "[x](https://a.example/\u200d)", allOn, tree(p(txt("x"))))
	check(t, "<https://a.example/\u200d>", allOn, tree(p(txt("https://a.example/\u200d"))))
	check(t, "[x](https://a.example/\u202e)", allOn, tree(p(txt("x"))))
	check(t, "![x](https://a.example/\u200b.png)", allOn, tree(p(txt("x"))))
	got := Parse("see https://a.example/\u200d now", allOn)
	if err := Validate(got); err != nil {
		t.Errorf("bare link with a format character: %v", err)
	}
	if has(got, "url") && strings.Contains(js(got), "u200d") {
		t.Errorf("a link holds a format character: %s", js(got))
	}
}

func TestParseEmailRuleMatchesValidate(t *testing.T) {
	check(t, "<x\u202e@c.example>", allOn, tree(p(txt("<x\u202e@c.example>"))))
	check(t, "<x\\y@c.example>", allOn, tree(p(txt("<x\\y@c.example>"))))
	check(t, "[m](mailto:x\u202e@c.example)", allOn, tree(p(txt("m"))))
	for _, s := range []string{"x\u202e@c.example", "x\u200b@c.example", "x\\y@c.example"} {
		if got := Parse("see "+s+" now", allOn); has(got, "email") {
			t.Errorf("Parse(%q) has an email: %s", s, js(got))
		}
	}
}

// TestRenderParseURLRoundTrip holds the parser and the renderer to one rule for
// URLs: a stored address is literal, nothing decodes entities in it, and the
// renderer escapes it once.
func TestRenderParseURLRoundTrip(t *testing.T) {
	cases := []struct {
		name, md, attr, want, html string
	}{
		{"bare link with entity-like names", "https://example.com/?a=1&not=3&para=2&reg=1", "href",
			"https://example.com/?a=1&not=3&para=2&reg=1", `href="https://example.com/?a=1&amp;not=3&amp;para=2&amp;reg=1"`},
		{"md link copy", "[x](/a?x=1&copy=2)", "href", "/a?x=1&copy=2", `href="/a?x=1&amp;copy=2"`},
		{"image times", "![a](/a.png?x&times=1)", "src", "/a.png?x&times=1", `src="/a.png?x&amp;times=1"`},
		{"shy stays a link", "https://example.com/?x=1&shy=1", "href", "https://example.com/?x=1&shy=1", `href="https://example.com/?x=1&amp;shy=1"`},
		{"md link shy", "[x](https://example.com/?x=1&shy=1)", "href", "https://example.com/?x=1&shy=1", `href="https://example.com/?x=1&amp;shy=1"`},
		{"md link with a semicolon entity", "[x](/a?b=1&amp;c=2)", "href", "/a?b=1&c=2", `href="/a?b=1&amp;c=2"`},
		{"angle autolink is literal", "<https://example.com/?a=1&amp;b>", "href", "https://example.com/?a=1&amp;b",
			`href="https://example.com/?a=1&amp;amp;b"`},
		{"image para", "![a](https://example.com/a.png?x&para=1)", "src", "https://example.com/a.png?x&para=1",
			`src="https://example.com/a.png?x&amp;para=1"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := Parse(c.md, allOn)
			if err := Validate(tr); err != nil {
				t.Fatal(err)
			}
			var found *Node
			var walk func(*Node)
			walk = func(n *Node) {
				if n.Kind == Element && (n.Name == "url" || n.Name == "img") && found == nil {
					found = n
				}
				for _, ch := range n.Children {
					walk(ch)
				}
			}
			walk(tr)
			if found == nil {
				t.Fatalf("no link or image in %s", js(tr))
			}
			if got := found.Attrs[c.attr]; got != c.want {
				t.Errorf("stored %s = %q, want %q", c.attr, got, c.want)
			}
			if out := string(Render(tr, RenderContext{}).HTML); !strings.Contains(out, c.html) {
				t.Errorf("html = %s, want it to contain %s", out, c.html)
			}
		})
	}
}

func TestParseNeverEmitsBlankLiText(t *testing.T) {
	for _, src := range []string{"- a\n\n  b\n- c", "1. a\n   - b\n\n2. c", "-\n- a\n-", "- a\n\n\n- b"} {
		got := Parse(src, allOn)
		if err := Validate(got); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
}

// oddSmilies holds smiley codes that Validate would reject if they matched.
var oddSmilies = Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: []Smiley{
	{Code: ":\t)"}, {Code: "\xc3"}, {Code: ":\x01)"}, {Code: ":\u0085)"}, {Code: "\t"}, {Code: " "}, {Code: ":)"}, {Code: "x"},
}}

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
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, opt := range []Options{allOn, mdOnly, {}, {Autolink: true, Smilies: true, SmileyList: smileyList}, oddSmilies} {
			got := Parse(s, opt)
			if err := Validate(got); err != nil {
				t.Fatalf("invalid tree for %q with %+v: %v\n%s", s, opt, err, js(got))
			}
		}
	})
}

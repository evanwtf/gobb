package markup

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// para wraps inline children in a paragraph, the usual home for inlines.
func para(children ...*Node) *Node { return el("p", nil, children...) }

// chain builds total nested elements: a p, then total-2 b elements, then a
// text leaf inside the last b. total must be at least 2.
func chain(total int) *Node {
	cur := el("b", nil, txt("x"))
	for i := 2; i < total; i++ {
		cur = el("b", nil, cur)
	}
	return para(cur)
}

// listChain builds total nested elements: list, li, p, then b elements.
func listChain(total int) *Node {
	cur := el("b", nil, txt("x"))
	for i := 4; i < total; i++ {
		cur = el("b", nil, cur)
	}
	return el("list", nil, el("li", nil, para(cur)))
}

// quotes builds n nested quote elements around a paragraph.
func quotes(n int) *Node {
	cur := para(txt("x"))
	for range n {
		cur = el("quote", nil, cur)
	}
	return cur
}

func TestValidateAccepts(t *testing.T) {
	tests := map[string]*Node{
		"empty root": tree(),
		"every kind": everyKindTree(),
		// p
		"p":      tree(para()),
		"p text": tree(para(txt("x"), txt("y"))),
		"p inline": tree(para(
			el("b", nil), el("i", nil), el("code", nil), el("br", nil), el("url", at("href", "https://a.example")),
			el("email", at("address", "a@b.example")), el("img", at("src", "/x.png")),
			el("spoiler", nil), el("smiley", at("code", ":)")))),
		// h
		"h1":          tree(el("h", at("level", "1"))),
		"h6":          tree(el("h", at("level", "6"), txt("x"))),
		"h inline":    tree(el("h", at("level", "3"), el("i", nil, txt("x")), el("code", nil, txt("y")))),
		"hr":          tree(el("hr", nil)),
		"b in b":      tree(para(el("b", nil, el("b", nil, txt("x"))))),
		"i":           tree(para(el("i", nil, txt("x")))),
		"code":        tree(para(el("code", nil, txt("a"), txt("b")))),
		"code empty":  tree(para(el("code", nil))),
		"br":          tree(para(el("br", nil))),
		"pre":         tree(el("pre", nil, txt("a\nb"))),
		"pre lang":    tree(el("pre", at("lang", "Az09_+.#-"))),
		"pre lang 32": tree(el("pre", at("lang", strings.Repeat("a", 32)), txt("x"))),
		// quote
		"quote":         tree(el("quote", nil)),
		"quote blocks":  tree(el("quote", nil, para(txt("x")), el("hr", nil), el("pre", nil), el("list", nil), el("youtube", at("id", "dQw4w9WgXcQ")))),
		"10 quotes":     tree(quotes(10)),
		"quotes spread": tree(quotes(10), quotes(10)),
		// list
		"list":         tree(el("list", nil)),
		"list li":      tree(el("list", nil, el("li", nil), el("li", nil, para(txt("x")), el("pre", nil)))),
		"list ordered": tree(el("list", at("ordered", "true"), el("li", nil))),
		"list start":   tree(el("list", at("ordered", "true", "start", "999999999"))),
		"list start 1": tree(el("list", at("ordered", "true", "start", "1"))),
		"list tight":   tree(el("list", at("tight", "true"))),
		"list in li":   tree(el("list", nil, el("li", nil, el("list", nil, el("li", nil))))),
		"quote in li":  tree(el("list", nil, el("li", nil, el("quote", nil, para())))),
		// url
		"url http":            tree(para(el("url", at("href", "http://example.com"), txt("x")))),
		"url upper":           tree(para(el("url", at("href", "HTTPS://EXAMPLE.com/p?q=1&amp;r=2#f")))),
		"url site":            tree(para(el("url", at("href", "/t/5")))),
		"url root":            tree(para(el("url", at("href", "/")))),
		"url mailto":          tree(para(el("url", at("href", "mailto:a@b.example")))),
		"url padded":          tree(para(el("url", at("href", " https://a.example/ ")))),
		"url display":         tree(para(el("url", at("href", "https://a.example", "display", strings.Repeat("é", 255))))),
		"url nested b":        tree(para(el("url", at("href", "https://example.com"), el("b", nil, el("img", at("src", "https://example.com/a.png")))))),
		"url spoiler":         tree(para(el("url", at("href", "https://a.example"), el("spoiler", nil, txt("x"))))),
		"link after":          tree(para(el("url", at("href", "https://a.example"), txt("x")), el("url", at("href", "https://b.example"), txt("y")))),
		"email":               tree(para(el("email", at("address", "a.b+c@example.com")))),
		"email in b":          tree(para(el("email", at("address", "a@b.example"), el("b", nil, txt("x"))))),
		"img":                 tree(para(el("img", at("src", "https://example.com/a.png")))),
		"img site":            tree(para(el("img", at("src", "/files/a.png")))),
		"img alt":             tree(para(el("img", at("src", "/a.png", "alt", strings.Repeat("é", 1000))))),
		"img empty alt":       tree(para(el("img", at("src", "/a.png", "alt", "")))),
		"spoiler":             tree(para(el("spoiler", nil, txt("x"), el("i", nil, txt("y"))))),
		"spoiler link":        tree(para(el("spoiler", nil, el("url", at("href", "https://a.example"))))),
		"alt tab newline":     tree(para(el("img", at("src", "/a.png", "alt", "a\tb\r\nc")))),
		"display tab newline": tree(para(el("url", at("href", "https://a.example", "display", "a\tb\nc\r")))),
		"spoilers next":       tree(para(el("spoiler", nil), el("spoiler", nil))),
		"smiley":              tree(para(el("smiley", at("code", ":-)")))),
		"smiley 32":           tree(para(el("smiley", at("code", strings.Repeat("é", 32))))),
		// embeds
		"youtube":        tree(el("youtube", at("id", "dQw4w9WgXcQ"))),
		"vimeo":          tree(el("vimeo", at("id", "1"))),
		"tweet":          tree(el("tweet", at("id", "12345678901234567890"))),
		"instagram":      tree(el("instagram", at("id", strings.Repeat("a", 64)))),
		"reddit":         tree(el("reddit", at("id", "go/a"))),
		"tiktok":         tree(el("tiktok", at("id", "1"))),
		"threads":        tree(el("threads", at("id", "a/b"))),
		"spotify":        tree(el("spotify", at("id", "album/4uLU6hMCjMI75M1A2tKUQC"))),
		"soundcloud":     tree(el("soundcloud", at("id", "a/b"))),
		"embed in quote": tree(el("quote", nil, el("vimeo", at("id", "1")))),
		"embed in li":    tree(el("list", nil, el("li", nil, el("vimeo", at("id", "1"))))),
		// depth
		"32 deep":      tree(chain(32)),
		"32 deep list": tree(listChain(32)),
		"deep quotes":  tree(el("quote", nil, el("list", nil, el("li", nil, el("quote", nil))))),
	}
	for name, n := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Validate(n); err != nil {
				t.Fatalf("want valid, got %v", err)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	const (
		urlA    = "https://a.example"
		onlyLi  = "list may hold only li elements"
		noNest  = "url and email may not nest inside url or email"
		noURL   = "href not allowed"
		noSrc   = "src not allowed"
		noMail  = "address is not a plain email address"
		dispCtl = "display must be 1 to 255 characters, with no control characters"
		altCtl  = "alt must be at most 1000 characters, with no control characters"
		codeCtl = "code must be 1 to 32 characters, with no control characters"
		langMsg = "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"
	)
	yt := at("id", "dQw4w9WgXcQ")
	two := tree(para())
	two.Children[0].Children = append(two.Children[0].Children, &Node{Kind: Root})
	hrefCase := func(v string) *Node { return para(el("url", at("href", v))) }
	srcCase := func(v string) *Node { return para(el("img", at("src", v))) }
	mailCase := func(v string) *Node { return para(el("email", at("address", v))) }

	tests := []struct {
		name string
		tree *Node
		path string // Error.Path
		msg  string // Error.Msg
	}{
		// The top node.
		{"nil root", nil, "/", "root is nil"},
		{"element as root", el("b", nil), "/", "top node must have kind root"},
		{"text as root", txt("x"), "/", "top node must have kind root"},
		{"unknown kind as root", &Node{Kind: 9}, "/", "top node must have kind root"},
		{"root with name", &Node{Kind: Root, Name: "b"}, "/", "root has a name, attributes or text"},
		{"root with attrs", &Node{Kind: Root, Attrs: at("a", "b")}, "/", "root has a name, attributes or text"},
		{"root with text", &Node{Kind: Root, Text: "x"}, "/", "root has a name, attributes or text"},
		// Node shape.
		{"unknown kind on a child", tree(para(&Node{Kind: 9})), "/p[0]/#kind9[0]", "unknown node kind 9"},
		{"unknown kind in root", tree(&Node{Kind: 9}), "/#kind9[0]", "unknown node kind 9"},
		{"second root", two, "/p[0]/#root[0]", "root node inside the tree"},
		{"root child of root", tree(&Node{Kind: Root}), "/#root[0]", "root node inside the tree"},
		{"nil child", tree(para(nil)), "/p[0]/nil[0]", "nil child"},
		{"nil child of root", &Node{Kind: Root, Children: []*Node{nil}}, "/nil[0]", "nil child"},
		{"element with text field", tree(para(&Node{Kind: Element, Name: "b", Text: "x"})), "/p[0]/b[0]", "element has text"},
		{"text with name", tree(para(&Node{Kind: Text, Name: "b", Text: "x"})), "/p[0]/#text[0]", "text node has a name, attributes or children"},
		{"text with attrs", tree(para(&Node{Kind: Text, Attrs: at("a", "b"), Text: "x"})), "/p[0]/#text[0]", "text node has a name, attributes or children"},
		{"text with children", tree(para(&Node{Kind: Text, Text: "x", Children: []*Node{txt("y")}})), "/p[0]/#text[0]", "text node has a name, attributes or children"},
		{"bad utf-8 text", tree(para(txt("a\xffb"))), "/p[0]/#text[0]", "text is not valid UTF-8"},
		{"bad utf-8 text in pre", tree(el("pre", nil, txt("\xff"))), "/pre[0]/#text[0]", "text is not valid UTF-8"},

		// Element names.
		{"unknown element", tree(el("script", nil, txt("alert(1)"))), "/script[0]", "unknown element"},
		{"empty element name", tree(el("", nil)), `/""[0]`, "unknown element"},
		{"upper-case element", tree(el("B", nil)), "/B[0]", "unknown element"},
		{"removed u", tree(para(el("u", nil, txt("x")))), "/p[0]/u[0]", "unknown element"},
		{"removed color", tree(para(el("color", at("value", "red"), txt("x")))), "/p[0]/color[0]", "unknown element"},
		{"removed size", tree(para(el("size", at("value", "100"), txt("x")))), "/p[0]/size[0]", "unknown element"},
		{"removed shout", tree(para(el("shout", nil, txt("x")))), "/p[0]/shout[0]", "unknown element"},
		{"removed comic", tree(para(el("comic", nil, txt("x")))), "/p[0]/comic[0]", "unknown element"},
		{"removed attachment", tree(para(el("attachment", at("index", "1")))), "/p[0]/attachment[0]", "unknown element"},
		{"removed shout in root", tree(el("shout", nil)), "/shout[0]", "unknown element"},

		// What the root and each parent may hold.
		{"text in root", tree(txt("x")), "/#text[0]", "text not allowed in root"},
		{"inline in root", tree(el("b", nil)), "/b[0]", "b not allowed in root"},
		{"smiley in root", tree(el("smiley", at("code", ":)"))), "/smiley[0]", "smiley not allowed in root"},
		{"li in root", tree(el("li", nil)), "/li[0]", "li outside list"},
		{"p in b", tree(para(el("b", nil, para()))), "/p[0]/b[0]/p[0]", "p not allowed in b"},
		{"p in p", tree(para(para())), "/p[0]/p[0]", "p not allowed in p"},
		{"h in p", tree(para(el("h", at("level", "1")))), "/p[0]/h[0]", "h not allowed in p"},
		{"quote in p", tree(para(el("quote", nil))), "/p[0]/quote[0]", "quote not allowed in p"},
		{"list in p", tree(para(el("list", nil))), "/p[0]/list[0]", "list not allowed in p"},
		{"pre in p", tree(para(el("pre", nil))), "/p[0]/pre[0]", "pre not allowed in p"},
		{"hr in p", tree(para(el("hr", nil))), "/p[0]/hr[0]", "hr not allowed in p"},
		{"embed in p", tree(para(el("youtube", yt))), "/p[0]/youtube[0]", "youtube not allowed in p"},
		{"embed in b", tree(para(el("b", nil, el("tweet", at("id", "20"))))), "/p[0]/b[0]/tweet[0]", "tweet not allowed in b"},
		{"embed in h", tree(el("h", at("level", "1"), el("vimeo", at("id", "1")))), "/h[0]/vimeo[0]", "vimeo not allowed in h"},
		{"p in url", tree(para(el("url", at("href", urlA), para()))), "/p[0]/url[0]/p[0]", "p not allowed in url"},
		{"p in spoiler", tree(para(el("spoiler", nil, para()))), "/p[0]/spoiler[0]/p[0]", "p not allowed in spoiler"},
		{"text in quote", tree(el("quote", nil, txt("x"))), "/quote[0]/#text[0]", "text not allowed in quote"},
		{"inline in quote", tree(el("quote", nil, el("i", nil))), "/quote[0]/i[0]", "i not allowed in quote"},
		{"li in quote", tree(el("quote", nil, el("li", nil))), "/quote[0]/li[0]", "li outside list"},
		{"li holds text", tree(el("list", nil, el("li", nil, txt("x")))), "/list[0]/li[0]/#text[0]", "text not allowed in li"},
		{"li holds inline", tree(el("list", nil, el("li", nil, el("b", nil)))), "/list[0]/li[0]/b[0]", "b not allowed in li"},
		{"li in li", tree(el("list", nil, el("li", nil, el("li", nil)))), "/list[0]/li[0]/li[0]", "li outside list"},
		{"li in b in p in li in list", tree(el("list", nil, el("li", nil, para(el("b", nil, el("li", nil)))))), "/list[0]/li[0]/p[0]/b[0]/li[0]", "li outside list"},
		{"li in b in list", tree(el("list", nil, el("b", nil, el("li", nil)))), "/list[0]/b[0]", onlyLi},
		{"list holds text", tree(el("list", nil, txt("stray"))), "/list[0]/#text[0]", onlyLi},
		{"list holds blank text", tree(el("list", nil, txt("\n"))), "/list[0]/#text[0]", onlyLi},
		{"list holds p", tree(el("list", nil, para())), "/list[0]/p[0]", onlyLi},
		{"list holds list", tree(el("list", nil, el("list", nil))), "/list[0]/list[0]", onlyLi},
		{"pre holds element", tree(el("pre", nil, el("b", nil))), "/pre[0]/b[0]", "pre may hold only text"},
		{"pre holds code", tree(el("pre", nil, txt("a"), el("code", nil))), "/pre[0]/code[1]", "pre may hold only text"},
		{"code holds element", tree(para(el("code", nil, el("b", nil)))), "/p[0]/code[0]/b[0]", "code may hold only text"},
		{"hr with child", tree(el("hr", nil, txt("x"))), "/hr[0]", "hr takes no children"},
		{"br with child", tree(para(el("br", nil, txt("x")))), "/p[0]/br[0]", "br takes no children"},
		{"img with child", tree(para(el("img", at("src", "/x.png"), txt("x")))), "/p[0]/img[0]", "img takes no children"},
		{"smiley with child", tree(para(el("smiley", at("code", ":)"), txt("x")))), "/p[0]/smiley[0]", "smiley takes no children"},
		{"embed with child", tree(el("youtube", yt, txt("x"))), "/youtube[0]", "youtube takes no children"},

		// Nesting of links and spoilers.
		{"url in url", tree(para(el("url", at("href", urlA), el("url", at("href", "https://b.example"))))), "/p[0]/url[0]/url[0]", noNest},
		{"url deep in url", tree(para(el("url", at("href", urlA), el("b", nil, el("url", at("href", "https://b.example")))))), "/p[0]/url[0]/b[0]/url[0]", noNest},
		{"email in url", tree(para(el("url", at("href", urlA), el("email", at("address", "a@b.example"))))), "/p[0]/url[0]/email[0]", noNest},
		{"url in email", tree(para(el("email", at("address", "a@b.example"), el("url", at("href", urlA))))), "/p[0]/email[0]/url[0]", noNest},
		{"email in email", tree(para(el("email", at("address", "a@b.example"), el("email", at("address", "c@d.example"))))), "/p[0]/email[0]/email[0]", noNest},
		{"url in spoiler in url", tree(para(el("url", at("href", urlA), el("spoiler", nil, el("url", at("href", urlA)))))), "/p[0]/url[0]/spoiler[0]/url[0]", noNest},
		{"spoiler in spoiler", tree(para(el("spoiler", nil, el("spoiler", nil)))), "/p[0]/spoiler[0]/spoiler[0]", "spoiler may not nest inside spoiler"},
		{"spoiler deep in spoiler", tree(para(el("spoiler", nil, el("b", nil, el("url", at("href", urlA), el("spoiler", nil)))))), "/p[0]/spoiler[0]/b[0]/url[0]/spoiler[0]", "spoiler may not nest inside spoiler"},

		// Attributes common to all elements.
		{"unknown attr on b", tree(para(el("b", at("onclick", "x")))), "/p[0]/b[0]", "unknown attribute onclick"},
		{"unknown attr on p", tree(para2(at("class", "x"))), "/p[0]", "unknown attribute class"},
		{"unknown attr on url", tree(para(el("url", at("href", urlA, "style", "x")))), "/p[0]/url[0]", "unknown attribute style"},
		{"hostile attr name is quoted", tree(para(el("b", at("a\"b", "x")))), "/p[0]/b[0]", `unknown attribute "a\"b"`},
		{"quote attr", tree(el("quote", at("author", "Ann"), para())), "/quote[0]", "unknown attribute author"},
		{"list type", tree(el("list", at("type", "disc"))), "/list[0]", "unknown attribute type"},
		{"li attr", tree(el("list", nil, el("li", at("a", "b")))), "/list[0]/li[0]", "unknown attribute a"},
		{"spoiler label", tree(para(el("spoiler", at("label", "x")))), "/p[0]/spoiler[0]", "unknown attribute label"},
		{"code attr", tree(para(el("code", at("a", "b"), txt("x")))), "/p[0]/code[0]", "unknown attribute a"},
		{"hr attr", tree(el("hr", at("a", "b"))), "/hr[0]", "unknown attribute a"},

		// h
		{"h level 7", tree(el("h", at("level", "7"))), "/h[0]", "level must be 1 to 6"},
		{"h level 0", tree(el("h", at("level", "0"))), "/h[0]", "level must be 1 to 6"},
		{"h level padded", tree(el("h", at("level", "01"))), "/h[0]", "level must be 1 to 6"},
		{"h level words", tree(el("h", at("level", "one"))), "/h[0]", "level must be 1 to 6"},
		{"h level empty", tree(el("h", at("level", ""))), "/h[0]", "level must be 1 to 6"},
		{"h level missing", tree(el("h", nil)), "/h[0]", "missing attribute level"},

		// list
		{"list start 0", tree(el("list", at("ordered", "true", "start", "0"))), "/list[0]", "start must be 1 to 9 digits with no leading zero"},
		{"list start leading zero", tree(el("list", at("ordered", "true", "start", "01"))), "/list[0]", "start must be 1 to 9 digits with no leading zero"},
		{"list start 10 digits", tree(el("list", at("ordered", "true", "start", "1234567890"))), "/list[0]", "start must be 1 to 9 digits with no leading zero"},
		{"list start sign", tree(el("list", at("ordered", "true", "start", "+1"))), "/list[0]", "start must be 1 to 9 digits with no leading zero"},
		{"list start empty", tree(el("list", at("ordered", "true", "start", ""))), "/list[0]", "start must be 1 to 9 digits with no leading zero"},
		{"list start without ordered", tree(el("list", at("start", "3"))), "/list[0]", "start needs ordered"},
		{"list ordered false", tree(el("list", at("ordered", "false"))), "/list[0]", "ordered must be true"},
		{"list ordered 1", tree(el("list", at("ordered", "1"))), "/list[0]", "ordered must be true"},
		{"list tight yes", tree(el("list", at("tight", "yes"))), "/list[0]", "tight must be true"},
		{"list tight empty", tree(el("list", at("tight", ""))), "/list[0]", "tight must be true"},

		// pre
		{"pre lang empty", tree(el("pre", at("lang", ""))), "/pre[0]", "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"},
		{"pre lang long", tree(el("pre", at("lang", strings.Repeat("a", 33)))), "/pre[0]", "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"},
		{"pre lang space", tree(el("pre", at("lang", "c c"))), "/pre[0]", "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"},
		{"pre lang quote", tree(el("pre", at("lang", `c"x`))), "/pre[0]", "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"},
		{"pre lang non-ASCII", tree(el("pre", at("lang", "é"))), "/pre[0]", "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"},

		// url
		{"url missing href", tree(para(el("url", nil))), "/p[0]/url[0]", "missing attribute href"},
		{"url javascript", tree(hrefCase("javascript:alert(1)")), "/p[0]/url[0]", noURL},
		{"url mixed case", tree(hrefCase("JaVaScRiPt:x")), "/p[0]/url[0]", noURL},
		{"url data", tree(hrefCase("data:text/html,x")), "/p[0]/url[0]", noURL},
		{"url vbscript", tree(hrefCase("vbscript:x")), "/p[0]/url[0]", noURL},
		{"url entity", tree(hrefCase("&#106;avascript:x")), "/p[0]/url[0]", noURL},
		{"url hex entity", tree(hrefCase("&#x6A;avascript:x")), "/p[0]/url[0]", noURL},
		{"url named entity colon", tree(hrefCase("javascript&colon;alert(1)")), "/p[0]/url[0]", noURL},
		{"url tab", tree(hrefCase("java\tscript:x")), "/p[0]/url[0]", noURL},
		{"url newline", tree(hrefCase("java\nscript:x")), "/p[0]/url[0]", noURL},
		{"url NUL", tree(hrefCase("java\x00script:x")), "/p[0]/url[0]", noURL},
		{"url entity tab", tree(hrefCase("java&Tab;script:x")), "/p[0]/url[0]", noURL},
		{"url leading ctrl", tree(hrefCase("\x01javascript:x")), "/p[0]/url[0]", noURL},
		{"url space inside", tree(hrefCase("https://a.example/a b")), "/p[0]/url[0]", noURL},
		{"url nbsp", tree(hrefCase("https://a.example/a b")), "/p[0]/url[0]", noURL},
		{"url backslash", tree(hrefCase("https:\\\\a.example")), "/p[0]/url[0]", noURL},
		{"url ftp", tree(hrefCase("ftp://a.example/")), "/p[0]/url[0]", noURL},
		{"url relative", tree(hrefCase("path")), "/p[0]/url[0]", noURL},
		{"url scheme relative", tree(hrefCase("//a.example/")), "/p[0]/url[0]", noURL},
		{"url slash backslash", tree(hrefCase(`/\evil.example`)), "/p[0]/url[0]", noURL},
		{"url slash entity backslash", tree(hrefCase(`/&bsol;evil.example`)), "/p[0]/url[0]", noURL},
		{"url slash entity slash", tree(hrefCase(`/&#47;evil.example`)), "/p[0]/url[0]", noURL},
		{"url path space", tree(hrefCase("/a b")), "/p[0]/url[0]", noURL},
		{"url path tab", tree(hrefCase("/a\tb")), "/p[0]/url[0]", noURL},
		{"url path control", tree(hrefCase("/a\x01")), "/p[0]/url[0]", noURL},
		{"url path bad escape", tree(hrefCase("/a%zz")), "/p[0]/url[0]", noURL},
		{"url mailto empty", tree(hrefCase("mailto:")), "/p[0]/url[0]", noURL},
		{"url mailto no at", tree(hrefCase("mailto:a")), "/p[0]/url[0]", noURL},
		{"url mailto two at", tree(hrefCase("mailto:a@b@c")), "/p[0]/url[0]", noURL},
		{"url mailto space", tree(hrefCase("mailto:a b@c.example")), "/p[0]/url[0]", noURL},
		{"url no host", tree(hrefCase("http://")), "/p[0]/url[0]", noURL},
		{"url port only", tree(hrefCase("http://:80/")), "/p[0]/url[0]", noURL},
		{"url opaque", tree(hrefCase("http:a.example")), "/p[0]/url[0]", noURL},
		{"url empty", tree(hrefCase("")), "/p[0]/url[0]", noURL},
		{"url blank", tree(hrefCase(" \t ")), "/p[0]/url[0]", noURL},
		{"url display empty", tree(para(el("url", at("href", urlA, "display", "")))), "/p[0]/url[0]", "display must be 1 to 255 characters, with no control characters"},
		{"url display long", tree(para(el("url", at("href", urlA, "display", strings.Repeat("a", 256))))), "/p[0]/url[0]", "display must be 1 to 255 characters, with no control characters"},
		{"url display bad utf-8", tree(para(el("url", at("href", urlA, "display", "\xff")))), "/p[0]/url[0]", "display is not valid UTF-8"},
		{"url href bad utf-8", tree(hrefCase("https://a.example/\xff")), "/p[0]/url[0]", "href is not valid UTF-8"},

		// email
		{"email missing", tree(para(el("email", nil))), "/p[0]/email[0]", "missing attribute address"},
		{"email no at", tree(mailCase("ab.example")), "/p[0]/email[0]", noMail},
		{"email two at", tree(mailCase("a@b@c")), "/p[0]/email[0]", noMail},
		{"email space", tree(mailCase("a b@c.example")), "/p[0]/email[0]", noMail},
		{"email colon", tree(mailCase("javascript:x@c.example")), "/p[0]/email[0]", noMail},
		{"email control", tree(mailCase("a\x01@c.example")), "/p[0]/email[0]", noMail},
		{"email angle", tree(mailCase("a<@c.example")), "/p[0]/email[0]", noMail},
		{"email quote", tree(mailCase(`a"@c.example`)), "/p[0]/email[0]", noMail},
		{"email apostrophe", tree(mailCase("a'@c.example")), "/p[0]/email[0]", noMail},
		{"email empty local", tree(mailCase("@c.example")), "/p[0]/email[0]", noMail},
		{"email empty domain", tree(mailCase("a@")), "/p[0]/email[0]", noMail},

		// img
		{"img missing src", tree(para(el("img", nil))), "/p[0]/img[0]", "missing attribute src"},
		{"img data", tree(srcCase("data:image/png;base64,x")), "/p[0]/img[0]", noSrc},
		{"img scheme relative", tree(srcCase("//evil.example/x")), "/p[0]/img[0]", noSrc},
		{"img slash backslash", tree(srcCase(`/\evil.example/x`)), "/p[0]/img[0]", noSrc},
		{"img mailto", tree(srcCase("mailto:a@b.example")), "/p[0]/img[0]", noSrc},
		{"img javascript", tree(srcCase("javascript:x")), "/p[0]/img[0]", noSrc},
		{"img relative", tree(srcCase("x.png")), "/p[0]/img[0]", noSrc},
		{"img alt long", tree(para(el("img", at("src", "/x.png", "alt", strings.Repeat("a", 1001))))), "/p[0]/img[0]", "alt must be at most 1000 characters, with no control characters"},
		{"img alt bad utf-8", tree(para(el("img", at("src", "/x.png", "alt", "\xff")))), "/p[0]/img[0]", "alt is not valid UTF-8"},

		// control characters in attribute values
		{"display NUL", tree(para(el("url", at("href", urlA, "display", "a\x00b")))), "/p[0]/url[0]", dispCtl},
		{"display escape", tree(para(el("url", at("href", urlA, "display", "a\x1bb")))), "/p[0]/url[0]", dispCtl},
		{"display DEL", tree(para(el("url", at("href", urlA, "display", "a\x7fb")))), "/p[0]/url[0]", dispCtl},
		{"alt NUL", tree(para(el("img", at("src", "/x.png", "alt", "a\x00b")))), "/p[0]/img[0]", altCtl},
		{"alt bell", tree(para(el("img", at("src", "/x.png", "alt", "a\x07")))), "/p[0]/img[0]", altCtl},
		{"smiley code tab", tree(para(el("smiley", at("code", ":\t)")))), "/p[0]/smiley[0]", codeCtl},
		{"smiley code newline", tree(para(el("smiley", at("code", ":\n)")))), "/p[0]/smiley[0]", codeCtl},
		{"smiley code NUL", tree(para(el("smiley", at("code", ":\x00")))), "/p[0]/smiley[0]", codeCtl},
		{"pre lang tab", tree(el("pre", at("lang", "c\tc"))), "/pre[0]", langMsg},
		{"pre lang NUL", tree(el("pre", at("lang", "c\x00"))), "/pre[0]", langMsg},
		{"pre lang newline", tree(el("pre", at("lang", "c\n"))), "/pre[0]", langMsg},

		// email address: characters that change a mailto: URL
		{"email question", tree(mailCase("a@b.example?subject=x")), "/p[0]/email[0]", noMail},
		{"email hash", tree(mailCase("a@b.example#x")), "/p[0]/email[0]", noMail},
		{"email percent", tree(mailCase("a@b.example%0D%0ABcc")), "/p[0]/email[0]", noMail},
		{"email percent local", tree(mailCase("a%40c@b.example")), "/p[0]/email[0]", noMail},
		{"email ampersand", tree(mailCase("a@b.example&cc=x")), "/p[0]/email[0]", noMail},
		{"email slash", tree(mailCase("/a@b")), "/p[0]/email[0]", noMail},
		{"url mailto query", tree(hrefCase("mailto:a@b.example?subject=x")), "/p[0]/url[0]", noURL},
		{"url mailto entity query", tree(hrefCase("mailto:a@b.example&#63;cc=x")), "/p[0]/url[0]", noURL},
		{"url mailto percent", tree(hrefCase("mailto:a@b.example%0D%0ABcc%3Ax")), "/p[0]/url[0]", noURL},
		{"url zero-width space", tree(hrefCase("https://a.example/\u200bx")), "/p[0]/url[0]", noURL},
		{"url bidi override", tree(hrefCase("https://a.example/\u202ex")), "/p[0]/url[0]", noURL},
		{"url soft hyphen", tree(hrefCase("https://a.exam\u00adple/")), "/p[0]/url[0]", noURL},
		{"img zero-width space", tree(srcCase("/\u200bx.png")), "/p[0]/img[0]", noSrc},
		{"url no host one slash", tree(hrefCase("http:/x")), "/p[0]/url[0]", noURL},
		{"url opaque https", tree(hrefCase("https:evil")), "/p[0]/url[0]", noURL},

		// threads user made of dots
		{"threads dotdot", tree(el("threads", at("id", "../x"))), "/threads[0]", "id is not a canonical threads ID"},
		{"threads dot", tree(el("threads", at("id", "./x"))), "/threads[0]", "id is not a canonical threads ID"},

		// smiley
		{"smiley missing code", tree(para(el("smiley", nil))), "/p[0]/smiley[0]", "missing attribute code"},
		{"smiley empty code", tree(para(el("smiley", at("code", "")))), "/p[0]/smiley[0]", "code must be 1 to 32 characters, with no control characters"},
		{"smiley long code", tree(para(el("smiley", at("code", strings.Repeat("a", 33))))), "/p[0]/smiley[0]", "code must be 1 to 32 characters, with no control characters"},

		// embeds
		{"youtube bad id", tree(el("youtube", at("id", "not an id"))), "/youtube[0]", "id is not a canonical youtube ID"},
		{"youtube url id", tree(el("youtube", at("id", "https://youtu.be/dQw4w9WgXcQ"))), "/youtube[0]", "id is not a canonical youtube ID"},
		{"youtube padded id", tree(el("youtube", at("id", " dQw4w9WgXcQ"))), "/youtube[0]", "id is not a canonical youtube ID"},
		{"youtube missing", tree(el("youtube", nil)), "/youtube[0]", "missing attribute id"},
		{"vimeo url id", tree(el("vimeo", at("id", "https://vimeo.com/1"))), "/vimeo[0]", "id is not a canonical vimeo ID"},
		{"tweet url id", tree(el("tweet", at("id", "https://x.com/a/status/1"))), "/tweet[0]", "id is not a canonical tweet ID"},
		{"instagram url id", tree(el("instagram", at("id", "https://instagram.com/p/abc"))), "/instagram[0]", "id is not a canonical instagram ID"},
		{"reddit url id", tree(el("reddit", at("id", "https://reddit.com/r/go/comments/abc"))), "/reddit[0]", "id is not a canonical reddit ID"},
		{"reddit upper post", tree(el("reddit", at("id", "go/ABC"))), "/reddit[0]", "id is not a canonical reddit ID"},
		{"tiktok url id", tree(el("tiktok", at("id", "https://tiktok.com/@a/video/1"))), "/tiktok[0]", "id is not a canonical tiktok ID"},
		{"tiktok letters", tree(el("tiktok", at("id", "12a"))), "/tiktok[0]", "id is not a canonical tiktok ID"},
		{"threads url id", tree(el("threads", at("id", "https://threads.net/@a/post/b"))), "/threads[0]", "id is not a canonical threads ID"},
		{"threads no post", tree(el("threads", at("id", "someone"))), "/threads[0]", "id is not a canonical threads ID"},
		{"spotify url id", tree(el("spotify", at("id", "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC"))), "/spotify[0]", "id is not a canonical spotify ID"},
		{"spotify bad type", tree(el("spotify", at("id", "artist/4uLU6hMCjMI75M1A2tKUQC"))), "/spotify[0]", "id is not a canonical spotify ID"},
		{"soundcloud url id", tree(el("soundcloud", at("id", "https://soundcloud.com/a/b"))), "/soundcloud[0]", "id is not a canonical soundcloud ID"},
		{"soundcloud upper", tree(el("soundcloud", at("id", "A/b"))), "/soundcloud[0]", "id is not a canonical soundcloud ID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.tree)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			var ve *Error
			if !errors.As(err, &ve) {
				t.Fatalf("error is %T, want *Error", err)
			}
			if ve.Path != tc.path || ve.Msg != tc.msg {
				t.Errorf("got  path %q msg %q\nwant path %q msg %q", ve.Path, ve.Msg, tc.path, tc.msg)
			}
			if !strings.HasPrefix(err.Error(), "markup: ") {
				t.Errorf("error %q lacks the markup: prefix", err)
			}
		})
	}
}

// para2 is a paragraph with attributes.
func para2(attrs map[string]string, children ...*Node) *Node { return el("p", attrs, children...) }

func TestValidateRejectsNameInError(t *testing.T) {
	// The Element field and the path name the offender.
	err := Validate(tree(el("quote", nil, para(txt("a")), el("u", nil))))
	var ve *Error
	if !errors.As(err, &ve) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ve.Element != "u" || ve.Path != "/quote[0]/u[1]" {
		t.Errorf("got element %q path %q", ve.Element, ve.Path)
	}
}

func TestValidateErrorPath(t *testing.T) {
	n := tree(el("quote", nil,
		para(txt("a")),
		para(txt("b"), el("b", nil),
			el("url", at("href", "javascript:x")))))
	err := Validate(n)
	if err == nil {
		t.Fatal("want an error")
	}
	if got, want := err.Error(), "markup: /quote[0]/p[1]/url[2]: href not allowed"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	var ve *Error
	if !errors.As(err, &ve) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ve.Element != "url" || ve.Path != "/quote[0]/p[1]/url[2]" {
		t.Errorf("got element %q path %q", ve.Element, ve.Path)
	}
}

func TestValidateErrorQuotesHostileNames(t *testing.T) {
	name := "x\n\x1b[31m" + strings.Repeat("y", 500)
	for label, n := range map[string]*Node{
		"element": tree(el(name, nil)),
		"attr":    tree(para(el("b", at(name, "x")))),
	} {
		err := Validate(n)
		if err == nil {
			t.Fatalf("%s: want an error", label)
		}
		if len(err.Error()) > 200 || strings.ContainsAny(err.Error(), "\n\x1b") {
			t.Errorf("%s: error leaks the raw name: %q", label, err)
		}
	}
}

func TestValidateFirstErrorIsStable(t *testing.T) {
	n := tree(para(el("b", at("z", "1", "a", "2", "m", "3"))))
	first := Validate(n).Error()
	for range 50 {
		if got := Validate(n).Error(); got != first {
			t.Fatalf("error changed between runs: %q vs %q", first, got)
		}
	}
	if !strings.HasSuffix(first, "unknown attribute a") {
		t.Errorf("want the first attribute in sorted order, got %q", first)
	}
}

func TestValidateDeepTree(t *testing.T) {
	const nest = "element nesting deeper than 32"
	reject := func(name string, n *Node, msg string) {
		t.Helper()
		err := Validate(tree(n))
		var ve *Error
		if !errors.As(err, &ve) {
			t.Errorf("%s: want an *Error, got %v", name, err)
			return
		}
		if ve.Msg != msg {
			t.Errorf("%s: got msg %q, want %q", name, ve.Msg, msg)
		}
	}
	reject("33 nested", chain(33), nest)
	reject("33 nested list chain", listChain(33), nest)
	reject("11 nested quote", quotes(11), "quote nesting deeper than 10")

	// Quotes count along the path, not across the tree: elements between still count.
	deep := el("quote", nil)
	for range 10 {
		deep = el("quote", nil, el("list", nil, el("li", nil, deep)))
	}
	reject("11 quotes with list and li between", deep, "quote nesting deeper than 10")

	// A childless element too deep is still too deep.
	cur := el("img", at("src", "https://a.example/x.png"))
	for range 31 {
		cur = el("b", nil, cur)
	}
	reject("img at depth 33", para(cur), nest)

	// The 32nd element is the last one allowed.
	if err := Validate(tree(chain(32))); err != nil {
		t.Errorf("32 nested: %v", err)
	}
}

func TestValidateHugeChain(t *testing.T) {
	const n = 100_000
	cur := el("b", nil, txt("x"))
	for i := 1; i < n; i++ {
		cur = el("b", nil, cur)
	}
	root := tree(para(cur))
	start := time.Now()
	err := Validate(root)
	elapsed := time.Since(start)
	var ve *Error
	if !errors.As(err, &ve) || ve.Msg != "element nesting deeper than 32" {
		t.Fatalf("100000-deep chain: got %v, want the nesting error", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("took %v, want under 100ms", elapsed)
	}
}

func TestValidateCycleAndSharing(t *testing.T) {
	const dup = "node appears more than once in the tree"
	// A cycle is a node that appears twice: the shared-node check stops it at
	// depth 2, long before the depth cap.
	b := el("b", nil)
	b.Children = []*Node{b}
	err := Validate(tree(para(b)))
	var ve *Error
	if !errors.As(err, &ve) || ve.Msg != dup || ve.Path != "/p[0]/b[0]/b[0]" {
		t.Errorf("cycle: got %v, want %q at /p[0]/b[0]/b[0]", err, dup)
	}
	// A node shared twice would let a small input expand without limit.
	shared := el("b", nil, txt("x"))
	err = Validate(tree(para(shared, shared)))
	if !errors.As(err, &ve) || ve.Msg != dup {
		t.Errorf("shared node: got %v", err)
	}
	// A chain of doubling shares must fail at once, not after 2^30 visits.
	cur := el("b", nil, txt("x"))
	for range 30 {
		cur = el("b", nil, cur, cur)
	}
	start := time.Now()
	if err := Validate(tree(para(cur))); !errors.As(err, &ve) || ve.Msg != dup {
		t.Errorf("doubling shares: got %v, want %q", err, dup)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("doubling shares took %v", time.Since(start))
	}
}

func TestValidateWide(t *testing.T) {
	kids := make([]*Node, 200_000)
	for i := range kids {
		kids[i] = txt("x")
	}
	if err := Validate(tree(para(kids...))); err != nil {
		t.Fatalf("wide tree: %v", err)
	}
}

func TestCheckURLs(t *testing.T) {
	linkOK := []string{
		"https://a.example/x", "http://a.example", "HTTPS://A.EXAMPLE/p?q=1&amp;r=2#f",
		"/t/5", "/files/a.png", "/", "/a?b=c#d", " /t/5 ",
		"mailto:a@b.example", "MAILTO:a@b.example", "mailto:a.b+c@example.com",
		"/%5Cevil", "/%2F/evil", "/%5C%5Cevil",
	}
	linkBad := []string{
		"//a.example", "/\\a", "javascript:x", "mailto:", "mailto:a", "ftp://a.example",
		"data:image/png;base64,x", "", " ", "a", "http://", "http:a.example", "//",
		"/&#47;a", "/&bsol;a", "/a b", "/a\tb", "/a\x00", "https://a.example/a b",
		"mailto:a@b@c", "mailto:a b@c.example", "mailto:a@b.example\nx", "mailto:a<@b.example",
		"mailto:\x01a@b.example", "http:/x", "https:evil", "https:/evil",
		"mailto:a@b.example?subject=x", "mailto:a@b.example%0D%0ABcc%3Ax", "mailto:a%40c@b.example",
		"mailto:a@b.example#x", "mailto:a@b.example&#63;cc=x", "mailto:/a@b", "mailto:a@b.example&amp;cc=x",
		"https://a.example/\u200bx", "https://a.example/a\u00adb", "/\u202ex", "/a\ufeffb", "/" + strings.Repeat("a", 9000),
	}
	for _, s := range linkOK {
		if !CheckLinkURL(s) {
			t.Errorf("CheckLinkURL(%q) = false, want true", s)
		}
	}
	for _, s := range linkBad {
		if CheckLinkURL(s) {
			t.Errorf("CheckLinkURL(%q) = true, want false", s)
		}
	}
	imageOK := []string{"https://a.example/x", "http://a.example/x.png", "/t/5", "/files/a.png", "/"}
	imageBad := append([]string{}, linkBad...)
	imageBad = append(imageBad, "mailto:a@b.example", "MAILTO:a@b.example", "mailto:a.b+c@example.com")
	for _, s := range imageOK {
		if !CheckImageURL(s) {
			t.Errorf("CheckImageURL(%q) = false, want true", s)
		}
	}
	for _, s := range imageBad {
		if CheckImageURL(s) {
			t.Errorf("CheckImageURL(%q) = true, want false", s)
		}
	}
}

func TestIsEmbed(t *testing.T) {
	for _, name := range []string{"youtube", "vimeo", "tweet", "instagram", "reddit", "tiktok", "threads", "spotify", "soundcloud"} {
		if !IsEmbed(name) {
			t.Errorf("IsEmbed(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "p", "url", "img", "YouTube", "bluesky", "youtube ", "pre"} {
		if IsEmbed(name) {
			t.Errorf("IsEmbed(%q) = true, want false", name)
		}
	}
}

func TestSchemaNamesMatchRules(t *testing.T) {
	// Every name in the schema has a rule, and the rules hold nothing else.
	want := map[string]bool{}
	for _, n := range schemaNames {
		want[n] = true
		if elemRules[n] == nil {
			t.Errorf("no rule for %s", n)
		}
	}
	for n := range elemRules {
		if !want[n] {
			t.Errorf("rule for %s, which is not in the schema", n)
		}
	}
}

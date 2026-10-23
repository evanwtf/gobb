package bbcode

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateAccepts(t *testing.T) {
	tests := map[string]*Node{
		"empty root": tree(),
		"every kind": everyKindTree(),
		"b":          tree(el("b", nil, txt("x"))),
		"i":          tree(el("i", nil, txt("x"))),
		"u":          tree(el("u", nil, txt("x"))),
		"shout":      tree(el("shout", nil)),
		"comic":      tree(el("comic", nil, el("b", nil))),
		"quote bare": tree(el("quote", nil, txt("x"))),
		"quote full": tree(el("quote", at("author", strings.Repeat("a", 255), "post", "1", "time", "9999999999999999999", "user", "0"))),
		"code":       tree(el("code", nil, txt("a"), txt("b"))),
		"list":       tree(el("list", nil, el("li", nil))),
		"list types": tree(
			el("list", at("type", "1")), el("list", at("type", "a")), el("list", at("type", "A")),
			el("list", at("type", "i")), el("list", at("type", "I")), el("list", at("type", "disc")),
			el("list", at("type", "circle")), el("list", at("type", "square"))),
		"list blanks":  tree(el("list", nil, txt(" \r\n\t"), el("li", nil, txt("x")), txt("\n"))),
		"url":          tree(el("url", at("href", "http://example.com"), txt("x"))),
		"url upper":    tree(el("url", at("href", "HTTPS://EXAMPLE.com/p?q=1&amp;r=2#f"))),
		"url nested b": tree(el("url", at("href", "https://example.com"), el("b", nil, el("img", at("src", "https://example.com/a.png"))))),
		"email":        tree(el("email", at("address", "a.b+c@example.com"))),
		"img":          tree(el("img", at("src", "https://example.com/a.png"))),
		"color hex3":   tree(el("color", at("value", "#abc"), txt("x"))),
		"color hex6":   tree(el("color", at("value", "#AbCdEf"), txt("x"))),
		"color name":   tree(el("color", at("value", "red"), txt("x"))),
		"size min":     tree(el("size", at("value", "50"), txt("x"))),
		"size max":     tree(el("size", at("value", "200"), txt("x"))),
		"attachment":   tree(el("attachment", at("index", "0"))),
		"smiley":       tree(el("smiley", at("code", ":-)"))),
		"spoiler":      tree(el("spoiler", nil, txt("x"))),
		"spoiler lbl":  tree(el("spoiler", at("label", strings.Repeat("é", 100)), txt("x"))),
		"youtube":      tree(el("youtube", at("id", "dQw4w9WgXcQ"))),
		"vimeo":        tree(el("vimeo", at("id", "1"))),
		"tweet":        tree(el("tweet", at("id", "12345678901234567890"))),
		"instagram":    tree(el("instagram", at("id", strings.Repeat("a", 64)))),
		"32 deep": func() *Node {
			return tree(nest("b", 32))
		}(),
		"10 quotes": tree(nest("quote", 10)),
		"quotes spread": tree(
			nest("quote", 10), nest("quote", 10)),
		"link after link": tree(
			el("url", at("href", "https://a.example"), txt("x")),
			el("url", at("href", "https://b.example"), txt("y"))),
	}
	for name, n := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Validate(n); err != nil {
				t.Fatalf("want valid, got %v", err)
			}
		})
	}
}

// nest builds depth nested elements of one name, with a text leaf inside.
func nest(name string, depth int) *Node {
	leaf := el(name, nil, txt("x"))
	cur := leaf
	for i := 1; i < depth; i++ {
		cur = el(name, nil, cur)
	}
	return cur
}

func TestValidateRejects(t *testing.T) {
	two := tree(el("b", nil, txt("x")))
	two.Children[0].Children = append(two.Children[0].Children, &Node{Kind: Root})
	tests := []struct {
		name string
		tree *Node
		want string // must appear in the error text
	}{
		{"nil root", nil, "root"},
		{"element as root", el("b", nil), "root"},
		{"text as root", txt("x"), "root"},
		{"unknown kind", &Node{Kind: 9}, "kind"},
		{"root with name", &Node{Kind: Root, Name: "b"}, "root"},
		{"root with attrs", &Node{Kind: Root, Attrs: at("a", "b")}, "root"},
		{"root with text", &Node{Kind: Root, Text: "x"}, "root"},
		{"unknown element", tree(el("script", nil, txt("alert(1)"))), "script"},
		{"empty element name", tree(el("", nil)), "element"},
		{"upper-case element", tree(el("B", nil)), "B"},
		{"unknown attr on b", tree(el("b", at("onclick", "x"))), "b"},
		{"unknown attr on url", tree(el("url", at("href", "https://a.example", "style", "x"))), "url"},
		{"url javascript", tree(el("url", at("href", "javascript:alert(1)"))), "url"},
		{"url mixed case", tree(el("url", at("href", "JaVaScRiPt:x"))), "url"},
		{"url data", tree(el("url", at("href", "data:text/html,x"))), "url"},
		{"url vbscript", tree(el("url", at("href", "vbscript:x"))), "url"},
		{"url entity", tree(el("url", at("href", "&#106;avascript:x"))), "url"},
		{"url hex entity", tree(el("url", at("href", "&#x6A;avascript:x"))), "url"},
		{"url named entity colon", tree(el("url", at("href", "javascript&colon;alert(1)"))), "url"},
		{"url tab", tree(el("url", at("href", "java\tscript:x"))), "url"},
		{"url newline", tree(el("url", at("href", "java\nscript:x"))), "url"},
		{"url NUL", tree(el("url", at("href", "java\x00script:x"))), "url"},
		{"url entity tab", tree(el("url", at("href", "java&Tab;script:x"))), "url"},
		{"url leading ctrl", tree(el("url", at("href", "\x01javascript:x"))), "url"},
		{"url space inside", tree(el("url", at("href", "https://a.example/a b"))), "url"},
		{"url nbsp", tree(el("url", at("href", "https://a.example/a b"))), "url"},
		{"url backslash", tree(el("url", at("href", "https:\\\\a.example"))), "url"},
		{"url ftp", tree(el("url", at("href", "ftp://a.example/"))), "url"},
		{"url mailto", tree(el("url", at("href", "mailto:a@b.example"))), "url"},
		{"url relative", tree(el("url", at("href", "/path"))), "url"},
		{"url scheme relative", tree(el("url", at("href", "//a.example/"))), "url"},
		{"url no host", tree(el("url", at("href", "http://"))), "url"},
		{"url port only", tree(el("url", at("href", "http://:80/"))), "url"},
		{"url opaque", tree(el("url", at("href", "http:a.example"))), "url"},
		{"url empty", tree(el("url", at("href", ""))), "url"},
		{"url blank", tree(el("url", at("href", " \t "))), "url"},
		{"url missing href", tree(el("url", nil)), "url"},
		{"url bad display", tree(el("url", at("href", "https://a.example", "display", "\xff"))), "url"},
		{"url in url", tree(el("url", at("href", "https://a.example"), el("url", at("href", "https://b.example")))), "url"},
		{"url deep in url", tree(el("url", at("href", "https://a.example"), el("b", nil, el("url", at("href", "https://b.example"))))), "url"},
		{"email in url", tree(el("url", at("href", "https://a.example"), el("email", at("address", "a@b.example")))), "email"},
		{"url in email", tree(el("email", at("address", "a@b.example"), el("url", at("href", "https://a.example")))), "url"},
		{"email missing", tree(el("email", nil)), "email"},
		{"email no at", tree(el("email", at("address", "ab.example"))), "email"},
		{"email two at", tree(el("email", at("address", "a@b@c"))), "email"},
		{"email space", tree(el("email", at("address", "a b@c.example"))), "email"},
		{"email colon", tree(el("email", at("address", "javascript:x@c.example"))), "email"},
		{"email control", tree(el("email", at("address", "a\x01@c.example"))), "email"},
		{"email angle", tree(el("email", at("address", "a<@c.example"))), "email"},
		{"email quote", tree(el("email", at("address", `a"@c.example`))), "email"},
		{"email apostrophe", tree(el("email", at("address", "a'@c.example"))), "email"},
		{"email empty local", tree(el("email", at("address", "@c.example"))), "email"},
		{"email empty domain", tree(el("email", at("address", "a@"))), "email"},
		{"img data", tree(el("img", at("src", "data:image/png;base64,x"))), "img"},
		{"img missing src", tree(el("img", nil)), "img"},
		{"img with child", tree(el("img", at("src", "https://a.example/x.png"), txt("x"))), "img"},
		{"size 300", tree(el("size", at("value", "300"), txt("x"))), "size"},
		{"size 49", tree(el("size", at("value", "49"), txt("x"))), "size"},
		{"size 201", tree(el("size", at("value", "201"), txt("x"))), "size"},
		{"size signed", tree(el("size", at("value", "+100"), txt("x"))), "size"},
		{"size float", tree(el("size", at("value", "100.5"), txt("x"))), "size"},
		{"size zero pad", tree(el("size", at("value", "0100"), txt("x"))), "size"},
		{"size huge", tree(el("size", at("value", "99999999999999999999"), txt("x"))), "size"},
		{"size empty", tree(el("size", at("value", ""), txt("x"))), "size"},
		{"size missing", tree(el("size", nil, txt("x"))), "size"},
		{"color injection", tree(el("color", at("value", "red;background:url(x)"), txt("x"))), "color"},
		{"color short", tree(el("color", at("value", "re"), txt("x"))), "color"},
		{"color long", tree(el("color", at("value", strings.Repeat("a", 21)), txt("x"))), "color"},
		{"color upper name", tree(el("color", at("value", "Red"), txt("x"))), "color"},
		{"color hex4", tree(el("color", at("value", "#abcd"), txt("x"))), "color"},
		{"color hex bad", tree(el("color", at("value", "#ggg"), txt("x"))), "color"},
		{"color missing", tree(el("color", nil, txt("x"))), "color"},
		{"li outside list", tree(el("li", nil, txt("x"))), "li"},
		{"li in b in list", tree(el("list", nil, el("b", nil, el("li", nil)))), "b"},
		{"li in li", tree(el("list", nil, el("li", nil, el("li", nil)))), "li"},
		{"li with attr", tree(el("list", nil, el("li", at("a", "b")))), "li"},
		{"list text", tree(el("list", nil, txt("stray"))), "list"},
		{"list element", tree(el("list", nil, el("b", nil))), "b"},
		{"list bad type", tree(el("list", at("type", "x"))), "list"},
		{"list empty type", tree(el("list", at("type", ""))), "list"},
		{"code with element", tree(el("code", nil, el("b", nil))), "code"},
		{"code with attr", tree(el("code", at("a", "b"), txt("x"))), "code"},
		{"smiley with child", tree(el("smiley", at("code", ":)"), txt("x"))), "smiley"},
		{"smiley missing code", tree(el("smiley", nil)), "smiley"},
		{"smiley empty code", tree(el("smiley", at("code", ""))), "smiley"},
		{"smiley long code", tree(el("smiley", at("code", strings.Repeat("a", 33)))), "smiley"},
		{"attachment missing", tree(el("attachment", nil)), "attachment"},
		{"attachment letters", tree(el("attachment", at("index", "x"))), "attachment"},
		{"attachment empty", tree(el("attachment", at("index", ""))), "attachment"},
		{"attachment child", tree(el("attachment", at("index", "1"), txt("x"))), "attachment"},
		{"attachment bad name", tree(el("attachment", at("index", "1", "name", "\xff"))), "attachment"},
		{"spoiler empty label", tree(el("spoiler", at("label", ""), txt("x"))), "spoiler"},
		{"spoiler long label", tree(el("spoiler", at("label", strings.Repeat("a", 101)), txt("x"))), "spoiler"},
		{"spoiler other attr", tree(el("spoiler", at("title", "x"), txt("x"))), "spoiler"},
		{"quote empty author", tree(el("quote", at("author", ""), txt("x"))), "quote"},
		{"quote long author", tree(el("quote", at("author", strings.Repeat("a", 256)), txt("x"))), "quote"},
		{"quote bad post", tree(el("quote", at("post", "12a"), txt("x"))), "quote"},
		{"quote 20 digits", tree(el("quote", at("user", strings.Repeat("9", 20)), txt("x"))), "quote"},
		{"quote empty time", tree(el("quote", at("time", ""), txt("x"))), "quote"},
		{"quote other attr", tree(el("quote", at("name", "x"), txt("x"))), "quote"},
		{"youtube bad id", tree(el("youtube", at("id", "not an id"))), "youtube"},
		{"youtube url id", tree(el("youtube", at("id", "https://youtu.be/dQw4w9WgXcQ"))), "youtube"},
		{"youtube padded id", tree(el("youtube", at("id", " dQw4w9WgXcQ"))), "youtube"},
		{"youtube missing", tree(el("youtube", nil)), "youtube"},
		{"youtube child", tree(el("youtube", at("id", "dQw4w9WgXcQ"), txt("x"))), "youtube"},
		{"vimeo url id", tree(el("vimeo", at("id", "https://vimeo.com/1"))), "vimeo"},
		{"tweet url id", tree(el("tweet", at("id", "https://x.com/a/status/1"))), "tweet"},
		{"instagram url id", tree(el("instagram", at("id", "https://instagram.com/p/abc"))), "instagram"},
		{"b with text field", tree(&Node{Kind: Element, Name: "b", Text: "x"}), "b"},
		{"text with name", tree(&Node{Kind: Text, Name: "b", Text: "x"}), "text"},
		{"text with attrs", tree(&Node{Kind: Text, Attrs: at("a", "b"), Text: "x"}), "text"},
		{"text with children", tree(&Node{Kind: Text, Text: "x", Children: []*Node{txt("y")}}), "text"},
		{"bad utf-8 text", tree(txt("a\xffb")), "text"},
		{"second root", two, "root"},
		{"root child of root", tree(&Node{Kind: Root}), "root"},
		{"nil child", tree(el("b", nil, nil)), "nil"},
		{"nil child of root", &Node{Kind: Root, Children: []*Node{nil}}, "nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.tree)
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if !strings.HasPrefix(err.Error(), "bbcode: ") {
				t.Errorf("error %q lacks the bbcode: prefix", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestValidateErrorPath(t *testing.T) {
	n := tree(el("quote", nil,
		txt("a"), el("b", nil),
		el("url", at("href", "javascript:x"))))
	err := Validate(n)
	if err == nil {
		t.Fatal("want an error")
	}
	if got, want := err.Error(), "bbcode: /quote[0]/url[2]: href scheme not allowed"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	var ve *Error
	if !errors.As(err, &ve) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if ve.Element != "url" || ve.Path != "/quote[0]/url[2]" {
		t.Errorf("got element %q path %q", ve.Element, ve.Path)
	}
}

func TestValidateErrorQuotesHostileNames(t *testing.T) {
	name := "x\n\x1b[31m" + strings.Repeat("y", 500)
	err := Validate(tree(el(name, nil)))
	if err == nil {
		t.Fatal("want an error")
	}
	if len(err.Error()) > 200 || strings.ContainsAny(err.Error(), "\n\x1b") {
		t.Errorf("error leaks the raw name: %q", err)
	}
}

func TestValidateFirstErrorIsStable(t *testing.T) {
	n := tree(el("b", at("z", "1", "a", "2", "m", "3")))
	first := Validate(n).Error()
	for range 50 {
		if got := Validate(n).Error(); got != first {
			t.Fatalf("error changed between runs: %q vs %q", first, got)
		}
	}
}

func TestValidateDeepTree(t *testing.T) {
	if err := Validate(tree(nest("b", 33))); err == nil {
		t.Error("33 nested b: want an error")
	} else if !strings.Contains(err.Error(), "nesting deeper") {
		t.Errorf("33 nested b: error %q does not mention nesting", err)
	}
	if err := Validate(tree(nest("quote", 11))); err == nil {
		t.Error("11 nested quote: want an error")
	} else if !strings.Contains(err.Error(), "quote") {
		t.Errorf("11 nested quote: error %q does not name quote", err)
	}
	// Quotes counted along the path, not across the tree: b between quotes still counts.
	deep := el("quote", nil, txt("x"))
	for range 10 {
		deep = el("quote", nil, el("b", nil, deep))
	}
	if err := Validate(tree(deep)); err == nil {
		t.Error("11 quotes with b between: want an error")
	}
	// A childless element too deep is still too deep.
	leaf := el("img", at("src", "https://a.example/x.png"))
	cur := leaf
	for range 32 {
		cur = el("b", nil, cur)
	}
	if err := Validate(tree(cur)); err == nil {
		t.Error("img at depth 33: want an error")
	}
}

func TestValidateHugeChain(t *testing.T) {
	const n = 100_000
	cur := el("b", nil, txt("x"))
	for i := 1; i < n; i++ {
		cur = el("b", nil, cur)
	}
	root := tree(cur)
	start := time.Now()
	err := Validate(root)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("want an error for a 100000-deep chain")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("took %v, want under 100ms", elapsed)
	}
}

func TestValidateCycleAndSharing(t *testing.T) {
	// A cycle reaches the depth cap, so it fails fast.
	b := el("b", nil)
	b.Children = []*Node{b}
	if err := Validate(tree(b)); err == nil {
		t.Error("cycle: want an error")
	}
	// A node shared twice would let a small input expand without limit.
	shared := el("b", nil, txt("x"))
	if err := Validate(tree(shared, shared)); err == nil {
		t.Error("shared node: want an error")
	}
	// A chain of doubling shares must fail at once, not after 2^30 visits.
	cur := el("b", nil, txt("x"))
	for range 30 {
		cur = el("b", nil, cur, cur)
	}
	start := time.Now()
	if err := Validate(tree(cur)); err == nil {
		t.Error("doubling shares: want an error")
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
	if err := Validate(tree(kids...)); err != nil {
		t.Fatalf("wide tree: %v", err)
	}
}

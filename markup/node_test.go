package markup

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Helpers shared by the tests in this package.

func txt(s string) *Node { return &Node{Kind: Text, Text: s} }

func el(name string, attrs map[string]string, children ...*Node) *Node {
	return &Node{Kind: Element, Name: name, Attrs: attrs, Children: children}
}

func tree(children ...*Node) *Node { return &Node{Kind: Root, Children: children} }

func at(kv ...string) map[string]string {
	m := make(map[string]string, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// everyKindTree holds one element of every name in the schema, each in a place
// where the schema allows it.
func everyKindTree() *Node {
	return tree(
		el("h", at("level", "2"), txt("Title "), el("b", nil, txt("bold"))),
		el("p", nil,
			txt("plain "),
			el("b", nil, txt("bold "), el("i", nil, txt("italic "))),
			el("br", nil),
			el("code", nil, txt("x := 1")),
			el("url", at("href", "https://example.com/a?b=c", "display", "short"), txt("link")),
			el("url", at("href", "/t/5"), txt("site")),
			el("url", at("href", "mailto:a@example.com"), txt("mail")),
			el("email", at("address", "a@example.com"), txt("mail")),
			el("img", at("src", "http://example.com/x.png", "alt", "pic")),
			el("img", at("src", "/files/a.png")),
			el("spoiler", nil, txt("hidden "), el("b", nil, txt("bold"))),
			el("smiley", at("code", ":)")),
		),
		el("hr", nil),
		el("quote", nil,
			el("p", nil, txt("said")),
			el("quote", nil, el("p", nil, txt("inner")))),
		el("list", at("ordered", "true", "start", "3", "tight", "true"),
			el("li", nil, el("p", nil, txt("one"))),
			el("li", nil, el("list", nil, el("li", nil, el("p", nil, txt("two")))))),
		el("pre", at("lang", "c++"), txt("[b]not parsed[/b]")),
		el("pre", nil, txt("no language")),
		el("youtube", at("id", "dQw4w9WgXcQ")),
		el("vimeo", at("id", "76979871")),
		el("tweet", at("id", "20")),
		el("instagram", at("id", "Bk3_xY-9")),
		el("reddit", at("id", "golang/abc123")),
		el("tiktok", at("id", "7123456789012345678")),
		el("threads", at("id", "some.user/C-abc_123")),
		el("spotify", at("id", "track/4uLU6hMCjMI75M1A2tKUQC")),
		el("soundcloud", at("id", "some-user/sets/a_set")),
	)
}

// schemaNames is every element name in the schema.
var schemaNames = []string{
	"p", "h", "hr", "br", "quote", "list", "li", "pre", "code", "b", "i",
	"url", "email", "img", "spoiler", "smiley",
	"youtube", "vimeo", "tweet", "instagram", "reddit", "tiktok", "threads", "spotify", "soundcloud",
}

func TestEveryKindTreeCoversSchema(t *testing.T) {
	seen := map[string]bool{}
	stack := []*Node{everyKindTree()}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Kind == Element {
			seen[n.Name] = true
		}
		stack = append(stack, n.Children...)
	}
	for _, name := range schemaNames {
		if !seen[name] {
			t.Errorf("everyKindTree has no %s element", name)
		}
	}
}

func TestTreeJSONRoundTrip(t *testing.T) {
	in := everyKindTree()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Node
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, &out) {
		t.Fatalf("round trip changed the tree:\n in: %s\nout: %+v", data, out)
	}
	if err := Validate(&out); err != nil {
		t.Fatalf("round-tripped tree does not validate: %v", err)
	}
}

func TestTreeJSONShape(t *testing.T) {
	n := tree(el("p", at("x", "y"), txt("hi")))
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"k":0,"c":[{"k":2,"n":"p","a":{"x":"y"},"c":[{"k":1,"t":"hi"}]}]}`
	if string(data) != want {
		t.Fatalf("got  %s\nwant %s", data, want)
	}
}

func TestTreeJSONEmptyIsNil(t *testing.T) {
	// An empty map or slice and a nil one marshal alike, and both come back nil.
	empty := tree(&Node{Kind: Element, Name: "p", Attrs: map[string]string{}, Children: []*Node{}})
	nilled := tree(&Node{Kind: Element, Name: "p"})
	a, _ := json.Marshal(empty)
	b, _ := json.Marshal(nilled)
	if string(a) != string(b) {
		t.Fatalf("empty and nil marshal differently: %s vs %s", a, b)
	}
	for _, src := range []string{string(a), `{"k":0,"c":[{"k":2,"n":"p","a":{},"c":[]}]}`} {
		out, err := Decode([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(nilled, out) {
			t.Fatalf("from %s got %+v, want the nil form", src, out)
		}
	}
}

func TestTreeJSONRejectsBadKind(t *testing.T) {
	if _, err := Decode([]byte(`{"k":300}`)); err == nil {
		t.Fatal("kind 300 does not fit Kind, want error")
	}
}

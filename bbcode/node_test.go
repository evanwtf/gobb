package bbcode

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

// everyKindTree holds one element of every name in the schema.
func everyKindTree() *Node {
	return tree(
		txt("plain "),
		el("b", nil, txt("bold "), el("i", nil, txt("italic "), el("u", nil, txt("under")))),
		el("shout", nil, txt("LOUD")),
		el("comic", nil, txt("fun")),
		el("quote", at("author", "Ann", "post", "42", "time", "1700000000", "user", "7"),
			txt("said"), el("quote", nil, txt("inner"))),
		el("code", nil, txt("[b]not parsed[/b]")),
		el("list", at("type", "disc"),
			txt("\n"), el("li", nil, txt("one")), txt("\n"), el("li", nil, el("b", nil, txt("two")))),
		el("url", at("href", "https://example.com/a?b=c", "display", "short"), txt("link")),
		el("email", at("address", "a@example.com"), txt("mail")),
		el("img", at("src", "http://example.com/x.png")),
		el("color", at("value", "#ff00aa"), txt("tinted")),
		el("size", at("value", "150"), txt("big")),
		el("attachment", at("index", "1", "name", "f.png")),
		el("smiley", at("code", ":)")),
		el("spoiler", at("label", "Plot"), txt("hidden")),
		el("youtube", at("id", "dQw4w9WgXcQ")),
		el("vimeo", at("id", "76979871")),
		el("tweet", at("id", "20")),
		el("instagram", at("id", "Bk3_xY-9")),
	)
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
	n := tree(el("b", at("x", "y"), txt("hi")))
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"k":0,"c":[{"k":2,"n":"b","a":{"x":"y"},"c":[{"k":1,"t":"hi"}]}]}`
	if string(data) != want {
		t.Fatalf("got  %s\nwant %s", data, want)
	}
}

func TestTreeJSONEmptyIsNil(t *testing.T) {
	// An empty map or slice and a nil one marshal alike, and both come back nil.
	empty := tree(&Node{Kind: Element, Name: "b", Attrs: map[string]string{}, Children: []*Node{}})
	nilled := tree(&Node{Kind: Element, Name: "b"})
	a, _ := json.Marshal(empty)
	b, _ := json.Marshal(nilled)
	if string(a) != string(b) {
		t.Fatalf("empty and nil marshal differently: %s vs %s", a, b)
	}
	for _, src := range []string{string(a), `{"k":0,"c":[{"k":2,"n":"b","a":{},"c":[]}]}`} {
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

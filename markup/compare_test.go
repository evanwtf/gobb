package markup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureSmilies is the smiley list that the round-trip fixtures use.
var fixtureSmilies = []Smiley{
	{Code: ":)", Image: "smile.gif", Alt: "smile"},
	{Code: ":D", Image: "grin.gif", Alt: "grin"},
}

// compareChain builds n nested b elements with one text leaf at the bottom.
func compareChain(n int, leaf string) *Node {
	cur := txt(leaf)
	for i := 0; i < n; i++ {
		cur = el("b", nil, cur)
	}
	return tree(cur)
}

func wantDiffs(t *testing.T, got []Diff, want ...Diff) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diffs\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCompareEqual(t *testing.T) {
	build := func() *Node {
		return tree(
			el("quote", nil, para(el("b", nil, txt("Ann")), txt(" "), el("url", at("href", "/p/1"), txt("wrote")))),
			el("list", at("tight", "true", "ordered", "true"), el("li", nil, para(txt("x")))),
			el("pre", at("lang", "go"), txt("x := 1")),
			el("hr", nil),
		)
	}
	wantDiffs(t, Compare(build(), build()))

	// An empty attribute map and a nil one are the same attribute set.
	a := tree(el("hr", nil))
	b := tree(el("hr", map[string]string{}))
	wantDiffs(t, Compare(a, b))
	wantDiffs(t, Compare(b, a))

	// Two nil trees are equal. A bare node compares like a tree.
	wantDiffs(t, Compare(nil, nil))
	wantDiffs(t, Compare(txt("x"), txt("x")))
}

func TestCompareText(t *testing.T) {
	want := tree(para(txt("one")), el("quote", nil, para(el("b", nil, txt("abc")))))
	got := tree(para(txt("one")), el("quote", nil, para(el("b", nil, txt("abd")))))
	wantDiffs(t, Compare(want, got), Diff{
		Path: "/quote[1]/p[0]/b[0]/#text[0]",
		Want: `"abc"`,
		Got:  `"abd"`,
	})

	// A difference in the white space counts.
	wantDiffs(t, Compare(tree(para(txt("a b"))), tree(para(txt("a  b")))),
		Diff{Path: "/p[0]/#text[0]", Want: `"a b"`, Got: `"a  b"`})
}

func TestCompareAttrs(t *testing.T) {
	// A value differs.
	want := tree(para(el("url", at("href", "/a", "display", "d"), txt("x"))))
	got := tree(para(el("url", at("href", "/b", "display", "d"), txt("x"))))
	wantDiffs(t, Compare(want, got), Diff{
		Path: "/p[0]/url[0]", Want: "display=d,href=/a", Got: "display=d,href=/b",
	})

	// The set differs: one attribute is missing, then one is extra.
	got = tree(para(el("url", at("href", "/a"), txt("x"))))
	wantDiffs(t, Compare(want, got), Diff{
		Path: "/p[0]/url[0]", Want: "display=d,href=/a", Got: "href=/a",
	})
	wantDiffs(t, Compare(got, want), Diff{
		Path: "/p[0]/url[0]", Want: "href=/a", Got: "display=d,href=/a",
	})

	// An attribute against none gives an empty side.
	wantDiffs(t, Compare(tree(el("list", nil)), tree(el("list", at("tight", "true")))),
		Diff{Path: "/list[0]", Want: "", Got: "tight=true"})

	// The list is sorted, whatever the insertion order.
	a := tree(el("img", at("src", "/a.png", "alt", "x")))
	b := tree(el("img", at("alt", "y", "src", "/a.png")))
	wantDiffs(t, Compare(a, b), Diff{Path: "/img[0]", Want: "alt=x,src=/a.png", Got: "alt=y,src=/a.png"})
}

func TestCompareShape(t *testing.T) {
	// A child is missing from got.
	want := tree(para(txt("a")), para(txt("b")))
	got := tree(para(txt("a")))
	wantDiffs(t, Compare(want, got), Diff{Path: "/p[1]", Want: "p", Got: ""})

	// A child is extra in got.
	wantDiffs(t, Compare(got, want), Diff{Path: "/p[1]", Want: "", Got: "p"})

	// A missing text node names its text.
	wantDiffs(t, Compare(tree(para(txt("a"), txt("abc"))), tree(para(txt("a")))),
		Diff{Path: "/p[0]/#text[1]", Want: `text "abc"`, Got: ""})

	// A different element name or kind gives one diff and no walk below it.
	wantDiffs(t, Compare(tree(el("b", nil, txt("x"))), tree(el("i", nil, txt("y")))),
		Diff{Path: "/b[0]", Want: "b", Got: "i"})
	wantDiffs(t, Compare(tree(para(txt("x"))), tree(para(el("br", nil)))),
		Diff{Path: "/p[0]/#text[0]", Want: `text "x"`, Got: "br"})

	// Diffs come in document order, with a missing child in its place.
	want = tree(para(txt("a")), para(txt("b")), para(txt("c")))
	got = tree(para(txt("A")), para(txt("b")))
	wantDiffs(t, Compare(want, got),
		Diff{Path: "/p[0]/#text[0]", Want: `"a"`, Got: `"A"`},
		Diff{Path: "/p[2]", Want: "p", Got: ""},
	)
	got = tree(para(txt("a"), txt("extra")), para(txt("B")), para(txt("c")))
	wantDiffs(t, Compare(want, got),
		Diff{Path: "/p[0]/#text[1]", Want: "", Got: `text "extra"`},
		Diff{Path: "/p[1]/#text[0]", Want: `"b"`, Got: `"B"`},
	)
}

func TestCompareNil(t *testing.T) {
	root := tree(para(txt("x")))
	wantDiffs(t, Compare(nil, root), Diff{Path: "/", Want: "nil", Got: "root"})
	wantDiffs(t, Compare(root, nil), Diff{Path: "/", Want: "root", Got: "nil"})

	// A nil child is not a panic.
	wantDiffs(t, Compare(&Node{Kind: Root, Children: []*Node{nil}}, root),
		Diff{Path: "/nil[0]", Want: "nil", Got: "p"})
	wantDiffs(t, Compare(&Node{Kind: Root, Children: []*Node{nil}}, &Node{Kind: Root, Children: []*Node{nil}}))
}

func TestCompareDeep(t *testing.T) {
	const depth = 100000
	wantDiffs(t, Compare(compareChain(depth, "x"), compareChain(depth, "x")))

	diffs := Compare(compareChain(depth, "x"), compareChain(depth, "y"))
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	d := diffs[0]
	if want := strings.Repeat("/b[0]", depth) + "/#text[0]"; d.Path != want {
		t.Errorf("path has %d bytes, want %d", len(d.Path), len(want))
	}
	if d.Want != `"x"` || d.Got != `"y"` {
		t.Errorf("diff = %#v", d)
	}
}

func TestCompareCycleEnds(t *testing.T) {
	a := el("b", nil)
	a.Children = []*Node{a}
	b := el("b", nil)
	b.Children = []*Node{b}
	wantDiffs(t, Compare(tree(a), tree(b)))
}

// loadFixture reads one round-trip fixture pair.
func loadFixture(t *testing.T, base string) (src string, tree *Node) {
	t.Helper()
	md, err := os.ReadFile(base + ".md")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(base + ".json")
	if err != nil {
		t.Fatalf("fixture %s has a source and no tree: %v", base, err)
	}
	tree, err = Decode(data)
	if err != nil {
		t.Fatalf("%s.json: %v", base, err)
	}
	return string(md), tree
}

// The fixtures are written by hand from the element rules. Each pair is the
// Markdown that a converter writes and the tree it expects from it.
func TestRoundTripFixtures(t *testing.T) {
	sources, err := filepath.Glob(filepath.Join("testdata", "roundtrip", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	trees, _ := filepath.Glob(filepath.Join("testdata", "roundtrip", "*.json"))
	if len(sources) < 11 || len(sources) != len(trees) {
		t.Fatalf("found %d sources and %d trees, want at least 11 matching pairs", len(sources), len(trees))
	}
	opt := Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: fixtureSmilies}
	for _, md := range sources {
		base := strings.TrimSuffix(md, ".md")
		t.Run(filepath.Base(base), func(t *testing.T) {
			src, want := loadFixture(t, base)
			if err := Validate(want); err != nil {
				t.Fatalf("the fixture tree is not valid: %v", err)
			}
			got := Parse(src, opt)
			for _, d := range Compare(want, got) {
				t.Errorf("%s: want %s, got %s", d.Path, d.Want, d.Got)
			}
			if t.Failed() {
				t.Logf("source: %q\ngot: %s", src, js(got))
			}
		})
	}
}

// A fixture must be able to fail: a changed tree gives a diff.
func TestRoundTripFixtureCanFail(t *testing.T) {
	opt := Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: fixtureSmilies}
	src, want := loadFixture(t, filepath.Join("testdata", "roundtrip", "spoiler"))
	want.Children[0].Children[1].Name = "b"
	if len(Compare(want, Parse(src, opt))) == 0 {
		t.Error("a changed fixture tree gives no diff")
	}
}

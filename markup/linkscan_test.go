package markup

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
)

func TestDestinationLen(t *testing.T) {
	tests := []struct {
		line  string
		limit int
		want  int
	}{
		{"", 100, 0},
		{"   ", 100, 0},
		{"b)", 100, 1},
		{"/a/b?c=d) tail", 100, 8},
		{"a(b)c) x", 100, 5}, // balanced parentheses stay in the destination
		{"a b", 100, 1},      // white space ends it
		{"  a b", 100, 1},    // white space before the destination does not count
		{"a\\)b) c", 100, 4}, // an escaped ")" does not close
		{"abcdef", 100, 6},   // the end of the line ends it
		{"abcdef", 3, 3},     // the limit stops the count
		{"<a b>c", 100, 5},   // "<...>" may hold white space
		{"<a\\>b>c", 100, 6}, // an escaped ">" does not close
		{"<abc", 100, 4},     // an open "<" reads to the end of the line
		{"<abcdef", 3, 3},    // and the limit stops it
		{"a\\", 100, 2},      // a lone backslash at the end
		{"((((((x", 100, 7},  // opened parentheses never close here
		{"a\tb", 100, 1},
	}
	for _, tc := range tests {
		if got := destinationLen([]byte(tc.line), tc.limit); got != tc.want {
			t.Errorf("destinationLen(%q, %d) = %d, want %d", tc.line, tc.limit, got, tc.want)
		}
	}
}

// TestLinkBudgetKeepsNaturalLinks parses long lines of ordinary links, which add
// up to about the size of the source, and requires every link to stay a link.
func TestLinkBudgetKeepsNaturalLinks(t *testing.T) {
	for name, unit := range map[string]string{
		"short":          "[a](/b) ",
		"no space":       "[a](/b)",
		"long address":   "[a](https://example.com/" + strings.Repeat("x", 500) + ") ",
		"parentheses":    "[a](/b(c)d)",
		"angle":          "[a](</b c>) ",
		"title":          "[a](/b \"t\") ",
		"image":          "![a](/b.png) ",
		"reference":      "[a][r] ",
		"nested address": "[a](/b?c=(d(e)))",
	} {
		t.Run(name, func(t *testing.T) {
			const n = 4000
			src := strings.Repeat(unit, n) + "\n\n[r]: /ref\n"
			tree := Parse(src, allOn)
			links := countLinks(tree)
			if links != n {
				t.Errorf("%d links in %d bytes, want %d", links, len(src), n)
			}
		})
	}
}

// countLinks counts the url and img elements of the tree.
func countLinks(n *Node) int {
	c := 0
	if n.Kind == Element && (n.Name == "url" || n.Name == "img") {
		c++
	}
	for _, k := range n.Children {
		c += countLinks(k)
	}
	return c
}

// TestLinkBudgetEndsAtTheEnd checks the other side: when the budget is gone, the
// text stays text, the tree is valid, and a good link after the run is text too.
func TestLinkBudgetEndsAtTheEnd(t *testing.T) {
	src := strings.Repeat("[a](", 30000) + " [ok](/fine)"
	tree := Parse(src, allOn)
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	got := allText(tree)
	if !strings.Contains(got, "[a]([a](") {
		t.Errorf("the run is not kept as text: %.60q", got)
	}
	if !strings.HasSuffix(got, "[ok](/fine)") {
		t.Errorf("the link after the run is not kept as text: %.60q", got[max(0, len(got)-60):])
	}
	if has(tree, "url") {
		t.Error("the tree holds a url element: the link after the run should be text")
	}
	// The same link after a short run is a link: the budget is what ends it.
	if short := Parse(strings.Repeat("[a](", 20)+" [ok](/fine)", allOn); !has(short, "url") {
		t.Error("a link after a short run is not a link")
	}
}

// TestBoundedLinkParserChangesNothingBelowTheBudget compares the bounded parser
// with goldmark's own on random link-like text, which stays far below the budget.
func TestBoundedLinkParserChangesNothingBelowTheBudget(t *testing.T) {
	tokens := []string{"[", "]", "(", ")", "![", "](", "<", ">", " ", "\n", "\\", "\"", "'", "a", "b", "/", ":", "http://x.example/", "&amp;", "||", "*", "`", "[a](b)", "[a](<b c>)", "[a][r]", "\t"}
	bounded := engines
	var plain [4]goldmark.Markdown
	for i := range plain {
		plain[i] = newEngineWith(i >= 2, i%2 == 1, false)
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20000; i++ {
		var sb strings.Builder
		for j, k := 0, 1+rng.Intn(24); j < k; j++ {
			sb.WriteString(tokens[rng.Intn(len(tokens))])
		}
		src := sb.String() + "\n\n[r]: /ref"
		want := func() string {
			engines = plain
			defer func() { engines = bounded }()
			return js(Parse(src, allOn))
		}()
		if got := js(Parse(src, allOn)); got != want {
			t.Fatalf("source %q\nbounded: %s\nplain:   %s", src, got, want)
		}
	}
}

// TestBoundedInlineParsersWrapsTheLinkParser fails unless exactly one parser is
// wrapped. The lookup is by value, so a goldmark upgrade that changes the value
// would otherwise remove the bound without an error.
func TestBoundedInlineParsersWrapsTheLinkParser(t *testing.T) {
	n := 0
	for _, pv := range boundedInlineParsers() {
		if _, ok := pv.Value.(boundedLinkParser); ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("wrapped %d parsers, want 1", n)
	}
}

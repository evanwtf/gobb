package markup

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
)

// delimShapes are the sources that make goldmark's emphasis matching slow: two
// runs of delimiters that never pair, so every closer walks back over every
// opener. Each takes a unit and repeats it to a size in bytes.
var delimShapes = map[string]string{
	"*_a":         "*_a",
	"_*a":         "_*a",
	"**_a":        "**_a",
	"_**a":        "_**a",
	"*a_":         "*a_",
	"[*_":         "[*_",
	"`*_":         "`*_",
	">*_":         ">*_",
	"||*_a":       "||*_a",
	"*_a|":        "*_a|",
	"*_a a_":      "*_a a_",
	"mixed marks": "*_a*",
	// The autolink parser reads a run of address characters from every trigger.
	"~a":   "~a",
	"a~":   "a~",
	"_a":   "_a",
	"-*_a": "-*_a",
	"|*_a": "|*_a",
}

// repeatTo repeats unit until the result is size bytes long.
func repeatTo(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit)+1)[:size]
}

// randomMix returns size bytes of random text from the characters that start
// markup.
func randomMix(seed int64, size int) string {
	const alphabet = "*_[]()`~>!"
	rng := rand.New(rand.NewSource(seed))
	b := make([]byte, size)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

// TestParseDelimiterShapesAreLinear covers the quadratic parse of mixed emphasis
// delimiters: a body of "*_a" repeated to 60,000 bytes took 1.6 s. Each shape
// must parse in under 50 ms (ten times that under the race detector), and
// twice the input must take less than three times as long.
func TestParseDelimiterShapesAreLinear(t *testing.T) {
	type shape struct {
		name string
		gen  func(size int) string
	}
	var shapes []shape
	for name, unit := range delimShapes {
		shapes = append(shapes, shape{name, func(size int) string { return repeatTo(unit, size) }})
	}
	for seed := int64(1); seed <= 4; seed++ {
		shapes = append(shapes, shape{"random mix", func(size int) string { return randomMix(seed, size) }})
	}
	// Openers of one kind, closers of another, and one pair at the end.
	shapes = append(shapes, shape{"closers walk past openers", func(size int) string {
		return repeatTo("*_a", size-8) + "a_ a_ a_"
	}})
	// A long run of address characters, then a "@" and a long domain with no dot,
	// which is no address: every trigger reads the run and the domain.
	shapes = append(shapes, shape{"long address", func(size int) string {
		return repeatTo("~a", size/2) + "@" + repeatTo("b", size/2-1)
	}})
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			// The ratio is a wall-clock measure, and a busy machine (the CI runner
			// runs other packages at the same time) can slow one of the two runs.
			// A quadratic parse gives a ratio of 4 on every attempt, so the test
			// fails only when all three attempts show a ratio of 3 or more.
			var small, large time.Duration
			linear := false
			for range 3 {
				small = bestOf(5, sh.gen(60000))
				large = bestOf(5, sh.gen(120000))
				t.Logf("60,000 bytes: %v, 120,000 bytes: %v", small, large)
				if limit := 50 * time.Millisecond * raceSlowdown; small > limit {
					t.Errorf("60,000 bytes took %v, want under %v", small, limit)
					return
				}
				if large < 3*max(small, 2*time.Millisecond) {
					linear = true
					break
				}
			}
			if !linear {
				t.Errorf("120,000 bytes took %v and 60,000 took %v: three times as long or more", large, small)
			}
		})
	}
}

// TestDelimiterBudgetKeepsNaturalPosts parses long paragraphs of ordinary
// emphasis, spoilers and addresses, which cost far less than the budget, and
// requires every one to stay markup.
func TestDelimiterBudgetKeepsNaturalPosts(t *testing.T) {
	const n = 4000
	for name, tc := range map[string]struct {
		unit string
		node string
	}{
		"italic":  {"*a* ", "i"},
		"bold":    {"**b** ", "b"},
		"under":   {"_c_ ", "i"},
		"spoiler": {"||d|| ", "spoiler"},
		"prose":   {"Some words with *emphasis*, **bold** and a snake_case_name in them. ", "b"},
		"address": {"mail a@example.com now ", "email"},
		"link":    {"see https://example.com/a now ", "url"},
	} {
		t.Run(name, func(t *testing.T) {
			// Paragraphs of 50 units, as a long post has them.
			para := strings.Repeat(tc.unit, 50)
			tree := Parse(strings.Repeat(para+"\n\n", n/50), allOn)
			if got := countNamed(tree, tc.node); got != n {
				t.Errorf("%d %q elements in %d bytes, want %d", got, tc.node, n*len(tc.unit), n)
			}
		})
	}
}

// TestDelimiterBudgetHoldsALongParagraph: a single paragraph of 500 pairs of
// emphasis, 3,000 bytes of nothing but markup, keeps every pair.
func TestDelimiterBudgetHoldsALongParagraph(t *testing.T) {
	tree := Parse(strings.Repeat("*a* ", 500), allOn)
	if got := countNamed(tree, "i"); got != 500 {
		t.Errorf("%d italic elements in one paragraph of 500 pairs, want 500", got)
	}
}

// countNamed counts the elements of the tree with the name.
func countNamed(n *Node, name string) int {
	c := 0
	if n.Kind == Element && n.Name == name {
		c++
	}
	for _, k := range n.Children {
		c += countNamed(k, name)
	}
	return c
}

// TestDelimiterBudgetEndsAtTheEnd checks the other side: when the budget is gone,
// the rest of the delimiters stay text, the tree is valid, and the text is all
// there. The same tail after a short run is markup: the budget is what ends it.
func TestDelimiterBudgetEndsAtTheEnd(t *testing.T) {
	const tail = " *last* ||spoiler|| x@example.com"
	for name, tc := range map[string]struct {
		run  string
		kept bool // the tail is markup
	}{
		"long run":  {strings.Repeat("*_a", 40000), false},
		"short run": {strings.Repeat("*_a", 20), true},
	} {
		t.Run(name, func(t *testing.T) {
			before := Parse("*first* "+tc.run, allOn)
			tree := Parse("*first* "+tc.run+tail, allOn)
			if err := Validate(tree); err != nil {
				t.Fatal(err)
			}
			if text := allText(tree); !strings.Contains(text, tc.run[:60]) && !tc.kept {
				t.Errorf("the run is not kept as text: %.80q", text)
			}
			italics := countNamed(tree, "i") - countNamed(before, "i")
			if tc.kept {
				if italics != 1 || !has(tree, "spoiler") || !has(tree, "email") {
					t.Errorf("the tail is not markup: %d italics\n%s", italics, js(tree))
				}
				return
			}
			if italics != 0 || has(tree, "spoiler") || has(tree, "email") {
				t.Errorf("the tail is markup after the budget: %d italics", italics)
			}
			if text := allText(tree); !strings.HasSuffix(text, tail[1:]) {
				t.Errorf("the tail is not kept as text: %.80q", text[max(0, len(text)-80):])
			}
		})
	}
}

// TestBoundedParsersChangeNothingBelowTheBudget compares the bounded parsers with
// the unbounded ones on random text full of delimiters and addresses, which stays
// far below the budget.
func TestBoundedParsersChangeNothingBelowTheBudget(t *testing.T) {
	tokens := []string{"*", "**", "_", "__", "|", "||", "~", "a", "b", " ", "\n", "\n\n", "\\", "[", "]", "(", ")", "`", ">", "!",
		"@", ".", "-", "a@b.c", "http://x.example/y", "www.x.example", "*a*", "_b_", "||c||", "**d**", "snake_case", "(*", "*)", "[a](b)"}
	bounded := engines
	var plain [4]goldmark.Markdown
	for i := range plain {
		plain[i] = newEngineWith(i >= 2, i%2 == 1, false)
	}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 30000; i++ {
		var sb strings.Builder
		for j, k := 0, 1+rng.Intn(30); j < k; j++ {
			sb.WriteString(tokens[rng.Intn(len(tokens))])
		}
		src := sb.String()
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

// TestBoundedEnginesWrapTheDelimiterParsers fails unless the emphasis parser, the
// spoiler parser and the autolink parser are each wrapped once. The emphasis
// lookup is by value, so a goldmark upgrade that changes the value would
// otherwise remove the bound without an error.
func TestBoundedEnginesWrapTheDelimiterParsers(t *testing.T) {
	n := 0
	for _, pv := range boundedInlineParsers() {
		if _, ok := pv.Value.(boundedDelimiterParser); ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("wrapped %d emphasis parsers, want 1", n)
	}
	// The spoiler and the autolink parsers are added by extensions. A shape for
	// each is in the scaling test; here the budget key shows the wrapper ran.
	for name, src := range map[string]string{
		"spoiler":  "||a||",
		"autolink": "a@b.cc",
	} {
		if !has(Parse(src, allOn), map[string]string{"spoiler": "spoiler", "autolink": "email"}[name]) {
			t.Errorf("%s: no markup from %q", name, src)
		}
	}
}

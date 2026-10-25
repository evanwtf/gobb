package markup

import (
	"math/rand"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
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

// shape is a source that a test builds to a size in bytes.
type shape struct {
	name string
	gen  func(size int) string
}

// codeSpanLadder is runs of 1, 2, ... 200 backticks, each followed by a space,
// then a newline and lines of "a" to the size. Each run finds no closer of its
// length, so each reads every later line of the paragraph.
func codeSpanLadder(size int) string {
	var sb strings.Builder
	for k := 1; k <= 200; k++ {
		sb.WriteString(strings.Repeat("`", k))
		sb.WriteByte(' ')
	}
	sb.WriteByte('\n')
	return sb.String() + repeatTo("a\n", max(0, size-sb.Len()))
}

// ledShape is a first line and then the unit, repeated to the size.
func ledShape(first, unit string, size int) string {
	return first + repeatTo(unit, max(0, size-len(first)))
}

// hostileShapes are the shapes that goldmark parses in more than linear time
// outside the delimiter list: the ladder of code spans, and a list of link
// reference definitions, which the paragraph transformer removes one at a time,
// with a copy of the lines that remain.
func hostileShapes() []shape {
	return []shape{
		{"code span ladder", codeSpanLadder},
		{"link reference definitions", func(size int) string { return repeatTo("[x]:\n", size) }},
		{"link reference definitions with a destination", func(size int) string { return repeatTo("[x]: /u\n", size) }},
		{"link reference definitions with titles", func(size int) string { return repeatTo("[x]: /u 't'\n", size) }},
		{"indented link reference definitions", func(size int) string { return repeatTo("   [x]:\n", size) }},
		// A definition may follow any white space: goldmark skips tabs and any
		// number of spaces before the "[", and the first line must not be indented.
		{"definitions behind a tab", func(size int) string { return ledShape("[x]: /u\n", "\t[x]: /u\n", size) }},
		{"definitions behind a tab, no destination", func(size int) string { return ledShape("[x]:\n", "\t[x]:\n", size) }},
		{"definitions behind a tab, in a quote", func(size int) string { return ledShape("> [x]:\n", "\t[x]:\n", size) }},
		{"definitions behind five spaces", func(size int) string { return ledShape("[x]: /u\n", "     [x]: /u\n", size) }},
		{"definitions behind eight spaces, in a list", func(size int) string { return ledShape("- [x]: /u\n", "        [x]: /u\n", size) }},
		// A definition may start in the middle of a line: after a title on the
		// line below its destination, goldmark parses the next "[" on that line.
		{"definitions after a title", func(size int) string { return ledShape("[x]: /u\n", "\"t\" [x]: /u\n", size) }},
		// The link parser reads the text of each bracket, and goldmark finds the
		// line of that text by a walk back from the last line.
		{"shortcut references", func(size int) string { return repeatTo("[x]\n", size) }},
		{"full references", func(size int) string { return repeatTo("[x][y]\n", size) }},
		{"collapsed references", func(size int) string { return repeatTo("[x][]\n", size) }},
		{"image references", func(size int) string { return repeatTo("![x]\n", size) }},
	}
}

// TestHostileShapesAllocateLittle: one parse of a 60,000-byte body allocates
// under 64 MiB. The code span ladder took 679 MiB.
func TestHostileShapesAllocateLittle(t *testing.T) {
	const limit = 64 << 20
	for _, sh := range hostileShapes() {
		t.Run(sh.name, func(t *testing.T) {
			src := sh.gen(60000)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			Parse(src, allOn)
			runtime.ReadMemStats(&after)
			got := after.TotalAlloc - before.TotalAlloc
			t.Logf("%d bytes allocated, %d allocations", got, after.Mallocs-before.Mallocs)
			if got > limit {
				t.Errorf("one parse allocated %d MiB, want under %d MiB", got>>20, limit>>20)
			}
		})
	}
}

// TestParseDelimiterShapesAreLinear covers the quadratic parse of mixed emphasis
// delimiters: a body of "*_a" repeated to 60,000 bytes took 1.6 s. Each shape
// must parse in under 50 ms (ten times that under the race detector), and
// twice the input must take less than three times as long.
func TestParseDelimiterShapesAreLinear(t *testing.T) {
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
	shapes = append(shapes, hostileShapes()...)
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

// TestSpanAndDefinitionBudgetsKeepNaturalPosts: many code spans, and a long list
// of link reference definitions, stay markup.
func TestSpanAndDefinitionBudgetsKeepNaturalPosts(t *testing.T) {
	tree := Parse(strings.Repeat("Use `a` and ``b`c`` here.\n\n", 3000), allOn)
	if got := countNamed(tree, "code"); got != 6000 {
		t.Errorf("%d code elements in 3,000 paragraphs, want 6000", got)
	}
	var defs strings.Builder
	for i := range 800 {
		defs.WriteString("[k" + strconv.Itoa(i) + "]: /u" + strconv.Itoa(i) + "\n")
	}
	tree = Parse(defs.String()+"\n[k799] and [k0]", allOn)
	if got := countNamed(tree, "url"); got != 2 {
		t.Errorf("%d links from 800 definitions, want 2", got)
	}
	// The definitions in one paragraph with text after them.
	tree = Parse(defs.String()+"[k799] and [k0]", allOn)
	if got := countNamed(tree, "url"); got != 2 {
		t.Errorf("%d links from 800 definitions in the paragraph of the text, want 2", got)
	}
}

// TestRefusedParagraphLeavesTheBudget: a long paragraph of "]:" lines costs more
// than the definition budget and keeps its definitions as text. A later,
// short paragraph with a definition and a link that uses it still works.
func TestRefusedParagraphLeavesTheBudget(t *testing.T) {
	log := strings.Repeat("[12:00]: name: text\n", 2500)
	tree := Parse("[a]: /first\n"+log+"\n[k]: /second\nsee [k] and [a]", allOn)
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	if got := hrefs(tree); len(got) != 1 || got[0] != "/second" {
		t.Errorf("links after the refused paragraph = %q, want [/second]", got)
	}
}

// TestLogWithoutLabelEndsKeepsItsDefinition: a pasted log of "[12:00] name:"
// lines holds no "]:", so it costs nothing, and the definition before it in the
// same paragraph stays a definition.
func TestLogWithoutLabelEndsKeepsItsDefinition(t *testing.T) {
	log := strings.Repeat("[12:00] name: text\n", 2500)
	tree := Parse("[a]: /first\n"+log+"\nsee [a]", allOn)
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	if got := hrefs(tree); len(got) != 1 || got[0] != "/first" {
		t.Errorf("links after the log = %q, want [/first]", got)
	}
}

// TestDefinitionLabelOnTwoLines: a label that spans two lines holds its "]:" on
// the second line. The count of "]:" sees it, and the definition resolves.
func TestDefinitionLabelOnTwoLines(t *testing.T) {
	tree := Parse("[a\nb]: /two\n\nsee [a b]", allOn)
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	if got := hrefs(tree); len(got) != 1 || got[0] != "/two" {
		t.Errorf("links = %q, want [/two]", got)
	}
}

// hrefs returns the targets of the links of the tree, in order.
func hrefs(n *Node) []string {
	var got []string
	if n.Kind == Element && n.Name == "url" {
		got = append(got, n.Attrs["href"])
	}
	for _, c := range n.Children {
		got = append(got, hrefs(c)...)
	}
	return got
}

// TestSpanAndDefinitionBudgetsEndAtTheEnd: past the budget, a run of backticks
// stays text and a definition stays text, and the tree is valid.
func TestSpanAndDefinitionBudgetsEndAtTheEnd(t *testing.T) {
	ladder := codeSpanLadder(60000)
	tree := Parse(ladder+"\n\nthen `last` here", allOn)
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	if got := countNamed(tree, "code"); got != 0 {
		// The ladder itself is a paragraph of text; the last span follows the end
		// of the budget and stays text.
		t.Errorf("%d code elements after the ladder, want 0", got)
	}
	tree = Parse(strings.Repeat("[x]: /u\n", 8000)+"\n[x]", allOn)
	if err := Validate(tree); err != nil {
		t.Fatal(err)
	}
	if got := countNamed(tree, "url"); got != 0 {
		t.Errorf("%d links from definitions past the budget, want 0", got)
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
// the unbounded ones on random text full of delimiters, code spans, links,
// definitions and addresses, which stays far below the budget. It compares each
// of the four engines: Markdown on or off, Autolink on or off.
func TestBoundedParsersChangeNothingBelowTheBudget(t *testing.T) {
	tokens := []string{"*", "**", "_", "__", "|", "||", "~", "a", "b", " ", "\n", "\n\n", "\\", "[", "]", "(", ")", "`", ">", "!",
		"@", ".", "-", "a@b.c", "http://x.example/y", "www.x.example", "*a*", "_b_", "||c||", "**d**", "snake_case", "(*", "*)", "[a](b)",
		"``", "```", "`code`", "``a`b``", "`\n`", "[x]", "[x][y]", "[x][]", "![x]", "[x]: /u\n", "\t[x]: /u\n", "     [x]:\n", "[x]:\n", "[y]: <v> 't'\n", "\n[x]: /w\n", "   [z]: /z\n", "> ", "- ", "    "}
	bounded := engines
	var plain [4]goldmark.Markdown
	for i := range plain {
		plain[i] = newEngineWith(i >= 2, i%2 == 1, false)
	}
	// Lines that begin inside a tab have padding, which a value must keep.
	fixed := []string{">\t[x]\n>\t[x]\n", ">\t[x]\n>\t[x][y]\n>  [x]: /u\n", "- a\n\t[x]\n\t[x]\n", "-\t[x]\n  [x]\n", "> - \t[x]\n>   [x]\n", ">\t[a\n>\tb]\n>\t[x]", ">\t[\n>\tx]\n>\t[x]: /u", "[\nx]\n[\ny]\n[x]", "[a\nb]: /u\n\n[a b]", "[x]: /u\n\"t\" [y]: /v\n[y]"}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 30000+len(fixed); i++ {
		var sb strings.Builder
		for j, k := 0, 1+rng.Intn(30); j < k; j++ {
			sb.WriteString(tokens[rng.Intn(len(tokens))])
		}
		src := sb.String()
		if i < len(fixed) {
			src = fixed[i]
		}
		for flags := range 4 {
			opt := Options{Markdown: flags >= 2, Autolink: flags%2 == 1}
			want := func() string {
				engines = plain
				defer func() { engines = bounded }()
				return js(Parse(src, opt))
			}()
			if got := js(Parse(src, opt)); got != want {
				t.Fatalf("source %q, %+v\nbounded: %s\nplain:   %s", src, opt, got, want)
			}
		}
	}
}

// TestBoundedEnginesWrapTheParsers fails unless each bounded parser ran in the
// engines that hold it. A parse takes a parser context of the test, and each
// wrapper sets the key of its budget in it. An engine of plain goldmark parsers
// sets none, so a source that shows markup is not a proof, and a lookup by value
// that a goldmark upgrade breaks would otherwise remove a bound without an error.
func TestBoundedEnginesWrapTheParsers(t *testing.T) {
	type want struct {
		name     string
		src      string
		key      parser.ContextKey
		markdown bool // the engine needs Markdown
		autolink bool // the engine needs Autolink
	}
	wants := []want{
		{"emphasis", "*a*", delimBudgetKey, true, false},
		{"spoiler", "||a||", delimBudgetKey, true, false},
		{"autolink", "a@b.cc", emailBudgetKey, false, true},
		{"link", "[a](b)", scanBudgetKey, true, false},
		{"code span", "`a`", spanBudgetKey, true, false},
		{"link reference definition", "[x]: /u\n\n[x]", refBudgetKey, true, false},
	}
	ran := func(m goldmark.Markdown, w want) bool {
		pc := parser.NewContext()
		m.Parser().Parse(text.NewReader([]byte(w.src)), parser.WithContext(pc))
		return pc.Get(w.key) != nil
	}
	for flags := range 4 {
		markdown, autolink := flags >= 2, flags%2 == 1
		for _, w := range wants {
			if (w.markdown && !markdown) || (w.autolink && !autolink) {
				continue
			}
			if !ran(engines[flags], w) {
				t.Errorf("markdown=%v autolink=%v: the %s parser is not wrapped in its budget", markdown, autolink, w.name)
			}
			if ran(newEngineWith(markdown, autolink, false), w) {
				t.Errorf("markdown=%v autolink=%v: the unbounded engine sets the budget of the %s parser", markdown, autolink, w.name)
			}
		}
	}
}

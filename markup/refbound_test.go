package markup

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The definition budget (spanscan.go, boundedRefTransformer) charges a paragraph
// for the number of times "]:" occurs in its lines. That is sound only while
// goldmark never takes more link reference definitions from a paragraph than that
// count. The argument rests on facts of goldmark v1.8.6 (a definition needs "]"
// and ":" as adjacent bytes in one line, and no two definitions share them), so
// a goldmark upgrade can break it without any shape test noticing.
//
// These tests state the property itself. They wrap goldmark's own transformer in
// a recorder, parse random container-heavy sources, and compare, for each
// paragraph, the definitions that goldmark took with the "]:" count of the
// segments of that paragraph. A violation means the budget no longer bounds the
// work: recheck the argument in spanscan.go against the new goldmark before the
// upgrade lands.

// refRecorder wraps a paragraph transformer and reports, for each paragraph that
// it sees, the definitions that the transformer took and the "]:" count of the
// segments of the paragraph before the transform.
type refRecorder struct {
	inner  parser.ParagraphTransformer
	report func(taken, bound int, paragraph string)
}

// Transform implements parser.ParagraphTransformer.
func (r refRecorder) Transform(node *ast.Paragraph, reader text.Reader, pc parser.Context) {
	parent := node.Parent()
	before := parent.ChildCount()
	src := reader.Source()
	lines := node.Lines()
	bound := 0
	var shown strings.Builder
	for i := range lines.Len() {
		seg := lines.At(i)
		bound += bytes.Count(src[seg.Start:seg.Stop], []byte("]:"))
		shown.Write(src[seg.Start:seg.Stop])
	}
	r.inner.Transform(node, reader, pc)
	// The transformer inserts one node before the paragraph for each definition.
	// It removes the paragraph when every line was a definition.
	after := parent.ChildCount()
	if node.Parent() == nil {
		after++
	}
	r.report(after-before, bound, shown.String())
}

// recordDefinitions parses src with the engine's block and inline parsers and
// goldmark's unbounded definition transformer inside the recorder.
func recordDefinitions(src string, report func(taken, bound int, paragraph string)) {
	rec := refRecorder{inner: parser.LinkReferenceParagraphTransformer, report: report}
	p := parser.NewParser(
		parser.WithBlockParsers(cappedBlockParsers()...),
		parser.WithInlineParsers(parser.DefaultInlineParsers()...),
		parser.WithParagraphTransformers(util.Prioritized(rec, 100)),
	)
	p.Parse(text.NewReader([]byte(cleanSource(src))))
}

// TestRecorderCountsDefinitions pins the recorder on sources whose answers are
// known, so a recorder that counts nothing cannot make the property test pass.
func TestRecorderCountsDefinitions(t *testing.T) {
	for _, k := range []struct {
		name       string
		src        string
		taken, bnd int
	}{
		{name: "one definition", src: "[a]: /u\n", taken: 1, bnd: 1},
		{name: "two definitions", src: "[a]: /u\n[b]: /v\n", taken: 2, bnd: 2},
		{name: "a definition, then text", src: "[a]: /u\ntext\n", taken: 1, bnd: 1},
		{name: "text with a label end that is no definition", src: "see [a]: here\n", taken: 0, bnd: 1},
		{name: "a definition in a quote", src: "> [a]: /u\n> [b]: /v\n", taken: 2, bnd: 2},
		{name: "a definition in a list item", src: "- [a]: /u\n  [b]: /v\n", taken: 2, bnd: 2},
		{name: "a label on two lines", src: "[a\nb]: /u\n", taken: 1, bnd: 1},
		{name: "a label end split over two lines", src: "[a]\n: /u\n", taken: 0, bnd: 0},
		{name: "a definition after a title", src: "[a]: /u \"t\" [b]: /v\n", taken: 0, bnd: 2},
		{name: "text only", src: "no definition here\n", taken: 0, bnd: 0},
	} {
		var taken, bound, paragraphs int
		recordDefinitions(k.src, func(tk, b int, _ string) { taken, bound, paragraphs = taken+tk, bound+b, paragraphs+1 })
		if paragraphs != 1 || taken != k.taken || bound != k.bnd {
			t.Errorf("%s: %d paragraphs, %d definitions taken, bound %d; want 1, %d, %d", k.name, paragraphs, taken, bound, k.taken, k.bnd)
		}
	}
}

// refSoupTokens are the pieces of the random sources: containers, lazy lines,
// labels, titles, escapes and the shapes that the security rounds found.
var refSoupTokens = []string{
	"[", "]", "]:", ":", "\\]", "\\[", "]\n:", "]\n  :", "[a]:", "[b]: /u", "[c]:\n/u", "[d\ne]:",
	"[a", "a]", "[a] ", "/u", "<u>", "<", ">", "\"t\"", "'t'", "(t)", "\"", "'", "(", ")",
	"\"t\n", "\"\n", "t\"", " \"t\"", " (t)",
	"> ", ">", ">> ", "- ", "* ", "+ ", "1. ", "1) ", "  ", "    ", "\t", " ", "   ",
	"\n", "\n", "\n", "\n\n", "\n> ", "\n- ", "\n  ", "\n\t", "\n[a]: /u\n", "\n>[b]:/v\n",
	"x", "text", "a b", "`", "``", "*", "_", "\\", "#", "&", "!", "|", "||",
}

// refSoup returns one random source of up to n tokens.
func refSoup(rng *rand.Rand, n int) string {
	var sb strings.Builder
	for range 1 + rng.Intn(n) {
		sb.WriteString(refSoupTokens[rng.Intn(len(refSoupTokens))])
	}
	return sb.String()
}

// TestGoldmarkTakesNoMoreDefinitionsThanLabelEnds is the soundness property of the
// definition bound: for every paragraph, the definitions that goldmark takes are
// at most the "]:" count of the segments of the paragraph. The seed is fixed, so a
// failure repeats.
func TestGoldmarkTakesNoMoreDefinitionsThanLabelEnds(t *testing.T) {
	const inputs = 20000
	rng := rand.New(rand.NewSource(20261024))
	var paragraphs, withBound, definitions, tight, violations int
	for range inputs {
		src := refSoup(rng, 60)
		recordDefinitions(src, func(taken, bound int, paragraph string) {
			paragraphs++
			if bound > 0 {
				withBound++
			}
			definitions += taken
			if taken > 0 && taken == bound {
				tight++
			}
			if taken > bound {
				violations++
				if violations <= 5 {
					t.Errorf("goldmark took %d definitions from a paragraph with %d \"]:\"\nparagraph: %q\nsource: %q", taken, bound, paragraph, src)
				}
			}
		})
	}
	if violations > 5 {
		t.Errorf("%d violations in all", violations)
	}
	// The run must reach the property: many paragraphs that hold a label end, and
	// many definitions taken. Otherwise a broken generator would pass.
	t.Logf("%d inputs, %d paragraphs, %d with a label end, %d definitions taken, %d paragraphs at the bound", inputs, paragraphs, withBound, definitions, tight)
	if withBound < inputs/4 || definitions < inputs/4 || tight < inputs/10 {
		t.Errorf("the generator is too weak: %d paragraphs with a label end, %d definitions and %d paragraphs at the bound from %d inputs", withBound, definitions, tight, inputs)
	}
}

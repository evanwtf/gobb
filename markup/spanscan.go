package markup

import (
	"bytes"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The code span parser of goldmark reads from a run of backticks to the next run
// of the same length. Where there is none, it has read the rest of the
// paragraph, one node for each line, and it starts again at the next run. A
// paragraph that opens with runs of 1, 2, ... 200 backticks, and then holds
// 20,000 short lines, makes 200 reads of 20,000 lines each: 60,000 bytes took
// 147 ms and 679 MiB. A run of one length finds a closer unless it is the last
// of its length, so the reads that fail are few, but each is as long as the rest
// of the paragraph.
//
// spanBudget bounds the reads. It is a count of bytes, kept for one parse in the
// parser context. The budget starts at spanBudgetPerByte for each byte of the
// source, plus spanBudgetBase. Before the parser reads, the wrapper looks ahead
// for the closer and spends the bytes that it reads, plus one for each line.
// With no closer, or with too few bytes left in the budget, the wrapper does not
// call the parser: the run of backticks stays text, as it does when the parser
// finds no closer. A natural post spends about its own size, and each span that
// succeeds moves the parse past the bytes that it read.
const (
	spanBudgetPerByte = 16
	spanBudgetBase    = 256 << 10
)

// spanBudgetKey is the context key of the code span read budget of one parse.
var spanBudgetKey = parser.NewContextKey()

// boundedCodeSpanParser wraps the code span parser with the read budget.
type boundedCodeSpanParser struct {
	parser.InlineParser
}

// Parse implements parser.InlineParser.
func (p boundedCodeSpanParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	budget, _ := pc.Get(spanBudgetKey).(*int)
	if budget == nil {
		n := spanBudgetBase + spanBudgetPerByte*len(block.Source())
		budget = &n
		pc.Set(spanBudgetKey, budget)
	}
	line, startSegment := block.PeekLine()
	opener := 0
	for opener < len(line) && line[opener] == '`' {
		opener++
	}
	startLine, startPos := block.Position()
	block.Advance(opener)
	afterLine, afterPos := block.Position()
	found, cost := findSpanCloser(block, opener, *budget)
	if found && cost <= *budget {
		*budget -= cost
		block.SetPosition(startLine, startPos)
		return p.InlineParser.Parse(parent, block, pc)
	}
	if cost > *budget {
		*budget = 0
	} else {
		*budget -= cost
	}
	// The parser, with no closer, leaves the reader after the run and returns
	// the run as text.
	block.SetPosition(afterLine, afterPos)
	return ast.NewTextSegment(startSegment.WithStop(startSegment.Start + opener))
}

// findSpanCloser reads from the reader to the next run of backticks of the given
// length that has no other backtick next to it, as the code span parser does. It
// returns whether it found one, and the bytes it read plus one for each line. It
// stops when that count passes limit. The reader is left at the end of the read.
func findSpanCloser(block text.Reader, opener, limit int) (found bool, cost int) {
	for {
		line, _ := block.PeekLine()
		if line == nil {
			return false, cost
		}
		cost += len(line) + 1
		if cost > limit {
			return false, cost
		}
		for i := 0; i < len(line); i++ {
			if line[i] != '`' {
				continue
			}
			start := i
			for i < len(line) && line[i] == '`' {
				i++
			}
			if i-start == opener {
				return true, cost
			}
		}
		block.AdvanceLine()
	}
}

// boundCodeSpans returns the inline parsers with the code span parser wrapped in
// the read budget.
func boundCodeSpans(list []util.PrioritizedValue) []util.PrioritizedValue {
	for i, v := range list {
		if v.Value == parser.NewCodeSpanParser() {
			list[i].Value = boundedCodeSpanParser{v.Value.(parser.InlineParser)}
		}
	}
	return list
}

// The paragraph transformer of goldmark takes the link reference definitions
// from the start of a paragraph. For each definition it moves the lines that
// remain, so a paragraph of 12,000 lines of "[x]:" costs the square of the line
// count: 60,000 bytes took 28 ms, and twice that took 110 ms.
//
// refBudget bounds the work. It is a count, kept for one parse in the parser
// context. It starts at refBudgetPerByte for each byte of the source, plus
// refBudgetBase. A paragraph spends the number of times "]:" occurs in its lines
// (the most definitions it can hold) times the number of its lines. When the
// paragraph costs more than is left, no definition of that paragraph is taken:
// its lines stay text, and the budget stays as it was for the paragraphs that
// follow. A paragraph of "]:" that are not definitions costs the same as one of
// definitions. A paragraph of 1,000 definitions costs 1,000,000 and fits; a
// natural post has a few.
const (
	refBudgetPerByte = 16
	refBudgetBase    = 1 << 20
)

// labelEnd ends the label of every definition: the "]", then the ":".
var labelEnd = []byte("]:")

// refBudgetKey is the context key of the definition budget of one parse.
var refBudgetKey = parser.NewContextKey()

// boundedRefTransformer wraps the link reference definition transformer with the
// definition budget.
type boundedRefTransformer struct {
	parser.ParagraphTransformer
}

// Transform implements parser.ParagraphTransformer.
func (t boundedRefTransformer) Transform(node *ast.Paragraph, reader text.Reader, pc parser.Context) {
	budget, _ := pc.Get(refBudgetKey).(*int)
	if budget == nil {
		n := refBudgetBase + refBudgetPerByte*len(reader.Source())
		budget = &n
		pc.Set(refBudgetKey, budget)
	}
	lines := node.Lines()
	src := reader.Source()
	// Count the occurrences of "]:" in the lines of the paragraph. Each
	// definition holds one, so the count is an upper bound on the definitions,
	// wherever a definition starts: at the start of a line, behind white space,
	// or after the title of the one before. The argument, from goldmark v1.8.6:
	//   - parser/link_ref.go:79 finds the "]" that closes the label, and
	//     text/reader.go:671 moves the read position past it (the options in
	//     parser/link.go:255-259 keep the move).
	//   - parser/link_ref.go:95 refuses the definition unless the next byte is
	//     ":". No white space can come between.
	//   - That ":" is in the same line as the "]". Each line ends in a newline
	//     (text/reader.go:263-268), except the last line of the source. A "]"
	//     before the newline is not the last byte of its line, so the move past
	//     it stays in that line (text/reader.go:452-466). A "]" at the end of
	//     the source has no next byte.
	//   - The definitions of a paragraph are read in order, and each one reads
	//     past its ":", so no two share a "]:". A label can span lines, but its
	//     "]" and the ":" cannot.
	// An escaped "\]:" counts too, which only makes the bound larger.
	defs := 0
	for i := range lines.Len() {
		seg := lines.At(i)
		defs += bytes.Count(src[seg.Start:seg.Stop], labelEnd)
	}
	// The product cannot overflow: both factors are at most the size of the
	// source, and the budget is far below the square of the size limit.
	cost := defs * lines.Len()
	if cost > *budget {
		// This paragraph keeps its definitions as text. The budget stays as it
		// is: a later paragraph spends from what is left, and the total stays
		// linear, because a refused paragraph costs only the loop above.
		return
	}
	*budget -= cost
	t.ParagraphTransformer.Transform(node, reader, pc)
}

// boundRefTransformers returns the paragraph transformers with the definition
// transformer wrapped in the budget.
func boundRefTransformers(list []util.PrioritizedValue) []util.PrioritizedValue {
	for i, v := range list {
		if v.Value == parser.LinkReferenceParagraphTransformer {
			list[i].Value = boundedRefTransformer{v.Value.(parser.ParagraphTransformer)}
		}
	}
	return list
}

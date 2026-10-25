package markup

import (
	"sort"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The link parser of goldmark reads the destination of "](" up to the first
// white space, or the first unbalanced ")", or the end of the line. When a
// line holds a long run of "[a](" and no white space, every "](" reads to the
// end of the line and fails, so the work grows with the square of the line.
// 10,000 repeats took half a second.
//
// scanBudget bounds that work. It is a count of bytes, kept for one parse in the
// parser context. The budget starts at scanBudgetPerByte for each byte of the
// source, plus scanBudgetBase. A "](" spends the length of the destination it
// would read. When the budget is gone, a "](" does not read its destination, so
// it is not a link and the text stays text. A natural post never gets there: its
// destinations end at the first space or ")" and add up to about the size of the
// post. The result depends on the input only, never on the clock.
const (
	scanBudgetPerByte = 32
	scanBudgetBase    = 64 << 10
)

// scanBudgetKey is the context key of the budget of one parse.
var scanBudgetKey = parser.NewContextKey()

// boundedLinkParser wraps the link parser of goldmark with the scan budget.
type boundedLinkParser struct {
	parser.InlineParser
}

// Parse implements parser.InlineParser.
func (p boundedLinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	if line, _ := block.PeekLine(); len(line) >= 2 && line[0] == ']' && line[1] == '(' {
		budget, _ := pc.Get(scanBudgetKey).(*int)
		if budget == nil {
			n := scanBudgetBase + scanBudgetPerByte*len(block.Source())
			budget = &n
			pc.Set(scanBudgetKey, budget)
		}
		// Scan at most one byte more than the budget holds: that is enough to
		// see that the destination does not fit.
		n := destinationLen(line[2:], *budget+1)
		if n > *budget {
			*budget = 0
			block = &noDestination{Reader: block}
		} else {
			*budget -= n
		}
	}
	if parent.Type() == ast.TypeBlock && parent.Lines().Len() > 1 {
		block = &valueReader{Reader: block, lines: parent.Lines()}
	}
	return p.InlineParser.Parse(parent, block, pc)
}

// valueReader is a reader whose Value finds its first line by binary search.
//
// The Value of the reader of goldmark finds the line that holds a segment by a
// walk back from the last line of the paragraph. The link parser calls Value for
// each "]" that can start a reference ("[x]", "[x][y]"), so a paragraph of 12,000
// lines of "[x]" costs the square of the line count: 60,000 bytes took 42 ms, and
// twice that took 148 ms. This Value returns what that Value returns: the lines
// of a paragraph are in order of their start, so the last line that starts at or
// before the segment is the line where the walk back ends.
type valueReader struct {
	text.Reader
	lines *text.Segments
}

// Value implements text.Reader.
func (r *valueReader) Value(seg text.Segment) []byte {
	n := r.lines.Len()
	line := sort.Search(n, func(i int) bool { return r.lines.At(i).Start > seg.Start }) - 1
	if line < 0 {
		return r.Reader.Value(seg)
	}
	src := r.Reader.Source()
	ret := make([]byte, 0, seg.Stop-seg.Start+1)
	i := seg.Start
	for ; line < n; line++ {
		s := r.lines.At(line)
		if i < 0 {
			i = s.Start
		}
		ret = s.ConcatPadding(ret)
		for ; i < seg.Stop && i < s.Stop; i++ {
			ret = append(ret, src[i])
		}
		i = -1
		if s.Stop > seg.Stop {
			break
		}
	}
	return ret
}

// CloseBlock implements parser.CloseBlocker. goldmark calls it when a paragraph
// ends, and the link parser turns each "[" that found no "]" back into text
// there. A wrapper that leaves it out loses those brackets, so it is forwarded.
func (p boundedLinkParser) CloseBlock(parent ast.Node, block text.Reader, pc parser.Context) {
	p.InlineParser.(parser.CloseBlocker).CloseBlock(parent, block, pc)
}

// noDestination is a reader whose second PeekLine returns an empty line. The
// link parser reads its first line to find the "]" and its second to find the
// destination, so an empty second line makes the destination fail at once.
type noDestination struct {
	text.Reader
	peeks int
}

// PeekLine implements text.Reader.
func (r *noDestination) PeekLine() ([]byte, text.Segment) {
	line, seg := r.Reader.PeekLine()
	if r.peeks++; r.peeks > 1 {
		return nil, seg
	}
	return line, seg
}

// destinationLen returns how many bytes of line the link parser reads for a
// destination, after the white space that it skips, and stops counting at limit.
// It follows the scan of goldmark: a "<...>" destination ends at the first
// unescaped ">", and a bare destination ends at white space or at the ")" that
// closes more parentheses than it opens. A backslash before punctuation escapes
// that character.
func destinationLen(line []byte, limit int) int {
	i := 0
	for i < len(line) && util.IsSpace(line[i]) {
		i++
	}
	start := i
	if i == len(line) {
		return 0
	}
	if line[i] == '<' {
		for i++; i < len(line) && i-start < limit; i++ {
			switch c := line[i]; {
			case c == '\\' && i < len(line)-1 && util.IsPunct(line[i+1]):
				i++
			case c == '>':
				return i - start + 1
			}
		}
		return min(i-start, limit)
	}
	opened := 0
	for i < len(line) && i-start < limit {
		c := line[i]
		switch {
		case c == '\\' && i < len(line)-1 && util.IsPunct(line[i+1]):
			i++
		case c == '(':
			opened++
		case c == ')':
			if opened--; opened < 0 {
				return i - start
			}
		case util.IsSpace(c):
			return i - start
		}
		i++
	}
	return min(i-start, limit)
}

// boundedInlineParsers is goldmark's list of inline parsers, with the link
// parser wrapped in the scan budget, the emphasis parser wrapped in the
// delimiter budget (delimscan.go), and the code span parser wrapped in the read
// budget (spanscan.go).
func boundedInlineParsers() []util.PrioritizedValue {
	list := parser.DefaultInlineParsers()
	for i, v := range list {
		if v.Value == parser.NewLinkParser() {
			list[i].Value = boundedLinkParser{v.Value.(parser.InlineParser)}
		}
	}
	return boundCodeSpans(boundDelimiters(list))
}

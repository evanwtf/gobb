package markup

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The emphasis parser and the spoiler parser of this package record each run of
// "*", "_" or "|" as a delimiter in one list. At the end of a paragraph,
// goldmark takes each closer in turn and walks back over the whole list to find
// an opener. Where no opener matches, the closer removes itself, but the openers
// stay. A paragraph that holds "*_a" many times has 20,000 closers of "*" that
// each walk back over every opener of "_", and nothing ever pairs. The work grows
// with the square of the paragraph: 60,000 bytes took 1.6 s.
//
// delimBudget bounds that work. It is a count of steps, kept for one parse in the
// parser context. The budget starts at delimBudgetPerByte for each byte of the
// source, plus delimBudgetBase. A new delimiter spends one step, and a delimiter
// that can close also spends the length of the list before it, which is the
// longest walk that it can cause, because the list only shrinks. When the budget
// is gone, the delimiter is not recorded and the rest of the delimiters stay
// text. The charge is the longest walk that the closer can cause, not the walk
// that it will make: a closer that pairs with the opener just before it walks
// one step, but is charged for the whole list. So a paragraph of n pairs of
// emphasis spends about n squared steps, and the budget holds a paragraph of
// 1,000 pairs; a natural post has tens. The delimiters of a paragraph leave the
// list when the paragraph ends. The result depends on the input only, never on
// the clock.
const (
	delimBudgetPerByte = 8
	delimBudgetBase    = 1 << 20
)

// delimBudgetKey is the context key of the delimiter budget of one parse.
var delimBudgetKey = parser.NewContextKey()

// boundedDelimiterParser wraps an inline parser that records delimiters with the
// delimiter budget.
type boundedDelimiterParser struct {
	parser.InlineParser
}

// Parse implements parser.InlineParser. It hands the inner parser a context that
// asks the budget before it records a delimiter.
func (p boundedDelimiterParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	budget, _ := pc.Get(delimBudgetKey).(*int)
	if budget == nil {
		n := delimBudgetBase + delimBudgetPerByte*len(block.Source())
		budget = &n
		pc.Set(delimBudgetKey, budget)
	}
	// With no budget left, the inner parser still reads the run, and the gate
	// refuses it. Returning nil instead would hand the run to the next parser,
	// and the autolink parser scans to the end of the line from every "*" and
	// "_", which is the same square again.
	gate := &delimGate{Context: pc, budget: budget}
	node := p.InlineParser.Parse(parent, block, gate)
	if gate.refused != nil {
		// The inner parser built a delimiter that the list does not hold. The
		// text of the run stays text.
		return ast.NewTextSegment(gate.refused.Segment)
	}
	return node
}

// delimGate is the parser context that the inner parser sees. It spends the
// budget when the parser records a delimiter.
type delimGate struct {
	parser.Context
	budget  *int
	refused *parser.Delimiter
}

// PushDelimiter implements parser.Context.
func (g *delimGate) PushDelimiter(d *parser.Delimiter) {
	cost := 1
	if d.CanClose {
		// Count at most one more than the budget holds: that is enough to see
		// that the walk does not fit.
		for o := g.LastDelimiter(); o != nil && cost <= *g.budget; o = o.PreviousDelimiter {
			cost++
		}
	}
	if cost > *g.budget {
		*g.budget = 0
		g.refused = d
		return
	}
	*g.budget -= cost
	g.Context.PushDelimiter(d)
}

// boundDelimiters returns the inline parsers with the emphasis parser wrapped in
// the delimiter budget.
func boundDelimiters(list []util.PrioritizedValue) []util.PrioritizedValue {
	for i, v := range list {
		if v.Value == parser.NewEmphasisParser() {
			list[i].Value = boundedDelimiterParser{v.Value.(parser.InlineParser)}
		}
	}
	return list
}

// The autolink parser of goldmark runs at every space, "*", "_", "~" and "(".
// For an address, it reads the run of characters that an address may hold, up to
// a "@", and then reads the domain after it. Where the run is long, as in a
// line of "~a~a~a", every "~" reads the rest of the run, and the work grows with
// the square of the line: 60,000 bytes took 0.2 s.
//
// emailBudget bounds that work, as delimBudget does. It is a count of bytes, kept
// for one parse in the parser context. A trigger spends one byte, plus the
// length of the run it would read, and of the domain after a "@". When the budget
// is gone, the autolink parser finds no address, so the text stays text. The
// scan of a natural post adds up to about the size of the post.
const (
	emailBudgetPerByte = 64
	emailBudgetBase    = 256 << 10
)

// emailBudgetKey is the context key of the address scan budget of one parse.
var emailBudgetKey = parser.NewContextKey()

// emailChars marks the bytes that the address parser of goldmark accepts before
// the "@". The table is built from the parser itself, so a change in goldmark
// cannot make the two differ.
var emailChars = func() (t [256]bool) {
	for c := range 256 {
		t[c] = util.FindEmailIndex([]byte{byte(c), '@', 'a', '.', 'a'}) >= 0
	}
	return t
}()

// boundedLinkifyParser wraps the autolink parser of goldmark with the address
// scan budget.
type boundedLinkifyParser struct {
	parser.InlineParser
}

// Parse implements parser.InlineParser.
func (p boundedLinkifyParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	budget, _ := pc.Get(emailBudgetKey).(*int)
	if budget == nil {
		n := emailBudgetBase + emailBudgetPerByte*len(block.Source())
		budget = &n
		pc.Set(emailBudgetKey, budget)
	}
	line, _ := block.PeekLine()
	if len(line) > 0 {
		switch line[0] {
		case ' ', '*', '_', '~', '(':
			line = line[1:] // the parser skips the trigger
		}
	}
	// Read at most one byte more than the budget holds: that is enough to see
	// that the scan does not fit.
	cost := emailScanLen(line, *budget+1)
	if cost > *budget {
		*budget = 0
		return nil
	}
	*budget -= cost
	return p.InlineParser.Parse(parent, block, pc)
}

// emailScanLen returns the bytes that the address parser reads from line: one,
// plus the run of address characters, and the domain after a "@". It stops
// counting at limit.
func emailScanLen(line []byte, limit int) int {
	n := 0
	for n < len(line) && n < limit && emailChars[line[n]] {
		n++
	}
	cost := n + 1
	if n < len(line) && line[n] == '@' {
		for i := n + 1; i < len(line) && cost < limit; i++ {
			c := line[i]
			if c != '-' && c != '.' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
				break
			}
			cost++
		}
	}
	return cost
}

// linkifyExtension adds the autolink parser of goldmark, wrapped in the address
// scan budget.
type linkifyExtension struct{}

// Extend implements goldmark.Extender.
func (linkifyExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(boundedLinkifyParser{extension.NewLinkifyParser()}, 999),
	))
}

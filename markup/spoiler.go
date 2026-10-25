package markup

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// spoilerNode is the goldmark node for ||text||. Its children are inlines.
type spoilerNode struct {
	ast.BaseInline
}

var kindSpoiler = ast.NewNodeKind("Spoiler")

// Kind implements ast.Node.
func (n *spoilerNode) Kind() ast.NodeKind { return kindSpoiler }

// Dump implements ast.Node.
func (n *spoilerNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// spoilerDelimiters pairs runs of "|" the way emphasis pairs runs of "*".
type spoilerDelimiters struct{}

func (spoilerDelimiters) IsDelimiter(b byte) bool { return b == '|' }

// CanOpenCloser needs two or more characters left on both runs, so a lone "|"
// never becomes part of a spoiler.
func (spoilerDelimiters) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char && opener.Length >= 2 && closer.Length >= 2
}

func (spoilerDelimiters) OnMatch(int) ast.Node { return &spoilerNode{} }

// spoilerParser is the inline parser that finds runs of two or more "|".
type spoilerParser struct{}

// Trigger implements parser.InlineParser.
func (spoilerParser) Trigger() []byte { return []byte{'|'} }

// Parse implements parser.InlineParser. It scans a run of "|", records it as a
// delimiter, and leaves the pairing to the delimiter processor. A run that
// nothing closes turns back into text. A backslash escape never reaches this
// parser, because the block parser skips an escaped character.
func (spoilerParser) Parse(_ ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := parser.ScanDelimiter(line, before, 2, spoilerDelimiters{})
	if node == nil {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

// spoilerExtension adds ||text|| to a goldmark instance.
type spoilerExtension struct {
	bounded bool // wrap the parser in the delimiter budget
}

// Extend implements goldmark.Extender.
func (e spoilerExtension) Extend(m goldmark.Markdown) {
	var p parser.InlineParser = spoilerParser{}
	if e.bounded {
		p = boundedDelimiterParser{p}
	}
	m.Parser().AddOptions(parser.WithInlineParsers(util.Prioritized(p, 500)))
}

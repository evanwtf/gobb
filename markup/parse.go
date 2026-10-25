package markup

import (
	"bufio"
	"bytes"
	"html"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Smiley is one smiley that the admin defines. The parser matches Code in the
// text and writes a smiley element that holds only the code. The parser ignores
// Image and Alt. The renderer finds the Smiley again by that code, through
// [RenderContext].Smiley, and uses Image and Alt.
type Smiley struct {
	Code  string // the text that stands for the smiley, for example ":)"
	Image string // the URL of the image: a site path or an http or https URL
	Alt   string // the alt text of the image; the code stands in when empty
}

// Options are the three flags of a post, signature or private message, and the
// smiley list that the Smilies flag uses.
type Options struct {
	// Markdown reads the source as CommonMark plus ||spoilers||. When it is off,
	// the source is plain text: a blank line splits paragraphs and each line
	// break is a br.
	Markdown bool
	// Smilies turns the codes in SmileyList into smiley elements.
	Smilies bool
	// Autolink turns bare http://, https:// and www. addresses and bare email
	// addresses into links, and a link alone on its line into an embed.
	Autolink bool
	// SmileyList holds the smilies. The parser ignores it unless Smilies is on.
	SmileyList []Smiley
}

const (
	maxPlainLinkRunes = 55   // a bare link with more characters gets a display text
	displayHead       = 39   // characters kept at the start of a display text
	displayTail       = 10   // characters kept at the end of a display text
	maxAltRunes       = 1000 // the most characters in an img alt
	maxSmileyRunes    = 32   // the most characters in a smiley code
)

// embedTags are the embed tags in the order of the embed table in the design
// spec. A test requires that the list names every tag that embedRules holds.
var embedTags = []string{
	"youtube", "vimeo", "tweet", "instagram", "reddit",
	"tiktok", "threads", "spotify", "soundcloud",
}

// engines holds one goldmark instance for each mix of the Markdown and Autolink
// flags. A goldmark instance is safe for concurrent use. The index is
// markdown*2 + autolink.
var engines = [4]goldmark.Markdown{
	newEngine(false, false), newEngine(false, true),
	newEngine(true, false), newEngine(true, true),
}

// newEngine builds the engine for one mix of flags, with the bounded link
// parser.
func newEngine(markdown, autolink bool) goldmark.Markdown {
	return newEngineWith(markdown, autolink, true)
}

// newEngineWith builds the goldmark parser for one mix of flags. With Markdown on
// it is CommonMark plus spoilers. With Markdown off it has the paragraph block
// parser and no inline parser, so the text stays as written. Linkify finds the
// bare links in both.
func newEngineWith(markdown, autolink, bounded bool) goldmark.Markdown {
	inline := parser.DefaultInlineParsers()
	if bounded {
		inline = boundedInlineParsers()
	}
	var opts []goldmark.Option
	if markdown {
		opts = append(opts, goldmark.WithParser(parser.NewParser(
			parser.WithBlockParsers(cappedBlockParsers()...),
			parser.WithInlineParsers(inline...),
			parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
		)))
	} else {
		opts = append(opts, goldmark.WithParser(parser.NewParser(
			parser.WithBlockParsers(util.Prioritized(parser.NewParagraphParser(), 1000)),
		)))
	}
	var exts []goldmark.Extender
	if markdown {
		exts = append(exts, spoilerExtension{bounded: bounded})
	}
	if autolink {
		if bounded {
			exts = append(exts, linkifyExtension{})
		} else {
			exts = append(exts, extension.Linkify)
		}
	}
	opts = append(opts, goldmark.WithExtensions(exts...))
	return goldmark.New(opts...)
}

// maxContainers is the deepest nesting of quotes and lists that the parser
// opens. The tree cannot hold more: it is at most [MaxDepth] elements deep, and
// every container is one of them. goldmark takes time in proportion to the
// square of the nesting on one line, so a line of 40,000 ">" costs seconds. Past
// the cap, a marker stays in the text of the innermost container.
const maxContainers = MaxDepth

// cappedParser opens a quote or a list only while the nesting allows it. It
// wraps the quote parser and the list parser. It must not wrap the list item
// parser: goldmark expects an item in every list, and a list without its item
// holds a paragraph and panics on the next indented line. Instead, a list opens
// only when the list and its item both fit.
type cappedParser struct {
	parser.BlockParser
	extra int // containers that Open adds: 1 for a quote, 2 for a list and its item
}

// Open implements parser.BlockParser.
func (c cappedParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	n := c.extra - 1
	for p := parent; p != nil; p = p.Parent() {
		if n++; n > maxContainers {
			return nil, parser.NoChildren
		}
	}
	return c.BlockParser.Open(parent, reader, pc)
}

// cappedBlockParsers is goldmark's list of block parsers, with the quote parser
// and the list parser capped. The ancestors that the walk counts are quotes,
// lists and list items, plus the document.
func cappedBlockParsers() []util.PrioritizedValue {
	list := parser.DefaultBlockParsers()
	for i, v := range list {
		switch v.Value {
		case parser.NewBlockquoteParser():
			list[i].Value = cappedParser{v.Value.(parser.BlockParser), 1}
		case parser.NewListParser():
			list[i].Value = cappedParser{v.Value.(parser.BlockParser), 2}
		}
	}
	return list
}

// Parse turns source text into a document tree. It never fails, and its result
// always passes [Validate].
//
// Parse replaces invalid UTF-8 and NUL with U+FFFD, and turns CR LF and CR into
// LF. It parses with goldmark and maps the goldmark tree to a [Node] tree in one
// pass. Content that would nest deeper than [MaxDepth] elements, or inside
// more than [MaxQuoteDepth] quotes, becomes plain text.
//
// Parse is safe for concurrent use.
func Parse(src string, opt Options) *Node {
	src = cleanSource(src)
	if !opt.Markdown {
		src = trimIndent(src)
	}
	data := []byte(src)
	root := &Node{Kind: Root}
	idx := 0
	if opt.Markdown {
		idx += 2
	}
	if opt.Autolink {
		idx++
	}
	doc := engines[idx].Parser().Parse(text.NewReader(data))
	b := &builder{src: data, opt: opt, md: opt.Markdown}
	b.w = bufio.NewWriter(&b.buf)
	if opt.Smilies {
		b.smileyIndex()
	}
	b.blocks(&root.Children, doc, 0, 0)
	return root
}

// cleanSource makes the source safe to parse. A run of invalid UTF-8 bytes
// becomes one U+FFFD. A NUL becomes U+FFFD too, as CommonMark says, because a
// stored JSON document may not hold it. Every line ending becomes LF.
func cleanSource(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	if strings.IndexByte(s, '\r') >= 0 {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return s
}

// trimIndent removes the white space at the start of every line. With Markdown
// off, the paragraph parser would otherwise read a line with four spaces of
// indent as a code block, which it does not have, and drop it.
func trimIndent(s string) string {
	if !strings.ContainsAny(s, " \t") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimLeft(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// builder maps one goldmark tree to a Node tree.
type builder struct {
	src []byte
	opt Options
	md  bool // Markdown is on: text holds escapes and entities to resolve

	smileys map[byte][]Smiley // smilies by first byte of code, longest code first

	joined map[*Node]*strings.Builder // the text of the nodes that addText has grown

	buf bytes.Buffer // scratch for decode
	w   *bufio.Writer
}

// inCtx is the state of the inline mapping. depth is the depth of the element
// that holds the nodes. The mapping makes an element only when depth is less
// than [MaxDepth].
type inCtx struct {
	depth     int
	inLink    bool
	inSpoiler bool
}

func newEl(name string, attrs map[string]string, children []*Node) *Node {
	return &Node{Kind: Element, Name: name, Attrs: attrs, Children: children}
}

func newText(s string) *Node { return &Node{Kind: Text, Text: s} }

// addText appends text to out. It joins the text to a text node at the end.
// The join is amortized: a node that grows keeps a strings.Builder in b.joined,
// so a run of n small pieces costs time in proportion to n, where a string
// concatenation per piece costs time in proportion to n squared.
func (b *builder) addText(out *[]*Node, s string) {
	if s == "" {
		return
	}
	if n := len(*out); n > 0 && (*out)[n-1].Kind == Text {
		last := (*out)[n-1]
		sb := b.joined[last]
		if sb == nil {
			sb = new(strings.Builder)
			sb.WriteString(last.Text)
			if b.joined == nil {
				b.joined = map[*Node]*strings.Builder{}
			}
			b.joined[last] = sb
		}
		sb.WriteString(s)
		// String returns the bytes written so far without a copy. Later writes
		// never change them, so an earlier value of last.Text stays valid.
		last.Text = sb.String()
		return
	}
	*out = append(*out, newText(s))
}

// addBreak appends a br, or a space when a br would be too deep. depth is the
// depth of the element that holds out.
func (b *builder) addBreak(out *[]*Node, depth int) {
	if depth >= MaxDepth {
		b.addText(out, " ")
		return
	}
	*out = append(*out, newEl("br", nil, nil))
}

// addMultiline appends s with a br at each line feed.
func (b *builder) addMultiline(out *[]*Node, s string, depth int) {
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.addBreak(out, depth)
		}
		b.addText(out, line)
	}
}

// decode resolves backslash escapes and character references the way CommonMark
// does. It uses goldmark's own writer, which escapes the four HTML characters
// on output, and then undoes that escaping. The two steps are exact inverses.
func (b *builder) decode(v []byte) string {
	if bytes.IndexByte(v, '\\') < 0 && bytes.IndexByte(v, '&') < 0 && bytes.IndexByte(v, 0) < 0 {
		return string(v)
	}
	b.buf.Reset()
	b.w.Reset(&b.buf)
	ghtml.DefaultWriter.Write(b.w, v)
	_ = b.w.Flush()
	return html.UnescapeString(b.buf.String())
}

// blocks maps the children of parent, an element at the given depth, into out.
// quotes is the number of quote elements on the path. A child that could not
// keep its structure within the caps becomes a p of plain text.
func (b *builder) blocks(out *[]*Node, parent ast.Node, depth, quotes int) {
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			b.paragraph(out, c, depth)
		case *ast.Heading:
			var kids []*Node
			b.inlineRange(&kids, v.FirstChild(), nil, inCtx{depth: depth + 1})
			kids = b.smilify(kids, depth+1)
			if len(kids) > 0 {
				*out = append(*out, newEl("h", map[string]string{"level": strconv.Itoa(v.Level)}, kids))
			}
		case *ast.ThematicBreak:
			*out = append(*out, newEl("hr", nil, nil))
		case *ast.Blockquote:
			// The quote is at depth+1 and needs room for a p inside it.
			if quotes+1 > MaxQuoteDepth || depth+2 > MaxDepth {
				b.plainBlock(out, c, depth)
				continue
			}
			var kids []*Node
			b.blocks(&kids, c, depth+1, quotes+1)
			*out = append(*out, newEl("quote", nil, kids))
		case *ast.List:
			// The list is at depth+1, its li at depth+2, and a p in the li at depth+3.
			if depth+3 > MaxDepth {
				b.plainBlock(out, c, depth)
				continue
			}
			b.list(out, v, depth, quotes)
		case *ast.FencedCodeBlock:
			attrs := map[string]string(nil)
			if v.Info != nil {
				info := string(v.Info.Segment.Value(b.src))
				if f := strings.Fields(info); len(f) > 0 {
					if lang := b.decode([]byte(f[0])); isLang(lang) {
						attrs = map[string]string{"lang": lang}
					}
				}
			}
			b.pre(out, attrs, v.Lines())
		case *ast.CodeBlock:
			b.pre(out, nil, v.Lines())
		case *ast.HTMLBlock:
			var sb strings.Builder
			sb.Write(v.Lines().Value(b.src))
			if v.HasClosure() {
				sb.Write(v.ClosureLine.Value(b.src))
			}
			var kids []*Node
			b.addMultiline(&kids, strings.TrimRight(sb.String(), "\n"), depth+1)
			if len(kids) > 0 {
				*out = append(*out, newEl("p", nil, kids))
			}
		default:
			b.plainBlock(out, c, depth)
		}
	}
}

// list maps a list whose li and p fit within the depth cap.
func (b *builder) list(out *[]*Node, l *ast.List, depth, quotes int) {
	var attrs map[string]string
	set := func(k, v string) {
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs[k] = v
	}
	if l.IsOrdered() {
		set("ordered", "true")
		if s := strconv.Itoa(l.Start); l.Start != 1 && isListStart(s) {
			set("start", s)
		}
	}
	if l.IsTight {
		set("tight", "true")
	}
	var items []*Node
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		var kids []*Node
		b.blocks(&kids, item, depth+2, quotes)
		items = append(items, newEl("li", nil, kids))
	}
	*out = append(*out, newEl("list", attrs, items))
}

// pre appends a pre element for a code block. It drops the final line feed.
func (b *builder) pre(out *[]*Node, attrs map[string]string, lines *text.Segments) {
	s := strings.TrimSuffix(string(lines.Value(b.src)), "\n")
	var kids []*Node
	if s != "" {
		kids = []*Node{newText(s)}
	}
	*out = append(*out, newEl("pre", attrs, kids))
}

// plainBlock appends a p that holds the text of n without its markup. The p is
// at depth+1.
func (b *builder) plainBlock(out *[]*Node, n ast.Node, depth int) {
	var kids []*Node
	b.addMultiline(&kids, b.plainString(n), depth+1)
	if len(kids) > 0 {
		*out = append(*out, newEl("p", nil, kids))
	}
}

// paragraph maps a paragraph or a tight list item's text block. With Autolink
// on, a line that holds only one bare link that EmbedID accepts becomes an
// embed, and the paragraph splits around it.
func (b *builder) paragraph(out *[]*Node, n ast.Node, depth int) {
	first := n.FirstChild()
	if !b.opt.Autolink {
		b.emitP(out, first, nil, depth)
		return
	}
	groupStart, lineStart := first, first
	for c := first; c != nil; c = c.NextSibling() {
		t, isText := c.(*ast.Text)
		if c.NextSibling() != nil && !(isText && (t.SoftLineBreak() || t.HardLineBreak())) {
			continue
		}
		// c ends the line that starts at lineStart.
		if emb := b.embedOf(lineStart, c); emb != nil {
			if groupStart != lineStart {
				b.emitP(out, groupStart, lineStart, depth)
			}
			*out = append(*out, emb)
			groupStart = c.NextSibling()
		}
		lineStart = c.NextSibling()
	}
	if groupStart != nil {
		b.emitP(out, groupStart, nil, depth)
	}
}

// emitP appends a p for the inline nodes from first up to, but not including,
// stop.
func (b *builder) emitP(out *[]*Node, from, stop ast.Node, depth int) {
	var kids []*Node
	b.inlineRange(&kids, from, stop, inCtx{depth: depth + 1})
	for len(kids) > 0 && kids[len(kids)-1].Name == "br" {
		kids = kids[:len(kids)-1]
	}
	for len(kids) > 0 && kids[0].Name == "br" {
		kids = kids[1:]
	}
	kids = b.smilify(kids, depth+1)
	if len(kids) > 0 {
		*out = append(*out, newEl("p", nil, kids))
	}
}

// embedOf returns an embed element when the line from first to last holds one
// bare link and nothing else but white space, and EmbedID accepts the link.
func (b *builder) embedOf(first, last ast.Node) *Node {
	var link *ast.AutoLink
	for c := first; c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			if strings.TrimSpace(string(v.Value(b.src))) != "" {
				return nil
			}
		case *ast.AutoLink:
			if link != nil || v.AutoLinkType != ast.AutoLinkURL {
				return nil
			}
			link = v
		default:
			return nil
		}
		if c == last {
			break
		}
	}
	if link == nil {
		return nil
	}
	raw := string(link.URL(b.src))
	if href, ok := destination([]byte(raw), false); !ok || !CheckLinkURL(href) {
		return nil
	}
	for _, tag := range embedTags {
		if id, ok := EmbedID(tag, raw); ok {
			return newEl(tag, map[string]string{"id": id}, nil)
		}
	}
	return nil
}

// inlineRange maps the siblings from first up to, but not including, stop.
func (b *builder) inlineRange(out *[]*Node, from, stop ast.Node, ctx inCtx) {
	for c := from; c != nil && c != stop; c = c.NextSibling() {
		b.inline(out, c, ctx)
	}
}

// wrap maps the children of n into a new element, if the depth cap allows one.
// Otherwise it maps them into the current element. An element without children
// is dropped.
func (b *builder) wrap(out *[]*Node, name string, attrs map[string]string, n ast.Node, ctx, child inCtx) {
	if ctx.depth >= MaxDepth {
		b.inlineRange(out, n.FirstChild(), nil, ctx)
		return
	}
	child.depth = ctx.depth + 1
	var kids []*Node
	b.inlineRange(&kids, n.FirstChild(), nil, child)
	if len(kids) > 0 {
		*out = append(*out, newEl(name, attrs, kids))
	}
}

func (b *builder) inline(out *[]*Node, n ast.Node, ctx inCtx) {
	switch v := n.(type) {
	case *ast.Text:
		var s string
		if b.md && !v.IsRaw() {
			s = b.decode(v.Value(b.src))
		} else {
			s = string(v.Value(b.src))
		}
		b.addText(out, s)
		if !b.md && v.HardLineBreak() {
			// Goldmark drops the backslash of a hard break. Plain text keeps it.
			if stop := v.Segment.Stop; stop < len(b.src) && b.src[stop] == '\\' {
				b.addText(out, "\\")
			}
		}
		if v.SoftLineBreak() || v.HardLineBreak() {
			b.addBreak(out, ctx.depth)
		}
	case *ast.String:
		b.addText(out, string(v.Value))
	case *ast.CodeSpan:
		var sb strings.Builder
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				sb.Write(t.Value(b.src))
			}
		}
		s := strings.ReplaceAll(sb.String(), "\n", " ")
		switch {
		case s == "":
		case ctx.depth >= MaxDepth:
			b.addText(out, s)
		default:
			*out = append(*out, newEl("code", nil, []*Node{newText(s)}))
		}
	case *ast.Emphasis:
		name := "i"
		if v.Level >= 2 {
			name = "b"
		}
		b.wrap(out, name, nil, n, ctx, ctx)
	case *spoilerNode:
		if ctx.inSpoiler {
			b.inlineRange(out, n.FirstChild(), nil, ctx)
			return
		}
		child := ctx
		child.inSpoiler = true
		b.wrap(out, "spoiler", nil, n, ctx, child)
	case *ast.Link:
		dest, ok := destination(v.Destination, true)
		if !ok || ctx.inLink || ctx.depth >= MaxDepth || !CheckLinkURL(dest) {
			b.inlineRange(out, n.FirstChild(), nil, ctx)
			return
		}
		child := ctx
		child.depth++
		child.inLink = true
		var kids []*Node
		b.inlineRange(&kids, n.FirstChild(), nil, child)
		if len(kids) == 0 {
			kids = []*Node{newText(dest)}
		}
		*out = append(*out, newEl("url", map[string]string{"href": dest}, kids))
	case *ast.Image:
		alt := cleanAlt(b.plainString(n))
		src, ok := destination(v.Destination, true)
		if !ok || ctx.depth >= MaxDepth || !CheckImageURL(src) {
			b.addText(out, alt)
			return
		}
		attrs := map[string]string{"src": src}
		if alt != "" {
			attrs["alt"] = alt
		}
		*out = append(*out, newEl("img", attrs, nil))
	case *ast.AutoLink:
		b.autoLink(out, v, ctx)
	case *ast.RawHTML:
		var sb strings.Builder
		for i := 0; i < v.Segments.Len(); i++ {
			seg := v.Segments.At(i)
			sb.Write(seg.Value(b.src))
		}
		b.addMultiline(out, sb.String(), ctx.depth)
	default:
		b.addMultiline(out, b.plainString(n), ctx.depth)
	}
}

// autoLink maps <url>, <email> and a bare link.
func (b *builder) autoLink(out *[]*Node, v *ast.AutoLink, ctx inCtx) {
	label := string(v.Label(b.src))
	if ctx.inLink || ctx.depth >= MaxDepth {
		b.addText(out, label)
		return
	}
	if v.AutoLinkType == ast.AutoLinkEmail {
		if !isEmail(label) {
			b.addText(out, label)
			return
		}
		*out = append(*out, newEl("email", map[string]string{"address": label}, []*Node{newText(label)}))
		return
	}
	href, ok := destination(v.URL(b.src), false)
	if !ok || !CheckLinkURL(href) {
		b.addText(out, label)
		return
	}
	attrs := map[string]string{"href": href}
	if d, ok := displayText(label); ok {
		attrs["display"] = d
	}
	*out = append(*out, newEl("url", attrs, []*Node{newText(label)}))
}

// destination returns the address of a link or image as CommonMark writes it
// into an href: escapes and references resolved when resolve is set, and
// percent-encoded. It reports false when the resolved address holds a control
// or format character. Percent-encoding would hide such a character from
// [CheckLinkURL], and the address is not one a member typed on purpose.
func destination(raw []byte, resolve bool) (string, bool) {
	if resolve {
		raw = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(raw)))
	}
	for _, r := range string(raw) {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", false
		}
	}
	return string(util.URLEscape(raw, false)), true
}

// displayText shortens link text of more than 55 characters: the first 39, then
// " ... ", then the last 10.
func displayText(label string) (string, bool) {
	if utf8.RuneCountInString(label) <= maxPlainLinkRunes {
		return "", false
	}
	r := []rune(label)
	return cleanAlt(string(r[:displayHead]) + " ... " + string(r[len(r)-displayTail:])), true
}

// cleanAlt makes text fit an attribute. A tab, a line break and every other
// control character, C0 or C1, becomes a space. The result keeps at most
// maxAltRunes characters.
func cleanAlt(s string) string {
	var sb strings.Builder
	n := 0
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			r = ' '
		}
		if n++; n > maxAltRunes {
			break
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// plainString returns the text of n and of all it holds, without markup. A
// line feed marks a line break or the end of a block. The walk is iterative, so
// a deep tree cannot exhaust the stack.
func (b *builder) plainString(root ast.Node) string {
	var sb strings.Builder
	atLineStart := true
	write := func(s string) {
		if s == "" {
			return
		}
		sb.WriteString(s)
		atLineStart = s[len(s)-1] == '\n'
	}
	n := root
	for {
		descend := true
		if n.Type() == ast.TypeBlock && !atLineStart {
			write("\n")
		}
		switch v := n.(type) {
		case *ast.Text:
			if b.md && !v.IsRaw() {
				write(b.decode(v.Value(b.src)))
			} else {
				write(string(v.Value(b.src)))
			}
			if v.SoftLineBreak() || v.HardLineBreak() {
				write("\n")
			}
		case *ast.String:
			write(string(v.Value))
		case *ast.AutoLink:
			write(string(v.Label(b.src)))
			descend = false
		case *ast.RawHTML:
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				write(string(seg.Value(b.src)))
			}
			descend = false
		case *ast.FencedCodeBlock:
			write(string(v.Lines().Value(b.src)))
			descend = false
		case *ast.CodeBlock:
			write(string(v.Lines().Value(b.src)))
			descend = false
		case *ast.HTMLBlock:
			write(string(v.Lines().Value(b.src)))
			if v.HasClosure() {
				write(string(v.ClosureLine.Value(b.src)))
			}
			descend = false
		}
		if descend && n.FirstChild() != nil {
			n = n.FirstChild()
			continue
		}
		for n != root && n.NextSibling() == nil {
			n = n.Parent()
		}
		if n == root {
			break
		}
		n = n.NextSibling()
	}
	return strings.Trim(sb.String(), "\n")
}

// smileyIndex prepares the smiley list: it drops a code that is empty or over
// 32 characters, and sorts the rest by first byte, longest code first.
func (b *builder) smileyIndex() {
	for _, s := range b.opt.SmileyList {
		// Hold a code to the rule of Validate: valid UTF-8, 1 to 32 characters,
		// and no control character. A code that fails it would give a tree that
		// Validate rejects.
		if !utf8.ValidString(s.Code) || !tokenLen(1, maxSmileyRunes)(s.Code) {
			continue
		}
		if b.smileys == nil {
			b.smileys = map[byte][]Smiley{}
		}
		list := b.smileys[s.Code[0]]
		i := 0
		for i < len(list) && len(list[i].Code) >= len(s.Code) {
			i++
		}
		list = append(list, Smiley{})
		copy(list[i+1:], list[i:])
		list[i] = s
		b.smileys[s.Code[0]] = list
	}
}

// smilify finds smiley codes in the text nodes of nodes, which an element at
// the given depth holds. It does not look inside code, url, email or img.
func (b *builder) smilify(nodes []*Node, depth int) []*Node {
	if len(b.smileys) == 0 {
		return nodes
	}
	out := make([]*Node, 0, len(nodes))
	for _, n := range nodes {
		switch {
		case n.Kind == Text:
			out = b.splitSmilies(out, n.Text, depth)
		case n.Name == "b" || n.Name == "i" || n.Name == "spoiler":
			n.Children = b.smilify(n.Children, depth+1)
			out = append(out, n)
		default:
			out = append(out, n)
		}
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// splitSmilies appends s to out, with a smiley element for each code in it. A
// code matches between word boundaries only, and the longest code wins.
func (b *builder) splitSmilies(out []*Node, s string, depth int) []*Node {
	if depth >= MaxDepth {
		return append(out, newText(s))
	}
	last := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		cands := b.smileys[s[i]]
		if len(cands) > 0 && (i == 0 || !wordBefore(s[:i])) {
			for _, sm := range cands {
				end := i + len(sm.Code)
				if strings.HasPrefix(s[i:], sm.Code) && (end == len(s) || !wordAfter(s[end:])) {
					if i > last {
						out = append(out, newText(s[last:i]))
					}
					out = append(out, newEl("smiley", map[string]string{"code": sm.Code}, nil))
					last, i = end, end-size
					break
				}
			}
		}
		i += size
	}
	if last < len(s) {
		out = append(out, newText(s[last:]))
	}
	return out
}

func wordBefore(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return isWordRune(r)
}

func wordAfter(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return isWordRune(r)
}

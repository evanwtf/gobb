package markup

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Error is the error that [Validate] returns. It names the offending node by
// its path from the root. Each path segment is the node's name and its index in
// its parent, for example "/quote[0]/p[1]/url[2]". The root's path is "/".
type Error struct {
	Path    string // path of the offending node
	Element string // its element name, "#text", "#root" or "nil"
	Msg     string // what is wrong
}

// Error returns the message in the form "markup: PATH: MESSAGE".
func (e *Error) Error() string { return "markup: " + e.Path + ": " + e.Msg }

// class says where an element may stand.
type class uint8

const (
	clsBlock  class = iota // a block: the root, a quote or an li may hold it
	clsInline              // an inline: a p, h or inline element may hold it
	clsLi                  // an li: only a list may hold it
)

// kids says what an element may hold.
type kids uint8

const (
	kidsNone   kids = iota // nothing
	kidsText               // text nodes only
	kidsInline             // text nodes and inline elements
	kidsBlock              // block elements only
	kidsLi                 // li elements only
)

// attrRule is the rule for one attribute of one element.
type attrRule struct {
	name     string
	required bool
	ok       func(string) bool
	msg      string // the error text when the value fails
}

// elemRule is the rule for one element.
type elemRule struct {
	class    class
	kids     kids
	attrs    []attrRule
	relation func(attrs map[string]string) string // checks attributes together; "" when fine
}

var elemRules = buildRules()

func buildRules() map[string]*elemRule {
	embed := func(tag string) *elemRule {
		return &elemRule{class: clsBlock, kids: kidsNone, attrs: []attrRule{{
			name: "id", required: true, msg: "id is not a canonical " + tag + " ID",
			ok: func(v string) bool {
				id, ok := EmbedID(tag, v)
				return ok && id == v
			},
		}}}
	}
	inline := func(k kids, attrs ...attrRule) *elemRule {
		return &elemRule{class: clsInline, kids: k, attrs: attrs}
	}
	block := func(k kids, attrs ...attrRule) *elemRule {
		return &elemRule{class: clsBlock, kids: k, attrs: attrs}
	}
	rules := map[string]*elemRule{
		"p": block(kidsInline),
		"h": block(kidsInline,
			attrRule{name: "level", required: true, ok: oneOf("1", "2", "3", "4", "5", "6"), msg: "level must be 1 to 6"}),
		"hr":    block(kidsNone),
		"quote": block(kidsBlock),
		"list": {class: clsBlock, kids: kidsLi, attrs: []attrRule{
			{name: "ordered", ok: oneOf("true"), msg: "ordered must be true"},
			{name: "start", ok: isListStart, msg: "start must be 1 to 9 digits with no leading zero"},
			{name: "tight", ok: oneOf("true"), msg: "tight must be true"},
		}, relation: func(attrs map[string]string) string {
			if _, ok := attrs["start"]; ok && attrs["ordered"] != "true" {
				return "start needs ordered"
			}
			return ""
		}},
		"li": {class: clsLi, kids: kidsBlock},
		"pre": block(kidsText,
			attrRule{name: "lang", ok: isLang, msg: "lang must be 1 to 32 characters from A-Z a-z 0-9 _ + . # -"}),
		"code": inline(kidsText),
		"b":    inline(kidsInline),
		"i":    inline(kidsInline),
		"br":   inline(kidsNone),
		"url": inline(kidsInline,
			attrRule{name: "href", required: true, ok: CheckLinkURL, msg: "href not allowed"},
			attrRule{name: "display", ok: textLen(1, 255), msg: "display must be 1 to 255 characters, with no control characters"}),
		"email": inline(kidsInline,
			attrRule{name: "address", required: true, ok: isEmail, msg: "address is not a plain email address"}),
		"img": inline(kidsNone,
			attrRule{name: "src", required: true, ok: CheckImageURL, msg: "src not allowed"},
			attrRule{name: "alt", ok: textLen(0, 1000), msg: "alt must be at most 1000 characters, with no control characters"}),
		"spoiler": inline(kidsInline),
		"smiley": inline(kidsNone,
			attrRule{name: "code", required: true, ok: tokenLen(1, 32), msg: "code must be 1 to 32 characters, with no control characters"}),
	}
	for name := range embedRules {
		rules[name] = embed(name)
	}
	return rules
}

// frame is one open element on the walk's stack.
type frame struct {
	n         *Node
	kids      kids // what this node may hold; the root holds blocks
	next      int  // index of the next child to visit
	quotes    int  // quote elements on the path, this one included
	inLink    bool // this element is, or sits inside, a url or email
	inSpoiler bool // this element is, or sits inside, a spoiler
}

// Validate checks a whole tree against the schema and returns the first error
// it finds, as an *[Error]. It returns nil when the tree is valid.
//
// Validate treats the tree as hostile. It never panics, and it walks the tree
// with an explicit stack, so a deep tree cannot exhaust the call stack. It stops
// at the first element deeper than [MaxDepth], so it does not walk the rest of
// a long chain. It also rejects a node that appears twice in the tree. That
// rules out cycles, and it stops a small input from expanding into a huge walk.
//
// Beyond the schema table, Validate requires every text and attribute value to
// be valid UTF-8.
func Validate(root *Node) error {
	if root == nil {
		return &Error{Path: "/", Element: "#root", Msg: "root is nil"}
	}
	if root.Kind != Root {
		return &Error{Path: "/", Element: "#root", Msg: "top node must have kind root"}
	}
	if root.Name != "" || len(root.Attrs) > 0 || root.Text != "" {
		return &Error{Path: "/", Element: "#root", Msg: "root has a name, attributes or text"}
	}
	seen := map[*Node]struct{}{root: {}}
	stack := []frame{{n: root, kids: kidsBlock}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.next >= len(top.n.Children) {
			stack = stack[:len(stack)-1]
			continue
		}
		c := top.n.Children[top.next]
		top.next++
		if c == nil {
			return fail(stack, nil, "nil child")
		}
		if _, dup := seen[c]; dup {
			return fail(stack, c, "node appears more than once in the tree")
		}
		seen[c] = struct{}{}
		if msg := checkNode(top, c, len(stack)); msg != "" {
			return fail(stack, c, msg)
		}
		if len(c.Children) > 0 {
			quotes := top.quotes
			if c.Name == "quote" {
				quotes++
			}
			stack = append(stack, frame{
				n:         c,
				kids:      elemRules[c.Name].kids,
				quotes:    quotes,
				inLink:    top.inLink || isLink(c.Name),
				inSpoiler: top.inSpoiler || c.Name == "spoiler",
			})
		}
	}
	return nil
}

func isLink(name string) bool { return name == "url" || name == "email" }

// checkNode checks child c of parent p. depth is the depth c has if it is an
// element. It returns an error message, or "" when c is valid.
func checkNode(p *frame, c *Node, depth int) string {
	switch c.Kind {
	case Text:
		if c.Name != "" || len(c.Attrs) > 0 || len(c.Children) > 0 {
			return "text node has a name, attributes or children"
		}
		if !utf8.ValidString(c.Text) {
			return "text is not valid UTF-8"
		}
		switch p.kids {
		case kidsText, kidsInline:
			return ""
		case kidsLi:
			return "list may hold only li elements"
		}
		return "text not allowed in " + parentLabel(p)
	case Root:
		return "root node inside the tree"
	case Element:
	default:
		return "unknown node kind " + strconv.Itoa(int(c.Kind))
	}

	rule := elemRules[c.Name]
	if rule == nil {
		return "unknown element"
	}
	if c.Text != "" {
		return "element has text"
	}
	if depth > MaxDepth {
		return "element nesting deeper than " + strconv.Itoa(MaxDepth)
	}
	if c.Name == "quote" && p.quotes+1 > MaxQuoteDepth {
		return "quote nesting deeper than " + strconv.Itoa(MaxQuoteDepth)
	}
	if msg := checkPlace(p, c.Name, rule); msg != "" {
		return msg
	}
	if isLink(c.Name) && p.inLink {
		return "url and email may not nest inside url or email"
	}
	if c.Name == "spoiler" && p.inSpoiler {
		return "spoiler may not nest inside spoiler"
	}
	if rule.kids == kidsNone && len(c.Children) > 0 {
		return c.Name + " takes no children"
	}
	return checkAttrs(rule, c.Attrs)
}

// checkPlace checks that the parent frame p may hold element name.
func checkPlace(p *frame, name string, rule *elemRule) string {
	if rule.class == clsLi && p.kids != kidsLi {
		return "li outside list"
	}
	switch p.kids {
	case kidsText:
		return parentLabel(p) + " may hold only text"
	case kidsLi:
		if rule.class != clsLi {
			return "list may hold only li elements"
		}
	case kidsInline:
		if rule.class != clsInline {
			return name + " not allowed in " + parentLabel(p)
		}
	case kidsBlock:
		if rule.class != clsBlock {
			return name + " not allowed in " + parentLabel(p)
		}
	}
	return ""
}

// parentLabel names the node of frame p for an error message.
func parentLabel(p *frame) string {
	if p.n.Kind == Root {
		return "root"
	}
	return label(p.n.Name)
}

// checkAttrs checks an element's attributes against its rule.
func checkAttrs(rule *elemRule, attrs map[string]string) string {
	if len(attrs) > 0 {
		if msg := unknownAttr(rule, attrs); msg != "" {
			return msg
		}
	}
	for _, ar := range rule.attrs {
		v, ok := attrs[ar.name]
		switch {
		case !ok && ar.required:
			return "missing attribute " + ar.name
		case !ok:
		case !utf8.ValidString(v):
			return ar.name + " is not valid UTF-8"
		case !ar.ok(v):
			return ar.msg
		}
	}
	if rule.relation != nil {
		return rule.relation(attrs)
	}
	return ""
}

// unknownAttr returns an error message for the first attribute, in sorted
// order, that the rule does not list. It returns "" when there is none.
func unknownAttr(rule *elemRule, attrs map[string]string) string {
	var bad []string
	for k := range attrs {
		known := false
		for _, ar := range rule.attrs {
			if ar.name == k {
				known = true
				break
			}
		}
		if !known {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return "unknown attribute " + label(bad[0])
}

// fail builds an *Error for child c of the open element on top of stack. A nil
// c stands for a nil child.
func fail(stack []frame, c *Node, msg string) *Error {
	var b strings.Builder
	for j := 1; j < len(stack); j++ {
		segment(&b, stack[j].n, stack[j-1].next-1)
	}
	segment(&b, c, stack[len(stack)-1].next-1)
	return &Error{Path: b.String(), Element: segName(c), Msg: msg}
}

func segment(b *strings.Builder, n *Node, idx int) {
	b.WriteByte('/')
	b.WriteString(segName(n))
	b.WriteByte('[')
	b.WriteString(strconv.Itoa(idx))
	b.WriteByte(']')
}

// segName names a node for an error. An attacker controls element names, so a
// name that is not plain is quoted and cut short.
func segName(n *Node) string {
	switch {
	case n == nil:
		return "nil"
	case n.Kind == Text:
		return "#text"
	case n.Kind == Root:
		return "#root"
	case n.Kind != Element:
		return "#kind" + strconv.Itoa(int(n.Kind))
	}
	return label(n.Name)
}

// label returns s as is when it is a short plain name, and a short quoted form
// otherwise.
func label(s string) string {
	if len(s) > 0 && len(s) <= 32 {
		plain := true
		for i := 0; i < len(s); i++ {
			c := s[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				plain = false
				break
			}
		}
		if plain {
			return s
		}
	}
	if len(s) > 32 {
		s = s[:32] + "..."
	}
	return strconv.Quote(s)
}

func oneOf(set ...string) func(string) bool {
	return func(s string) bool {
		for _, w := range set {
			if s == w {
				return true
			}
		}
		return false
	}
}

// runeLen accepts a string of minLen to maxLen characters.
func runeLen(minLen, maxLen int) func(string) bool {
	return func(s string) bool {
		if len(s) < minLen || len(s) > maxLen*utf8.UTFMax {
			return false
		}
		n := utf8.RuneCountInString(s)
		return n >= minLen && n <= maxLen
	}
}

// textLen accepts a string of minLen to maxLen characters. It may hold tab,
// newline and carriage return, but no other control character.
func textLen(minLen, maxLen int) func(string) bool {
	n := runeLen(minLen, maxLen)
	return func(s string) bool {
		if !n(s) {
			return false
		}
		for _, r := range s {
			if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
				return false
			}
		}
		return true
	}
}

// tokenLen accepts a string of minLen to maxLen characters with no control
// character at all.
func tokenLen(minLen, maxLen int) func(string) bool {
	n := runeLen(minLen, maxLen)
	return func(s string) bool {
		return n(s) && strings.IndexFunc(s, unicode.IsControl) < 0
	}
}

// isListStart accepts 1 to 9 digits with no leading zero. That also rules out 0.
func isListStart(v string) bool { return digitsOnly(v, 1, 9) && v[0] != '0' }

// isLang accepts 1 to 32 bytes from A-Z, a-z, 0-9 and "_+.#-".
func isLang(v string) bool {
	if len(v) < 1 || len(v) > 32 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("_+.#-", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// cleanURL prepares a link or image target for checking. The target is a
// literal string: cleanURL does not decode HTML entities or percent escapes. It
// trims ASCII whitespace at both ends. It then rejects an empty value, an
// over-long value, and any whitespace, control character, Unicode format
// character (such as U+200B, U+00AD or U+202E) or backslash. It returns the
// cleaned value and whether it passed. The cleaned value is the string that the
// renderer emits, HTML-escaped.
func cleanURL(v string) (string, bool) {
	if len(v) > 8192 {
		return "", false
	}
	v = strings.Trim(v, " \t\n\r\f\v")
	if v == "" {
		return "", false
	}
	for _, r := range v {
		if r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", false
		}
	}
	return v, true
}

// isSitePath reports whether v, after cleanURL, is a path on this site. It
// starts with "/" but not with "//" or "/\\", which a browser reads as a host.
func isSitePath(v string) bool {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "" && u.Host == ""
}

// isHTTPURL reports whether v, after cleanURL, is an http or https URL with a
// host, as net/url reads it.
func isHTTPURL(v string) bool {
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return false
	}
	return u.Hostname() != ""
}

// CheckLinkURL reports whether s is allowed as the target of a link. It is the
// one rule that [Validate], the parser and the renderer share. A target is a
// literal string: nothing decodes HTML entities in it, so "&copy=1" in a query
// stays as written. The check trims ASCII whitespace at both ends. It then
// rejects any whitespace, control character, format character or backslash. It
// accepts the result only in one of these forms: an http or https URL with a
// host; "mailto:" followed by an address that passes the email rule; or a site
// path, which starts with "/" but not with "//" or "/\". Anything else fails,
// including an entity-encoded form of another scheme, which has no allowed
// scheme.
//
// The check approves the trimmed string, not s. The renderer emits that same
// string: the value of cleanURL. It HTML-escapes the value, and it
// percent-encodes a mailto: address. It does not decode s.
func CheckLinkURL(s string) bool {
	v, ok := cleanURL(s)
	if !ok {
		return false
	}
	if len(v) >= len("mailto:") && strings.EqualFold(v[:len("mailto:")], "mailto:") {
		return isEmail(v[len("mailto:"):])
	}
	return isSitePath(v) || isHTTPURL(v)
}

// CheckImageURL reports whether s is allowed as the source of an image. It
// applies the same cleaning as [CheckLinkURL] and accepts an http or https URL
// with a host, or a site path. It rejects every mailto: value. As with
// [CheckLinkURL], the source is a literal string, and the renderer emits the
// trimmed string that the check approved, HTML-escaped.
func CheckImageURL(s string) bool {
	v, ok := cleanURL(s)
	return ok && (isSitePath(v) || isHTTPURL(v))
}

// isEmail accepts one "@" with text on both sides, and no whitespace, control
// character, Unicode format character, backslash, colon, angle bracket, quote,
// or any of "?#%&/". The last group would change the meaning of a mailto: URL
// built from the address.
func isEmail(v string) bool {
	if len(v) > 1024 || strings.Count(v, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(v, "@")
	if local == "" || domain == "" {
		return false
	}
	for _, r := range v {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(":<>\"'?#%&/\\", r) {
			return false
		}
	}
	return true
}

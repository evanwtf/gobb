package bbcode

import (
	"html"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Error is the error that [Validate] returns. It names the offending node by
// its path from the root. Each path segment is the node's name and its index in
// its parent, for example "/quote[0]/url[2]". The root's path is "/".
type Error struct {
	Path    string // path of the offending node
	Element string // its element name, "#text", "#root" or "nil"
	Msg     string // what is wrong
}

// Error returns the message in the form "bbcode: PATH: MESSAGE".
func (e *Error) Error() string { return "bbcode: " + e.Path + ": " + e.Msg }

type childRule uint8

const (
	childAny  childRule = iota // any node
	childNone                  // no children
	childText                  // text nodes only
	childList                  // li elements and whitespace-only text
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
	attrs    []attrRule
	children childRule
}

var elemRules = buildRules()

func buildRules() map[string]*elemRule {
	embed := func(tag string) *elemRule {
		return &elemRule{children: childNone, attrs: []attrRule{{
			name: "id", required: true, msg: "id is not a canonical " + tag + " ID",
			ok: func(v string) bool {
				id, ok := EmbedID(tag, v)
				return ok && id == v
			},
		}}}
	}
	rules := map[string]*elemRule{
		"quote": {attrs: []attrRule{
			{name: "author", ok: runeLen(1, 255), msg: "author must be 1 to 255 characters"},
			{name: "post", ok: digits(1, 19), msg: "post must be 1 to 19 digits"},
			{name: "time", ok: digits(1, 19), msg: "time must be 1 to 19 digits"},
			{name: "user", ok: digits(1, 19), msg: "user must be 1 to 19 digits"},
		}},
		"code": {children: childText},
		"list": {children: childList, attrs: []attrRule{
			{name: "type", ok: oneOf("1", "a", "A", "i", "I", "disc", "circle", "square"), msg: "type is not allowed"},
		}},
		"li": {},
		"url": {attrs: []attrRule{
			{name: "href", required: true, ok: isHTTPURL, msg: "href scheme not allowed"},
			{name: "display", ok: anyString, msg: "display is invalid"},
		}},
		"email": {attrs: []attrRule{
			{name: "address", required: true, ok: isEmail, msg: "address is not a plain email address"},
		}},
		"img": {children: childNone, attrs: []attrRule{
			{name: "src", required: true, ok: isHTTPURL, msg: "src scheme not allowed"},
		}},
		"color": {attrs: []attrRule{
			{name: "value", required: true, ok: isColor, msg: "value is not a color"},
		}},
		"size": {attrs: []attrRule{
			{name: "value", required: true, ok: isSize, msg: "value must be an integer from 50 to 200"},
		}},
		"attachment": {children: childNone, attrs: []attrRule{
			{name: "index", required: true, ok: digits(1, 9), msg: "index must be 1 to 9 digits"},
			{name: "name", ok: anyString, msg: "name is invalid"},
		}},
		"smiley": {children: childNone, attrs: []attrRule{
			{name: "code", required: true, ok: runeLen(1, 32), msg: "code must be 1 to 32 characters"},
		}},
		"spoiler": {attrs: []attrRule{
			{name: "label", ok: runeLen(1, 100), msg: "label must be 1 to 100 characters"},
		}},
		"youtube":   embed("youtube"),
		"vimeo":     embed("vimeo"),
		"tweet":     embed("tweet"),
		"instagram": embed("instagram"),
	}
	for _, n := range []string{"b", "i", "u", "shout", "comic"} {
		rules[n] = &elemRule{}
	}
	return rules
}

// frame is one open element on the walk's stack.
type frame struct {
	n      *Node
	next   int  // index of the next child to visit
	quotes int  // quote elements on the path, this one included
	inLink bool // this element is, or sits inside, a url or email
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
	stack := []frame{{n: root}}
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
			stack = append(stack, frame{n: c, quotes: quotes, inLink: top.inLink || isLink(c.Name)})
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
		if p.n.Name == "list" && !isBlank(c.Text) {
			return "list may hold only li elements and whitespace"
		}
		return ""
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
	switch p.n.Name {
	case "code":
		return "code may hold only text"
	case "list":
		if c.Name != "li" {
			return "list may hold only li elements and whitespace"
		}
	}
	if c.Name == "li" && p.n.Name != "list" {
		return "li outside list"
	}
	if isLink(c.Name) && p.inLink {
		return "url and email may not nest inside url or email"
	}
	if rule.children == childNone && len(c.Children) > 0 {
		return "element takes no children"
	}
	return checkAttrs(rule, c.Attrs)
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

// isBlank reports whether s holds only ASCII whitespace.
func isBlank(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f':
		default:
			return false
		}
	}
	return true
}

func anyString(string) bool { return true }

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

func digits(min, max int) func(string) bool {
	return func(s string) bool { return digitsOnly(s, min, max) }
}

// runeLen accepts a string of min to max characters.
func runeLen(min, max int) func(string) bool {
	return func(s string) bool {
		if len(s) < min || len(s) > max*utf8.UTFMax {
			return false
		}
		n := utf8.RuneCountInString(s)
		return n >= min && n <= max
	}
}

// isHTTPURL is the allowlist for link and image targets. It decodes HTML
// entities once and trims ASCII whitespace at both ends. It then rejects any
// whitespace, control character or backslash. It accepts the value only when
// net/url reads it as an http or https URL with a host. Anything else fails,
// including every obfuscated form of another scheme.
func isHTTPURL(v string) bool {
	if len(v) > 8192 {
		return false
	}
	v = strings.Trim(html.UnescapeString(v), " \t\n\r\f\v")
	if v == "" {
		return false
	}
	for _, r := range v {
		if r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return false
	}
	return u.Hostname() != ""
}

// isEmail accepts one "@" with text on both sides, and no whitespace, control
// character, colon, angle bracket or quote.
func isEmail(v string) bool {
	if len(v) > 1024 || strings.Count(v, "@") != 1 {
		return false
	}
	local, domain, _ := strings.Cut(v, "@")
	if local == "" || domain == "" {
		return false
	}
	for _, r := range v {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(":<>\"'", r) {
			return false
		}
	}
	return true
}

// isColor accepts "#rgb", "#rrggbb", or 3 to 20 lower-case ASCII letters.
func isColor(v string) bool {
	if strings.HasPrefix(v, "#") {
		if len(v) != 4 && len(v) != 7 {
			return false
		}
		for i := 1; i < len(v); i++ {
			c := v[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		return true
	}
	if len(v) < 3 || len(v) > 20 {
		return false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 'a' || v[i] > 'z' {
			return false
		}
	}
	return true
}

// isSize accepts an integer from 50 to 200 written in plain decimal digits.
func isSize(v string) bool {
	if !digitsOnly(v, 2, 3) || v[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 50 && n <= 200
}

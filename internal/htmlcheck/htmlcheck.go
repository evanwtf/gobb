// Package htmlcheck checks the HTML that gobb writes for member text. Only test
// files import it, so golang.org/x/net/html stays out of the gobb binary; a test
// in this package fails if that changes.
//
// The check is strict on purpose. It reads the tags of a fragment as a browser
// would, without repairing them, and it allows only what [markup.Render] can
// write: the elements and attributes of [markup.Allowed], the URL rules of
// [markup.CheckLinkURL] and [markup.CheckImageURL], and the frame origins of
// [markup.EmbedOrigins]. It reads the same table as the renderer, so the two
// cannot disagree. A page test calls [Check] on each element that holds a post
// or a signature.
package htmlcheck

import (
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/evanwtf/gobb/markup"
)

// forbidden are the elements that no member text may ever produce. Each is also
// outside the allowlist; the check names it so that the report says why.
var forbidden = []string{"script", "style", "object", "embed", "base", "form", "meta", "link"}

// maxShown is the most characters of the fragment that a report shows.
const maxShown = 2000

// Check fails t for every problem in fragment. It reports all of them, not the
// first, and it shows the fragment.
//
// The fragment must be the raw bytes of the inner HTML of the element, as the
// server wrote them. Do not pass a node that html.Parse built and rendered again:
// the tree builder repairs the faults that this check exists to see (an
// unclosed tag, a stray end tag, a tag in the wrong place, a repeated attribute).
func Check(t testing.TB, fragment string) {
	t.Helper()
	problems := Problems(fragment)
	if len(problems) == 0 {
		return
	}
	shown := fragment
	if len(shown) > maxShown {
		shown = shown[:maxShown] + " ..."
	}
	for _, p := range problems {
		t.Errorf("htmlcheck: %s\nin: %s", p, shown)
	}
}

// Problems returns the problems in fragment, or nil when it is clean. Check
// fails a test with them; the tests of this package read them directly.
func Problems(fragment string) []string {
	c := &checker{allowed: markup.Allowed(), origins: markup.EmbedOrigins(), seen: map[string]bool{}}
	z := html.NewTokenizer(strings.NewReader(fragment))
	for {
		switch z.Next() {
		case html.ErrorToken:
			if err := z.Err(); err != io.EOF {
				c.add("parse error: %v", err)
			}
			// An incomplete tag at the end gives an error token that still holds the
			// bytes it dropped.
			if raw := z.Raw(); len(raw) > 0 {
				c.add("incomplete tag %q", raw)
			}
			for _, name := range c.open {
				c.add("unclosed <%s>", name)
			}
			return c.problems
		case html.TextToken:
			if raw := string(z.Raw()); strings.ContainsAny(raw, "<>") {
				c.add("angle bracket in text %q: the renderer escapes both", raw)
			}
		case html.CommentToken:
			c.add("comment or declaration %q", z.Raw())
		case html.DoctypeToken:
			c.add("doctype %q", z.Raw())
		case html.StartTagToken, html.SelfClosingTagToken:
			c.element(tagOf(z), true)
		case html.EndTagToken:
			c.element(tagOf(z), false)
		}
	}
}

// attr is one attribute as the tokenizer read it.
type attr struct{ key, val string }

// tag is one start or end tag.
type tag struct {
	name     string
	attrs    []attr   // the attributes that the tokenizer kept
	rawNames []string // the name of every attribute in the raw tag
}

// tagOf reads the current tag. The tokenizer keeps the first of a repeated
// attribute and drops the others, as the HTML standard says, so the names of
// all attributes come from the raw bytes of the tag.
func tagOf(z *html.Tokenizer) tag {
	raw := slices.Clone(z.Raw())
	name, more := z.TagName()
	t := tag{name: string(name), rawNames: rawAttrNames(raw)}
	for more {
		var k, v []byte
		k, v, more = z.TagAttr()
		t.attrs = append(t.attrs, attr{string(k), string(v)})
	}
	return t
}

// rawAttrNames returns the attribute names in the raw bytes of a start tag, in
// lower case and in order, repeats included. It follows the states of the HTML
// tokenizer: a name ends at white space, "/", ">" or "=", and a value is quoted
// or ends at white space or ">".
func rawAttrNames(raw []byte) []string {
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r' }
	i := 1 // after "<"
	for i < len(raw) && !isSpace(raw[i]) && raw[i] != '/' && raw[i] != '>' {
		i++
	}
	var names []string
	for i < len(raw) {
		for i < len(raw) && (isSpace(raw[i]) || raw[i] == '/') {
			i++
		}
		if i >= len(raw) || raw[i] == '>' {
			break
		}
		start := i
		i++ // a name may start with "="
		for i < len(raw) && !isSpace(raw[i]) && raw[i] != '/' && raw[i] != '>' && raw[i] != '=' {
			i++
		}
		names = append(names, strings.ToLower(string(raw[start:i])))
		for i < len(raw) && isSpace(raw[i]) {
			i++
		}
		if i < len(raw) && raw[i] == '=' {
			for i++; i < len(raw) && isSpace(raw[i]); i++ {
			}
			switch {
			case i < len(raw) && (raw[i] == '"' || raw[i] == '\''):
				q := raw[i]
				for i++; i < len(raw) && raw[i] != q; i++ {
				}
				i++
			default:
				for i < len(raw) && !isSpace(raw[i]) && raw[i] != '>' {
					i++
				}
			}
		}
	}
	return names
}

// checker holds the state of one call of Problems.
type checker struct {
	allowed  map[string][]string
	origins  []string
	problems []string
	seen     map[string]bool // problems about a whole element, said once
	open     []string        // the start tags that have no end tag yet
}

// void are the elements that have no end tag.
var void = []string{"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track", "wbr"}

// track keeps the stack of open elements: a start tag of an element that is not
// void opens one, and an end tag must close the one on top. A self-closing slash
// does nothing on an element that is not void, as in a browser.
func (c *checker) track(name string, start bool) {
	if slices.Contains(void, name) {
		if !start {
			c.add("end tag </%s> has no start tag: <%s> is a void element", name, name)
		}
		return
	}
	if start {
		c.open = append(c.open, name)
		return
	}
	switch top := len(c.open) - 1; {
	case top < 0:
		c.add("end tag </%s> has no start tag", name)
	case c.open[top] != name:
		c.add("end tag </%s> does not match <%s>", name, c.open[top])
	default:
		c.open = c.open[:top]
	}
}

func (c *checker) add(format string, args ...any) {
	c.problems = append(c.problems, sprintf(format, args...))
}

// once adds a problem about an element name the first time only.
func (c *checker) once(format, name string) {
	msg := sprintf(format, name)
	if !c.seen[msg] {
		c.seen[msg] = true
		c.problems = append(c.problems, msg)
	}
}

// element checks one tag. start says that it is a start tag, which can carry
// attributes.
func (c *checker) element(tok tag, start bool) {
	name := tok.name
	c.track(name, start)
	if slices.Contains(forbidden, name) {
		c.once("forbidden element <%s>", name)
	}
	attrs, ok := c.allowed[name]
	if !ok {
		if !slices.Contains(forbidden, name) {
			c.once("element <%s> is not allowed", name)
		}
		return
	}
	if !start {
		return
	}
	seenName := map[string]bool{}
	for _, n := range tok.rawNames {
		if seenName[n] {
			c.add("duplicate attribute %s on <%s>", n, name)
		}
		seenName[n] = true
	}
	var href, src, hasSrc, hasHref = "", "", false, false
	for _, a := range tok.attrs {
		key := a.key
		if strings.HasPrefix(key, "on") {
			c.add("event handler attribute %s on <%s>", key, name)
		}
		if key == "style" {
			c.add("style attribute on <%s>", name)
		}
		if !slices.Contains(attrs, key) {
			c.add("attribute %s is not allowed on <%s>", key, name)
		} else if !markup.AttrValueOK(name, key, a.val) {
			c.add("attribute %s=%q on <%s> is not a value that the renderer writes", key, a.val, name)
		}
		switch key {
		case "href":
			href, hasHref = a.val, true
		case "src":
			src, hasSrc = a.val, true
		case "data-instgrm-permalink", "data-text-post-permalink":
			if !linkOK(a.val) {
				c.add("%s %q fails the URL rules", key, a.val)
			}
		}
	}
	switch {
	case name == "iframe":
		if !c.embedOK(src) {
			c.add("iframe src %q is not under an origin of the embed table", src)
		}
	case hasSrc && !imageOK(src):
		c.add("src %q fails the URL rules", src)
	case name == "img" && !hasSrc:
		c.add("<img> has no src")
	}
	if hasHref && !linkOK(href) {
		c.add("href %q fails the URL rules", href)
	}
	if name == "a" && !hasHref {
		c.add("<a> has no href")
	}
}

// trimmed reports whether v has no white space at either end. The renderer
// writes the trimmed value that the URL rules approved, so a value with white
// space there did not come from it.
func trimmed(v string) bool { return v == strings.Trim(v, " \t\n\r\f\v") }

// schemeOK is a second, independent look at a URL, so that a fault in the shared
// rule of the markup package cannot blind the check. A browser reads a scheme
// when a colon comes before any "/", "?" or "#": the scheme must be one of the
// allowed ones, and the URL must hold no character that a browser removes or
// treats as white space (tab, line feed, carriage return, NUL, any other
// control character, or a backslash).
func schemeOK(v string, schemes ...string) bool {
	for _, r := range v {
		if r < 0x20 || r == 0x7f || r == '\\' || (r >= 0x80 && r <= 0x9f) {
			return false
		}
	}
	if i := strings.IndexAny(v, ":/?#"); i >= 0 && v[i] == ':' {
		return slices.Contains(schemes, strings.ToLower(v[:i]))
	}
	return true
}

// linkOK reports whether v is a link target that the renderer could write: it
// passes the rule for a link. A mailto: address is percent-encoded in the HTML,
// so the check decodes it first, as a mail program does.
func linkOK(v string) bool {
	if !trimmed(v) || !schemeOK(v, "http", "https", "mailto") {
		return false
	}
	if len(v) >= len("mailto:") && strings.EqualFold(v[:len("mailto:")], "mailto:") {
		addr, err := url.PathUnescape(v[len("mailto:"):])
		return err == nil && markup.CheckLinkURL("mailto:"+addr)
	}
	return markup.CheckLinkURL(v)
}

// imageOK reports whether v is an image source that the renderer could write.
func imageOK(v string) bool {
	return trimmed(v) && schemeOK(v, "http", "https") && markup.CheckImageURL(v)
}

// embedOK reports whether v is an address in a frame origin of the embed table:
// an https URL whose host is exactly that of an origin, with no user name.
func (c *checker) embedOK(v string) bool {
	if !trimmed(v) || !markup.CheckImageURL(v) {
		return false
	}
	u, err := url.Parse(v)
	if err != nil || u.User != nil || u.Scheme != "https" {
		return false
	}
	for _, o := range c.origins {
		if strings.HasPrefix(v, o+"/") && "https://"+u.Host == o {
			return true
		}
	}
	return false
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

package markup

import (
	"html"
	"slices"
	"sort"
	"strings"
)

// allowed is the table of every HTML element that [Render] may write, with the
// attributes that each one may carry. It is the one source: the renderer writes
// each tag through [openTag] and [closeTag], which read the table, and the HTML
// check of the tests reads it through [Allowed]. A tag or an attribute that is
// not in the table cannot reach the output, and a check that reads the table
// cannot disagree with the renderer about what is allowed.
//
// The heading levels share the table: the post title is the h1 of the page and
// the h2 of the post, so a heading of the post is an h3 to an h6.
//
// Notes on the entries:
//   - class marks the code block, the code language, the image, the smiley, the
//     spoiler, the note that replaces an image and the embeds.
//   - The two data attributes of blockquote hold the permalink that the script of
//     an embed reads. The check requires each to pass [CheckLinkURL].
//   - iframe holds only embeds. Its src is one of the origins in [EmbedOrigins].
var allowed = map[string][]string{
	"p":          nil,
	"br":         nil,
	"hr":         nil,
	"h3":         nil,
	"h4":         nil,
	"h5":         nil,
	"h6":         nil,
	"ul":         nil,
	"ol":         {"start"},
	"li":         nil,
	"pre":        {"class"},
	"code":       {"class"},
	"strong":     nil,
	"em":         nil,
	"blockquote": {"class", "data-instgrm-permalink", "data-text-post-permalink"},
	"a":          {"href", "rel"},
	"img":        {"src", "alt", "loading", "class"},
	"span":       {"class", "tabindex"},
	"div":        {"class"},
	"iframe":     {"class", "src", "title", "loading", "allow", "allowfullscreen", "referrerpolicy"},
}

// Allowed returns each element that [Render] may write, with the attributes that
// it may carry. The renderer and the HTML check of the tests both read this
// table, so they cannot disagree. The caller owns the result: a change to it
// does not change the table.
func Allowed() map[string][]string {
	out := make(map[string][]string, len(allowed))
	for tag, attrs := range allowed {
		out[tag] = slices.Clone(attrs)
	}
	return out
}

// embedOrigins are the origins that the embed table loads in frames. It holds
// each frame origin of embedHTML.
var embedOrigins = []string{
	originYouTube, originVimeo, originXFrame, originInstagram,
	originTikTok, originThreads, originSpotify, originSoundCloud,
}

// EmbedOrigins returns the frame origins of the embed table, sorted. The src of
// an iframe in the output of [Render] starts with one of them and a "/".
func EmbedOrigins() []string {
	out := slices.Clone(embedOrigins)
	sort.Strings(out)
	return out
}

// bare marks an attribute that has no value, such as allowfullscreen.
const bare = "\x00bare"

// openTag returns the opening tag of name with the attributes in kv, which hold
// names and values in turn. It escapes every value. It reads the table: a tag
// that the table lacks gives "", and an attribute that the table lacks for the
// tag is left out, so the renderer fails closed.
func openTag(name string, kv ...string) string {
	attrs, ok := allowed[name]
	if !ok {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('<')
	sb.WriteString(name)
	for i := 0; i+1 < len(kv); i += 2 {
		if !slices.Contains(attrs, kv[i]) {
			continue
		}
		sb.WriteByte(' ')
		sb.WriteString(kv[i])
		if kv[i+1] != bare {
			sb.WriteString(`="`)
			sb.WriteString(html.EscapeString(kv[i+1]))
			sb.WriteByte('"')
		}
	}
	sb.WriteByte('>')
	return sb.String()
}

// closeTag returns the closing tag of name, or "" when the table lacks it.
func closeTag(name string) string {
	if _, ok := allowed[name]; !ok {
		return ""
	}
	return "</" + name + ">"
}

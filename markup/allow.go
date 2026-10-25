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
	"blockquote": nil,
	"a":          {"href", "rel"},
	"img":        {"src", "alt", "loading", "class"},
	"span":       {"class", "tabindex"},
	"div":        {"class"},
	"iframe":     {"class", "src", "title", "loading", "allow", "allowfullscreen", "referrerpolicy"},
}

// fixedValues lists, for each attribute that the renderer always writes as a
// constant, the values that it writes. The HTML check accepts no other value, so
// a change that lets member text into one of these attributes (the permissions of
// an embed frame in allow, say) fails the check. The attributes that follow a
// rule instead of a list are in [AttrValueOK]: ol start and code class. The
// addresses and the alt text are free here: the check has its own rules for them.
// A test pins this table to the output of the renderer in both directions.
var fixedValues = map[string]map[string][]string{
	"a": {"rel": {"nofollow ugc noopener"}},
	"img": {
		"loading": {"lazy"},
		"class":   {"post-img", "smiley"},
	},
	"span": {
		"class":    {"spoiler", "img-note"},
		"tabindex": {"0"},
	},
	"div": {"class": {"embed-link"}},
	"pre": {"class": {"code"}},
	"iframe": {
		"class": {"embed youtube", "embed vimeo", "embed tiktok", "embed spotify", "embed soundcloud"},
		"title": {"YouTube video", "Vimeo video", "TikTok video", "Spotify", "SoundCloud"},
		"allow": {
			"encrypted-media; picture-in-picture; fullscreen",
			"fullscreen; picture-in-picture",
			"fullscreen",
			"encrypted-media; fullscreen; picture-in-picture",
		},
		"loading":         {"lazy"},
		"referrerpolicy":  {"strict-origin-when-cross-origin"},
		"allowfullscreen": {""},
	},
}

// AttrValueOK reports whether value is a value that the renderer can write for
// the attribute attr of the element tag. An attribute whose value is a constant
// accepts only the constants of the table. The start of an ol must be a number of
// 1 to 9 digits without a leading zero, and the class of a code element must be
// "language-" and a language name. An attribute with no fixed value (an address
// or an alt text) always passes: the HTML check has its own rules for it.
func AttrValueOK(tag, attr, value string) bool {
	switch tag + "/" + attr {
	case "ol/start":
		return isListStart(value)
	case "code/class":
		lang, ok := strings.CutPrefix(value, "language-")
		return ok && isLang(lang)
	}
	values, pinned := fixedValues[tag][attr]
	return !pinned || slices.Contains(values, value)
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
	originYouTube, originVimeo, originTikTok, originSpotify, originSoundCloud,
}

// EmbedOrigins returns the frame origins of the embed table, sorted. The src of
// an iframe in the output of [Render] starts with one of them and a "/".
func EmbedOrigins() []string {
	out := slices.Clone(embedOrigins)
	sort.Strings(out)
	return out
}

// valueless is the type of [bare]. A value of this type is the only way to write
// an attribute with no value. Member text is a string, so it cannot be one.
type valueless struct{}

// bare, as the value of an attribute in [openTag], writes the attribute with no
// value, as in allowfullscreen.
var bare = valueless{}

// openTag returns the opening tag of name with the attributes in kv, which hold
// names and values in turn. A value is a string, which openTag escapes, or
// [bare]. It reads the table: a tag that the table lacks gives "", and an
// attribute that the table lacks for the tag is left out, so the renderer fails
// closed. A value of any other type leaves the attribute out.
func openTag(name string, kv ...any) string {
	attrs, ok := allowed[name]
	if !ok {
		return ""
	}
	var sb strings.Builder
	sb.WriteByte('<')
	sb.WriteString(name)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		if !slices.Contains(attrs, key) {
			continue
		}
		switch v := kv[i+1].(type) {
		case string:
			sb.WriteString(" " + key + `="` + html.EscapeString(v) + `"`)
		case valueless:
			sb.WriteString(" " + key)
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

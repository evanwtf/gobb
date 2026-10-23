package markup

import (
	"html"
	"html/template"
	"net/url"
	"sort"
	"strings"
)

// RenderContext holds what the renderer needs from the site.
type RenderContext struct {
	// SmileyURL maps a smiley code to the URL of its image. It returns false
	// for an unknown code. A nil SmileyURL knows no code. The renderer checks the
	// URL with [CheckImageURL], so a bad URL renders the code as text.
	SmileyURL func(code string) (string, bool)
}

// Rendered is the result of [Render].
type Rendered struct {
	// HTML is the post as HTML. [Render] builds it by hand and escapes every text
	// and attribute value, so it is safe to put in a page as it is.
	HTML template.HTML
	// Text is the plain text of the post: everything except the contents of quote
	// elements and the embeds. Block elements and br elements end a line. It
	// feeds the search text of a post.
	Text string
	// FrameSrc lists the origins that the embeds of this tree load in frames.
	// It is sorted and has no duplicates.
	FrameSrc []string
	// ScriptSrc lists the origins of the scripts that the embeds of this tree
	// need. It is sorted and has no duplicates.
	ScriptSrc []string
}

// renderItem is one entry of the render stack. An entry with a nil n closes an
// element: it writes close and, for a block, ends the line of text.
type renderItem struct {
	n         *Node
	close     string // closing HTML, when n is nil
	block     bool   // closing a block element, when n is nil
	quote     bool   // the node is inside a quote element
	tight     bool   // an li: its list is tight
	inTightLi bool   // a p: its parent is an li of a tight list
}

// renderer holds the state of one [Render] call.
type renderer struct {
	rc      RenderContext
	html    strings.Builder
	text    strings.Builder
	textNL  bool // the text so far ends with a newline
	stack   []renderItem
	frames  map[string]struct{}
	scripts map[string]struct{}
	seen    map[*Node]struct{} // the nodes that Render has already visited
}

// Render turns a tree into HTML. It is the one place that writes the HTML of a
// post, so it escapes all text and every attribute value. It builds each tag by
// hand from a fixed table; an attribute reaches the output only after the same
// check that [Validate] makes, and the renderer writes the exact value it
// checked.
//
// Render does not need a tree that [Validate] accepted, because a stored tree
// can be older than a rule. It handles the cases like this:
//   - A url or email with a bad target renders its children, with no link.
//   - An img with a bad source renders its alt text.
//   - An embed with a bad ID renders nothing.
//   - An unknown element renders its children, without its attributes.
//   - A node of unknown kind, or a nil node, renders nothing.
//
// Render walks the tree with an explicit stack, so a deep tree cannot exhaust
// the call stack. It keeps the set of nodes it has visited, as [Validate] does,
// and renders nothing for a node that it has seen before. A cycle ends, and the
// work and the memory stay in proportion to the size of the tree. A valid tree
// shares no node, so the set never changes its output.
func Render(n *Node, rc RenderContext) Rendered {
	r := &renderer{rc: rc, seen: map[*Node]struct{}{}}
	switch {
	case n == nil:
	case n.Kind == Root:
		r.pushKids(n.Children, false, false, false)
	default:
		r.stack = append(r.stack, renderItem{n: n})
	}
	for len(r.stack) > 0 {
		it := r.stack[len(r.stack)-1]
		r.stack = r.stack[:len(r.stack)-1]
		if it.n == nil {
			r.html.WriteString(it.close)
			if it.block {
				r.lineBreak(it.quote)
			}
			continue
		}
		if _, dup := r.seen[it.n]; dup {
			continue
		}
		r.seen[it.n] = struct{}{}
		r.node(it)
	}
	return Rendered{
		HTML:      template.HTML(r.html.String()),
		Text:      strings.Trim(r.text.String(), "\n"),
		FrameSrc:  sortedKeys(r.frames),
		ScriptSrc: sortedKeys(r.scripts),
	}
}

// sortedKeys returns the keys of m in order, or nil when m is empty.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pushKids puts the children on the stack so that the first child is popped
// first. It sets tight, inTightLi and quote on each child; see renderItem.
func (r *renderer) pushKids(kids []*Node, tight, inTightLi, quote bool) {
	for i := len(kids) - 1; i >= 0; i-- {
		if kids[i] != nil {
			r.stack = append(r.stack, renderItem{n: kids[i], tight: tight, inTightLi: inTightLi, quote: quote})
		}
	}
}

// addText adds s to the plain text, unless the node is inside a quote.
func (r *renderer) addText(s string, quote bool) {
	if quote || s == "" {
		return
	}
	r.text.WriteString(s)
	r.textNL = s[len(s)-1] == '\n'
}

// lineBreak ends the current line of the plain text. It never writes an empty
// line, and it writes nothing at the start.
func (r *renderer) lineBreak(quote bool) {
	if quote || r.textNL || r.text.Len() == 0 {
		return
	}
	r.text.WriteByte('\n')
	r.textNL = true
}

// wrap writes open, then lets the children render, then writes close. block
// says that the element is a block, which starts and ends a line of text.
func (r *renderer) wrap(it renderItem, open, close string, block bool, childTight, childInTightLi, childQuote bool) {
	if block {
		r.lineBreak(it.quote)
	}
	r.html.WriteString(open)
	r.stack = append(r.stack, renderItem{close: close, block: block, quote: it.quote})
	r.pushKids(it.n.Children, childTight, childInTightLi, childQuote)
}

// children renders the children of the node and nothing around them.
func (r *renderer) children(it renderItem) {
	r.wrap(it, "", "", false, false, false, it.quote)
}

// node renders one node. It writes the opening HTML and puts the children and
// the closing HTML on the stack.
func (r *renderer) node(it renderItem) {
	n := it.n
	switch n.Kind {
	case Text:
		r.html.WriteString(html.EscapeString(n.Text))
		r.addText(n.Text, it.quote)
	case Element:
		r.element(it)
	}
	// Any other kind, a root node inside the tree included, renders nothing.
}

// element renders one element.
func (r *renderer) element(it renderItem) {
	n := it.n
	q := it.quote
	switch n.Name {
	case "p":
		if it.inTightLi {
			r.wrap(it, "", "", true, false, false, q)
		} else {
			r.wrap(it, "<p>", "</p>", true, false, false, q)
		}
	case "br":
		r.html.WriteString("<br>")
		r.addText("\n", q)
	case "h":
		tag := headingTag(n.Attrs["level"])
		r.wrap(it, "<"+tag+">", "</"+tag+">", true, false, false, q)
	case "hr":
		r.html.WriteString("<hr>")
	case "quote":
		r.wrap(it, "<blockquote>", "</blockquote>", true, false, false, true)
	case "list":
		r.list(it)
	case "li":
		r.wrap(it, "<li>", "</li>", true, false, it.tight, q)
	case "pre":
		open := `<pre class="code"><code>`
		if lang := n.Attrs["lang"]; isLang(lang) {
			open = `<pre class="code"><code class="language-` + html.EscapeString(lang) + `">`
		}
		r.wrap(it, open, "</code></pre>", true, false, false, q)
	case "code":
		r.wrap(it, "<code>", "</code>", false, false, false, q)
	case "b":
		r.wrap(it, "<strong>", "</strong>", false, false, false, q)
	case "i":
		r.wrap(it, "<em>", "</em>", false, false, false, q)
	case "url":
		r.link(it)
	case "email":
		r.email(it)
	case "img":
		r.image(it)
	case "spoiler":
		r.wrap(it, `<span class="spoiler" tabindex="0">`, "</span>", false, false, false, q)
	case "smiley":
		r.smiley(it)
	default:
		if IsEmbed(n.Name) {
			r.embed(it)
			return
		}
		r.children(it) // an unknown element: children only
	}
}

// headingTag maps a heading level to a tag. The post title is the h1 of the
// page and the h2 of the post, so level 1 is h3, and the levels 4 to 6 share
// h6. A bad level also gives h6.
func headingTag(level string) string {
	switch level {
	case "1":
		return "h3"
	case "2":
		return "h4"
	case "3":
		return "h5"
	}
	return "h6"
}

// list renders a list or an ordered list. Its li children learn whether the
// list is tight.
func (r *renderer) list(it renderItem) {
	attrs := it.n.Attrs
	tag := "ul"
	open := "<ul>"
	if attrs["ordered"] == "true" {
		tag = "ol"
		open = "<ol>"
		if s := attrs["start"]; isListStart(s) {
			open = `<ol start="` + html.EscapeString(s) + `">`
		}
	}
	r.wrap(it, open, "</"+tag+">", true, attrs["tight"] == "true", false, it.quote)
}

// cleanLinkTarget returns the value that CheckLinkURL checked, or false.
func cleanLinkTarget(raw string) (string, bool) {
	v, ok := cleanURL(raw)
	return v, ok && CheckLinkURL(raw)
}

// cleanImageTarget returns the value that CheckImageURL checked, or false. An
// http URL becomes an https URL. It serves img and smiley images.
func cleanImageTarget(raw string) (string, bool) {
	v, ok := cleanURL(raw)
	if !ok || !CheckImageURL(raw) {
		return "", false
	}
	if len(v) >= len("http://") && strings.EqualFold(v[:len("http://")], "http://") {
		v = "https://" + v[len("http://"):]
	}
	return v, true
}

// link renders a url element.
func (r *renderer) link(it renderItem) {
	n := it.n
	href, ok := cleanLinkTarget(n.Attrs["href"])
	if !ok {
		r.children(it)
		return
	}
	// A mailto: target takes the same path as an email element, so the address
	// is percent-encoded: a raw "," would add a recipient.
	if len(href) >= len("mailto:") && strings.EqualFold(href[:len("mailto:")], "mailto:") {
		href = mailtoHref(href[len("mailto:"):])
	}
	open := `<a href="` + html.EscapeString(href) + `" rel="nofollow ugc noopener">`
	if display := n.Attrs["display"]; display != "" {
		r.html.WriteString(open + html.EscapeString(display) + "</a>")
		r.addText(display, it.quote)
		return
	}
	r.wrap(it, open, "</a>", false, false, false, it.quote)
}

// mailtoHref builds a mailto: URL for an address. It percent-encodes the
// address, so a "?" or "#" cannot add a header or a fragment, and it encodes
// "&" too. The caller escapes the result for HTML.
func mailtoHref(address string) string {
	return "mailto:" + strings.ReplaceAll(url.PathEscape(address), "&", "%26")
}

// email renders an email element.
func (r *renderer) email(it renderItem) {
	n := it.n
	// Check the raw address, as Validate does. Do not decode entities in it.
	address := n.Attrs["address"]
	if !isEmail(address) {
		r.children(it)
		return
	}
	open := `<a href="` + html.EscapeString(mailtoHref(address)) + `">`
	if len(n.Children) == 0 {
		r.html.WriteString(open + html.EscapeString(address) + "</a>")
		r.addText(address, it.quote)
		return
	}
	r.wrap(it, open, "</a>", false, false, false, it.quote)
}

// image renders an img element.
func (r *renderer) image(it renderItem) {
	attrs := it.n.Attrs
	alt := attrs["alt"]
	src, ok := cleanImageTarget(attrs["src"])
	if !ok {
		r.html.WriteString(html.EscapeString(alt))
		r.addText(alt, it.quote)
		return
	}
	r.html.WriteString(`<img src="` + html.EscapeString(src) + `" alt="` + html.EscapeString(alt) +
		`" loading="lazy" class="post-img">`)
	r.addText(alt, it.quote)
}

// smiley renders a smiley element, or its code as text.
func (r *renderer) smiley(it renderItem) {
	code := it.n.Attrs["code"]
	r.addText(code, it.quote)
	if r.rc.SmileyURL != nil {
		if raw, ok := r.rc.SmileyURL(code); ok {
			if src, ok := cleanImageTarget(raw); ok {
				r.html.WriteString(`<img class="smiley" src="` + html.EscapeString(src) +
					`" alt="` + html.EscapeString(code) + `">`)
				return
			}
		}
	}
	r.html.WriteString(html.EscapeString(code))
}

// embed renders an embed element, or nothing when its ID fails [EmbedID].
func (r *renderer) embed(it renderItem) {
	n := it.n
	id, ok := EmbedID(n.Name, n.Attrs["id"])
	if !ok {
		return
	}
	out, frame, scripts := embedHTML(n.Name, id)
	if out == "" {
		return
	}
	r.html.WriteString(out)
	if r.frames == nil {
		r.frames = map[string]struct{}{}
	}
	r.frames[frame] = struct{}{}
	for _, script := range scripts {
		if r.scripts == nil {
			r.scripts = map[string]struct{}{}
		}
		r.scripts[script] = struct{}{}
	}
}

// pathEsc escapes each segment of a "/"-separated ID for a URL path, then for
// HTML. An ID that [EmbedID] accepted holds only safe characters, so this is a
// second guard.
func pathEsc(id string) string {
	segs := strings.Split(id, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return html.EscapeString(strings.Join(segs, "/"))
}

// Origins of the providers. Each embed in [embedHTML] names the page of the
// provider's published embed documentation that its entry follows.
const (
	originYouTube    = "https://www.youtube-nocookie.com"
	originVimeo      = "https://player.vimeo.com"
	originXFrame     = "https://platform.twitter.com"
	originXHost      = "https://platform.x.com"
	originInstagram  = "https://www.instagram.com"
	originReddit     = "https://embed.reddit.com"
	originTikTok     = "https://www.tiktok.com"
	originThreads    = "https://www.threads.com"
	originSpotify    = "https://open.spotify.com"
	originSoundCloud = "https://w.soundcloud.com"
)

// embedHTML returns the HTML of an embed for a canonical ID, the origin it
// loads in a frame, and the origins of the scripts it needs (nil for none). It
// returns empty strings when the ID has the wrong shape for the tag.
//
// Every iframe has loading="lazy" and a title. None has a sandbox attribute:
// the providers' players need both scripts and their own origin, and the
// pair allow-scripts with allow-same-origin removes the sandbox.
func embedHTML(tag, id string) (out, frame string, scripts []string) {
	switch tag {
	case "youtube":
		// https://support.google.com/youtube/answer/171780 (privacy-enhanced mode
		// uses youtube-nocookie.com). The allow and referrerpolicy values follow the
		// markup that https://www.youtube.com/oembed returns.
		return `<iframe class="embed youtube" src="` + originYouTube + `/embed/` + pathEsc(id) +
			`" title="YouTube video" loading="lazy" allow="encrypted-media; picture-in-picture; fullscreen"` +
			` allowfullscreen referrerpolicy="strict-origin-when-cross-origin"></iframe>`, originYouTube, nil
	case "vimeo":
		// https://help.vimeo.com/hc/en-us/articles/12426260232977-About-Player-parameters
		// shows src="https://player.vimeo.com/video/{id}" with allowfullscreen.
		return `<iframe class="embed vimeo" src="` + originVimeo + `/video/` + pathEsc(id) +
				`" title="Vimeo video" loading="lazy" allow="fullscreen; picture-in-picture" allowfullscreen></iframe>`,
			originVimeo, nil
	case "tweet":
		// https://docs.x.com/x-for-websites/embedded-posts/overview, and the markup
		// that https://publish.x.com/oembed returns: a blockquote.twitter-tweet with a
		// link to x.com, and a script from platform.x.com/widgets.js. That script
		// builds its iframes on platform.twitter.com.
		//
		// ScriptSrc lists both hosts. platform.x.com/widgets.js answers with a 302 to
		// platform.twitter.com/widgets.js, and that script loads its chunks from
		// https://platform.twitter.com/js/. A CSP checks each redirect, so a policy
		// with only platform.x.com blocks the script and the tweet never renders.
		return `<blockquote class="twitter-tweet"><a href="https://x.com/i/status/` + pathEsc(id) +
			`">View post on X</a></blockquote>`, originXFrame, []string{originXFrame, originXHost}
	case "instagram":
		// https://developers.facebook.com/docs/instagram/embedding : a
		// blockquote.instagram-media that the script at www.instagram.com/embed.js
		// turns into a frame.
		link := "https://www.instagram.com/p/" + pathEsc(id) + "/"
		return `<blockquote class="instagram-media" data-instgrm-permalink="` + link + `"><a href="` + link +
			`">View on Instagram</a></blockquote>`, originInstagram, []string{originInstagram}
	case "reddit":
		// The widget script is https://embed.reddit.com/widgets.js. It selects
		// blockquote.reddit-embed-bq and loads its frames from embed.reddit.com.
		sub, post, ok := strings.Cut(id, "/")
		if !ok {
			return "", "", nil
		}
		return `<blockquote class="reddit-embed-bq"><a href="https://www.reddit.com/r/` + pathEsc(sub) +
			`/comments/` + pathEsc(post) + `/">View post on Reddit</a></blockquote>`, originReddit, []string{originReddit}
	case "tiktok":
		// https://developers.tiktok.com/doc/embed-player : the player is
		// www.tiktok.com/player/v1/{post_id}.
		return `<iframe class="embed tiktok" src="` + originTikTok + `/player/v1/` + pathEsc(id) +
			`" title="TikTok video" loading="lazy" allow="fullscreen" allowfullscreen></iframe>`, originTikTok, nil
	case "threads":
		// https://developers.facebook.com/docs/threads/tools-and-resources/embed-a-threads-post :
		// a blockquote.text-post-media with data-text-post-permalink, and a
		// script (embed.js) from www.threads.com. The docs show a trailing "/" on the
		// permalink.
		user, post, ok := strings.Cut(id, "/")
		if !ok {
			return "", "", nil
		}
		link := "https://www.threads.com/@" + pathEsc(user) + "/post/" + pathEsc(post) + "/"
		return `<blockquote class="text-post-media" data-text-post-permalink="` + link + `"><a href="` + link +
			`">View on Threads</a></blockquote>`, originThreads, []string{originThreads}
	case "spotify":
		// https://developer.spotify.com/documentation/embeds : the iframe src is
		// open.spotify.com/embed/{type}/{id}.
		kind, item, ok := strings.Cut(id, "/")
		if !ok {
			return "", "", nil
		}
		return `<iframe class="embed spotify" src="` + originSpotify + `/embed/` + pathEsc(kind) + `/` + pathEsc(item) +
				`" title="Spotify" loading="lazy" allow="encrypted-media; fullscreen; picture-in-picture"></iframe>`,
			originSpotify, nil
	case "soundcloud":
		// https://developers.soundcloud.com/docs/api/html5-widget : an iframe whose
		// src is w.soundcloud.com/player/?url={escaped resource URL}.
		return `<iframe class="embed soundcloud" src="` + originSoundCloud + `/player/?url=https%3A//soundcloud.com/` +
			pathEsc(id) + `" title="SoundCloud" loading="lazy"></iframe>`, originSoundCloud, nil
	}
	return "", "", nil
}

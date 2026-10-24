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
	// Smiley looks up a smiley by its code. It returns false for an unknown
	// code. A nil Smiley knows no code. The renderer uses Image as the image
	// URL: it checks the URL with [CheckImageURL] and upgrades http to https. It
	// uses Alt as the alt text, and the code when Alt is empty. When the lookup
	// fails, or Image fails the check, the renderer writes the code as text.
	Smiley func(code string) (Smiley, bool)
	// BasePath is the URL path prefix of the site, such as "/forum/". When it is
	// set, the renderer puts it, minus its last "/", in front of every site path
	// it writes: the href of a url element, the src of an img and the image of a
	// smiley. A target with a scheme, such as http, https or mailto, does not
	// change. Rendered.Text does not change either.
	BasePath string
	// ImageNote, when it is set, is asked about the src of each img whose src is
	// a site path. When it returns a non-empty note, the renderer writes
	// <span class="img-note">Image: ALT (NOTE)</span> in place of the image,
	// with "image" for an empty alt. A site uses it for an image that the viewer
	// may not load, such as an attachment that needs a right the viewer lacks.
	// Rendered.Text does not change.
	ImageNote func(src string) string
}

// Rendered is the result of [Render].
type Rendered struct {
	// HTML is the post as HTML. [Render] builds it by hand and escapes every text
	// and attribute value, so it is safe to put in a page as it is.
	HTML template.HTML
	// Text is the plain text of the post: everything except the contents of quote
	// elements and the embeds. Block elements and br elements end a line. A link
	// that has a display text adds its full target after the display text. Each
	// angle bracket is a space: the text search of PostgreSQL would read
	// "<like this>" as a tag and drop it. It feeds the search text of a post.
	Text string
	// FrameSrc lists the origins that the embeds of this tree load in frames.
	// It is sorted and has no duplicates.
	FrameSrc []string
	// ScriptSrc lists the origins of the scripts that the embeds of this tree
	// need. It is sorted and has no duplicates.
	ScriptSrc []string
	// ConnectSrc lists the origins that the provider scripts of this tree reach
	// with fetch or XMLHttpRequest. It is sorted and has no duplicates.
	ConnectSrc []string
	// Scripts lists the full URLs of the provider scripts that the embeds of
	// this tree need. A page loads each one once, with <script async>. It is
	// sorted and has no duplicates.
	Scripts []string
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
	scripts map[string]struct{} // script origins
	connect map[string]struct{}
	urls    map[string]struct{} // script URLs
	seen    map[*Node]struct{}  // the nodes that Render has already visited
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
		HTML:       template.HTML(r.html.String()),
		Text:       strings.Trim(r.text.String(), "\n"),
		FrameSrc:   sortedKeys(r.frames),
		ScriptSrc:  sortedKeys(r.scripts),
		ConnectSrc: sortedKeys(r.connect),
		Scripts:    sortedKeys(r.urls),
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

// searchSafe turns each angle bracket in s into a space. The text search of
// PostgreSQL reads "<like this>" as a markup tag and drops what it holds, so a
// word that stood only inside angle brackets would never be found.
func searchSafe(s string) string {
	if !strings.ContainsAny(s, "<>") {
		return s
	}
	return strings.Map(func(c rune) rune {
		if c == '<' || c == '>' {
			return ' '
		}
		return c
	}, s)
}

// addText adds s to the plain text, unless the node is inside a quote. The plain
// text has no angle bracket: see [searchSafe].
func (r *renderer) addText(s string, quote bool) {
	if quote || s == "" {
		return
	}
	s = searchSafe(s)
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
			r.wrap(it, openTag("p"), closeTag("p"), true, false, false, q)
		}
	case "br":
		r.html.WriteString(openTag("br"))
		r.addText("\n", q)
	case "h":
		tag := headingTag(n.Attrs["level"])
		r.wrap(it, openTag(tag), closeTag(tag), true, false, false, q)
	case "hr":
		r.html.WriteString(openTag("hr"))
	case "quote":
		r.wrap(it, openTag("blockquote"), closeTag("blockquote"), true, false, false, true)
	case "list":
		r.list(it)
	case "li":
		r.wrap(it, openTag("li"), closeTag("li"), true, false, it.tight, q)
	case "pre":
		code := openTag("code")
		if lang := n.Attrs["lang"]; isLang(lang) {
			code = openTag("code", "class", "language-"+lang)
		}
		r.wrap(it, openTag("pre", "class", "code")+code, closeTag("code")+closeTag("pre"), true, false, false, q)
	case "code":
		r.wrap(it, openTag("code"), closeTag("code"), false, false, false, q)
	case "b":
		r.wrap(it, openTag("strong"), closeTag("strong"), false, false, false, q)
	case "i":
		r.wrap(it, openTag("em"), closeTag("em"), false, false, false, q)
	case "url":
		r.link(it)
	case "email":
		r.email(it)
	case "img":
		r.image(it)
	case "spoiler":
		r.wrap(it, openTag("span", "class", "spoiler", "tabindex", "0"), closeTag("span"), false, false, false, q)
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
	open := openTag("ul")
	if attrs["ordered"] == "true" {
		tag = "ol"
		open = openTag("ol")
		if s := attrs["start"]; isListStart(s) {
			open = openTag("ol", "start", s)
		}
	}
	r.wrap(it, open, closeTag(tag), true, attrs["tight"] == "true", false, it.quote)
}

// sitePath puts the base path of the site in front of v when v is a site path.
// Any other target, such as an https URL or a mailto: address, stays as it is.
func (r *renderer) sitePath(v string) string {
	prefix := basePrefix(r.rc.BasePath)
	if prefix == "" || !isSitePath(v) {
		return v
	}
	return prefix + v
}

// basePrefix turns a base path such as "forum/" or "/forum/" into "/forum". The
// result starts with one "/" and has no "/" at the end, so a site path that
// follows it is rooted, and never a relative or a scheme-relative URL. A value
// that is not a plain path (it holds a colon, a backslash or a space) gives "".
func basePrefix(base string) string {
	base = strings.Trim(base, "/")
	if base == "" || strings.ContainsAny(base, ":\\ \t\r\n") {
		return ""
	}
	return "/" + strings.Trim(strings.Join(strings.FieldsFunc(base, func(r rune) bool { return r == '/' }), "/"), "/")
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
	// textHref is the target as written, for the search text.
	textHref := href
	// A mailto: target takes the same path as an email element, so the address
	// is percent-encoded: a raw "," would add a recipient.
	if len(href) >= len("mailto:") && strings.EqualFold(href[:len("mailto:")], "mailto:") {
		href = mailtoHref(href[len("mailto:"):])
	}
	open := openTag("a", "href", r.sitePath(href), "rel", "nofollow ugc noopener")
	if display := n.Attrs["display"]; display != "" {
		r.html.WriteString(open + html.EscapeString(display) + closeTag("a"))
		// The display text is a shortened form of the link. The search text also
		// carries the full target, so a search finds the middle of a long URL.
		r.addText(display, it.quote)
		r.addText(" "+textHref, it.quote)
		return
	}
	r.wrap(it, open, closeTag("a"), false, false, false, it.quote)
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
	open := openTag("a", "href", mailtoHref(address))
	if len(n.Children) == 0 {
		r.html.WriteString(open + html.EscapeString(address) + closeTag("a"))
		r.addText(address, it.quote)
		return
	}
	r.wrap(it, open, closeTag("a"), false, false, false, it.quote)
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
	if r.rc.ImageNote != nil && isSitePath(src) {
		if note := r.rc.ImageNote(src); note != "" {
			label := alt
			if label == "" {
				label = "image"
			}
			r.html.WriteString(openTag("span", "class", "img-note") + "Image: " + html.EscapeString(label) +
				" (" + html.EscapeString(note) + ")" + closeTag("span"))
			r.addText(alt, it.quote)
			return
		}
	}
	r.html.WriteString(openTag("img", "src", r.sitePath(src), "alt", alt, "loading", "lazy", "class", "post-img"))
	r.addText(alt, it.quote)
}

// smiley renders a smiley element, or its code as text.
func (r *renderer) smiley(it renderItem) {
	code := it.n.Attrs["code"]
	r.addText(code, it.quote)
	if r.rc.Smiley != nil {
		if sm, ok := r.rc.Smiley(code); ok {
			if src, ok := cleanImageTarget(sm.Image); ok {
				alt := sm.Alt
				if alt == "" {
					alt = code
				}
				r.html.WriteString(openTag("img", "class", "smiley", "src", r.sitePath(src), "alt", alt))
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
	e := embedHTML(n.Name, id)
	if e.html == "" {
		return
	}
	r.html.WriteString(e.html)
	if e.frame != "" {
		r.frames = addKey(r.frames, e.frame)
	}
	for _, o := range e.scriptSrc {
		r.scripts = addKey(r.scripts, o)
	}
	for _, o := range e.connect {
		r.connect = addKey(r.connect, o)
	}
	for _, u := range e.scripts {
		r.urls = addKey(r.urls, u)
	}
}

// addKey adds k to the set m and returns it. A nil m becomes a new set.
func addKey(m map[string]struct{}, k string) map[string]struct{} {
	if m == nil {
		m = map[string]struct{}{}
	}
	m[k] = struct{}{}
	return m
}

// pathEsc escapes each segment of a "/"-separated ID for a URL path. An ID that
// [EmbedID] accepted holds only safe characters, so this is a second guard.
// [openTag] escapes the result for HTML when it writes the attribute.
func pathEsc(id string) string {
	segs := strings.Split(id, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Origins of the providers. Each embed in [embedHTML] names the page of the
// provider's published embed documentation that its entry follows.
const (
	originYouTube = "https://www.youtube-nocookie.com"
	originVimeo   = "https://player.vimeo.com"
	originXFrame  = "https://platform.twitter.com"
	originXHost   = "https://platform.x.com"
	// The hosts that X's widget script reaches with fetch or XHR.
	originXSyndication    = "https://syndication.twitter.com"
	originXCDNSyndication = "https://cdn.syndication.twimg.com"
	originInstagram       = "https://www.instagram.com"
	originTikTok          = "https://www.tiktok.com"
	originThreads         = "https://www.threads.com"
	originSpotify         = "https://open.spotify.com"
	originSoundCloud      = "https://w.soundcloud.com"
)

// embedOut is one row of the embed table: the HTML of an embed and the origins
// that the page needs for it in its Content-Security-Policy.
type embedOut struct {
	html string
	// frame is the origin that the embed loads in a frame. It is empty for an
	// embed that is a plain link.
	frame string
	// scripts are the full URLs of the provider scripts that the page loads.
	scripts []string
	// scriptSrc are the origins that script-src needs for those scripts. A script
	// that answers with a redirect needs the origin of the target too.
	scriptSrc []string
	// connect are the origins that the provider scripts reach with fetch or
	// XMLHttpRequest.
	connect []string
}

// embedHTML returns the embed table row for a canonical ID. It returns an empty
// row when the ID has the wrong shape for the tag.
//
// Every iframe has loading="lazy" and a title. None has a sandbox attribute:
// the providers' players need both scripts and their own origin, and the
// pair allow-scripts with allow-same-origin removes the sandbox.
func embedHTML(tag, id string) embedOut {
	switch tag {
	case "youtube":
		// https://support.google.com/youtube/answer/171780 (privacy-enhanced mode
		// uses youtube-nocookie.com). The allow and referrerpolicy values follow the
		// markup that https://www.youtube.com/oembed returns.
		return embedOut{html: openTag("iframe", "class", "embed youtube", "src", originYouTube+"/embed/"+pathEsc(id),
			"title", "YouTube video", "loading", "lazy", "allow", "encrypted-media; picture-in-picture; fullscreen",
			"allowfullscreen", bare, "referrerpolicy", "strict-origin-when-cross-origin") + closeTag("iframe"),
			frame: originYouTube}
	case "vimeo":
		// https://help.vimeo.com/hc/en-us/articles/12426260232977-About-Player-parameters
		// shows src="https://player.vimeo.com/video/{id}" with allowfullscreen.
		return embedOut{html: openTag("iframe", "class", "embed vimeo", "src", originVimeo+"/video/"+pathEsc(id),
			"title", "Vimeo video", "loading", "lazy", "allow", "fullscreen; picture-in-picture",
			"allowfullscreen", bare) + closeTag("iframe"),
			frame: originVimeo}
	case "tweet":
		// https://docs.x.com/x-for-websites/embedded-posts/overview, and the markup
		// that https://publish.x.com/oembed returns: a blockquote.twitter-tweet with a
		// link to x.com, and the script at platform.x.com/widgets.js. The FAQ at
		// https://docs.x.com/x-for-websites/embedded-posts/faq gives that script URL.
		// The pages list no frame, connect or image host and give no CSP advice, so
		// the lists below come from what the script does in a browser (see the report
		// of the Task 9 check).
		//
		// scriptSrc lists both hosts. platform.x.com/widgets.js answers with a 302 to
		// platform.twitter.com/widgets.js, and that script loads its chunks from
		// https://platform.twitter.com/js/. A CSP checks each redirect, so a policy
		// with only platform.x.com blocks the script and the tweet never renders.
		//
		// connect lists the hosts that the script reaches with fetch or XHR: the
		// syndication hosts. The frame the script builds loads from
		// platform.twitter.com.
		return embedOut{
			html: openTag("blockquote", "class", "twitter-tweet") +
				openTag("a", "href", "https://x.com/i/status/"+pathEsc(id)) + "View post on X" + closeTag("a") +
				closeTag("blockquote"),
			frame:     originXFrame,
			scripts:   []string{originXHost + "/widgets.js"},
			scriptSrc: []string{originXFrame, originXHost},
			connect:   []string{originXSyndication, originXCDNSyndication},
		}
	case "instagram":
		// https://developers.facebook.com/docs/instagram-platform/oembed : a
		// blockquote.instagram-media that the script at www.instagram.com/embed.js
		// turns into a frame. The page names www.instagram.com only and gives no
		// CSP advice.
		link := "https://www.instagram.com/p/" + pathEsc(id) + "/"
		return embedOut{
			html: openTag("blockquote", "class", "instagram-media", "data-instgrm-permalink", link) +
				openTag("a", "href", link) + "View on Instagram" + closeTag("a") + closeTag("blockquote"),
			frame:     originInstagram,
			scripts:   []string{originInstagram + "/embed.js"},
			scriptSrc: []string{originInstagram},
		}
	case "reddit":
		// A plain link, with no script and no frame. Reddit publishes no embed page
		// that this project can read (https://www.reddit.com/wiki/embeds answers 403
		// to a script fetch), and Reddit blocks some clients in its embed frame. A
		// link needs no origin in the Content-Security-Policy.
		sub, post, ok := strings.Cut(id, "/")
		if !ok {
			return embedOut{}
		}
		return embedOut{html: openTag("div", "class", "embed-link") +
			openTag("a", "href", "https://www.reddit.com/r/"+pathEsc(sub)+"/comments/"+pathEsc(post)+"/",
				"rel", "nofollow ugc noopener") + "View post on Reddit" + closeTag("a") + closeTag("div")}
	case "tiktok":
		// https://developers.tiktok.com/doc/embed-player : the player is
		// www.tiktok.com/player/v1/{post_id}.
		return embedOut{html: openTag("iframe", "class", "embed tiktok", "src", originTikTok+"/player/v1/"+pathEsc(id),
			"title", "TikTok video", "loading", "lazy", "allow", "fullscreen", "allowfullscreen", bare) + closeTag("iframe"),
			frame: originTikTok}
	case "threads":
		// https://developers.facebook.com/docs/threads/tools-and-resources/embed-a-threads-post :
		// a blockquote.text-post-media with data-text-post-permalink, and
		// <script async src="https://www.threads.com/embed.js">. The page names
		// www.threads.com for both and gives no CSP advice. The docs show a trailing
		// "/" on the permalink.
		user, post, ok := strings.Cut(id, "/")
		if !ok {
			return embedOut{}
		}
		link := "https://www.threads.com/@" + pathEsc(user) + "/post/" + pathEsc(post) + "/"
		return embedOut{
			html: openTag("blockquote", "class", "text-post-media", "data-text-post-permalink", link) +
				openTag("a", "href", link) + "View on Threads" + closeTag("a") + closeTag("blockquote"),
			frame:     originThreads,
			scripts:   []string{originThreads + "/embed.js"},
			scriptSrc: []string{originThreads},
		}
	case "spotify":
		// https://developer.spotify.com/documentation/embeds : the iframe src is
		// open.spotify.com/embed/{type}/{id}.
		kind, item, ok := strings.Cut(id, "/")
		if !ok {
			return embedOut{}
		}
		return embedOut{html: openTag("iframe", "class", "embed spotify",
			"src", originSpotify+"/embed/"+pathEsc(kind)+"/"+pathEsc(item), "title", "Spotify", "loading", "lazy",
			"allow", "encrypted-media; fullscreen; picture-in-picture") + closeTag("iframe"),
			frame: originSpotify}
	case "soundcloud":
		// https://developers.soundcloud.com/docs/api/html5-widget : an iframe whose
		// src is w.soundcloud.com/player/?url={escaped resource URL}.
		return embedOut{html: openTag("iframe", "class", "embed soundcloud",
			"src", originSoundCloud+"/player/?url=https%3A//soundcloud.com/"+pathEsc(id), "title", "SoundCloud",
			"loading", "lazy") + closeTag("iframe"), frame: originSoundCloud}
	}
	return embedOut{}
}

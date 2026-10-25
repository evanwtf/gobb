package markup

import (
	"html"
	"html/template"
	"net/url"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// renderSmileys is the smiley table that the render tests use.
func renderSmileys() RenderContext {
	return RenderContext{Smiley: func(code string) (Smiley, bool) {
		if code == ":)" {
			return Smiley{Code: code, Image: "/static/smilies/smile.gif", Alt: "smile"}, true
		}
		return Smiley{}, false
	}}
}

// renderHTML renders n with the test smiley table and returns the HTML.
func renderHTML(n *Node) string { return string(Render(n, renderSmileys()).HTML) }

// renderLi builds an li that holds one paragraph of text.
func renderLi(s string) *Node { return el("li", nil, para(txt(s))) }

// renderEmbed builds the one-element tree for an embed.
func renderEmbed(tag, id string) *Node { return tree(el(tag, at("id", id))) }

func TestRenderTable(t *testing.T) {
	cases := []struct {
		name string
		in   *Node
		want string
	}{
		{"p", tree(para(txt("hi"))), `<p>hi</p>`},
		{"br", tree(para(txt("a"), el("br", nil), txt("b"))), `<p>a<br>b</p>`},
		{"h1", tree(el("h", at("level", "1"), txt("t"))), `<h3>t</h3>`},
		{"h2", tree(el("h", at("level", "2"), txt("t"))), `<h4>t</h4>`},
		{"h3", tree(el("h", at("level", "3"), txt("t"))), `<h5>t</h5>`},
		{"h4", tree(el("h", at("level", "4"), txt("t"))), `<h6>t</h6>`},
		{"h5", tree(el("h", at("level", "5"), txt("t"))), `<h6>t</h6>`},
		{"h6", tree(el("h", at("level", "6"), txt("t"))), `<h6>t</h6>`},
		{"h bad level", tree(el("h", at("level", "9"), txt("t"))), `<h6>t</h6>`},
		{"hr", tree(el("hr", nil)), `<hr>`},
		{"quote", tree(el("quote", nil, para(txt("q")))), `<blockquote><p>q</p></blockquote>`},
		{"ul", tree(el("list", nil, renderLi("a"), renderLi("b"))),
			`<ul><li><p>a</p></li><li><p>b</p></li></ul>`},
		{"ol", tree(el("list", at("ordered", "true"), renderLi("a"))), `<ol><li><p>a</p></li></ol>`},
		{"ol start", tree(el("list", at("ordered", "true", "start", "3"), renderLi("a"))),
			`<ol start="3"><li><p>a</p></li></ol>`},
		{"ul ignores start", tree(el("list", at("start", "3"), renderLi("a"))), `<ul><li><p>a</p></li></ul>`},
		{"ol bad start", tree(el("list", at("ordered", "true", "start", `3" x="`), renderLi("a"))),
			`<ol><li><p>a</p></li></ol>`},
		{"pre", tree(el("pre", nil, txt("a<b\nc"))), "<pre class=\"code\"><code>a&lt;b\nc</code></pre>"},
		{"pre lang", tree(el("pre", at("lang", "c++"), txt("x"))),
			`<pre class="code"><code class="language-c++">x</code></pre>`},
		{"pre bad lang", tree(el("pre", at("lang", `x" onclick="y`), txt("x"))),
			`<pre class="code"><code>x</code></pre>`},
		{"code", tree(para(el("code", nil, txt("x := 1")))), `<p><code>x := 1</code></p>`},
		{"b i", tree(para(el("b", nil, txt("a")), el("i", nil, txt("b")))),
			`<p><strong>a</strong><em>b</em></p>`},
		{"url", tree(para(el("url", at("href", "https://example.com/a?b=c"), txt("link")))),
			`<p><a href="https://example.com/a?b=c" rel="nofollow ugc noopener">link</a></p>`},
		{"url display", tree(para(el("url", at("href", "https://example.com/", "display", "short"), txt("long")))),
			`<p><a href="https://example.com/" rel="nofollow ugc noopener">short</a></p>`},
		{"url site path", tree(para(el("url", at("href", "/t/5"), txt("t")))),
			`<p><a href="/t/5" rel="nofollow ugc noopener">t</a></p>`},
		{"url keeps http", tree(para(el("url", at("href", "http://example.com/"), txt("t")))),
			`<p><a href="http://example.com/" rel="nofollow ugc noopener">t</a></p>`},
		{"url mailto", tree(para(el("url", at("href", "mailto:a@example.com"), txt("t")))),
			`<p><a href="mailto:a@example.com" rel="nofollow ugc noopener">t</a></p>`},
		{"email", tree(para(el("email", at("address", "a@example.com"), txt("mail")))),
			`<p><a href="mailto:a@example.com">mail</a></p>`},
		{"email no children", tree(para(el("email", at("address", "a@example.com")))),
			`<p><a href="mailto:a@example.com">a@example.com</a></p>`},
		{"img", tree(para(el("img", at("src", "https://example.com/a.png", "alt", "cat")))),
			`<p><img src="https://example.com/a.png" alt="cat" loading="lazy" class="post-img"></p>`},
		{"img no alt", tree(para(el("img", at("src", "/f/a.png")))),
			`<p><img src="/f/a.png" alt="" loading="lazy" class="post-img"></p>`},
		{"spoiler", tree(para(el("spoiler", nil, txt("s")))), `<p><span class="spoiler" tabindex="0">s</span></p>`},
		{"smiley", tree(para(el("smiley", at("code", ":)")))),
			`<p><img class="smiley" src="/static/smilies/smile.gif" alt="smile"></p>`},
		{"smiley unknown", tree(para(el("smiley", at("code", ":x:")))), `<p>:x:</p>`},
		{"unknown element", tree(para(el("marquee", at("onclick", "x()"), txt("hi"), el("b", nil, txt("!"))))),
			`<p>hi<strong>!</strong></p>`},
		{"unknown block", tree(el("section", at("class", "x"), para(txt("a")))), `<p>a</p>`},
		{"text at root", tree(txt("loose")), `loose`},
		{"empty root", tree(), ``},

		{"youtube", renderEmbed("youtube", "M7lc1UVf-VE"),
			`<iframe class="embed youtube" src="https://www.youtube-nocookie.com/embed/M7lc1UVf-VE" title="YouTube video" loading="lazy" allow="encrypted-media; picture-in-picture; fullscreen" allowfullscreen referrerpolicy="strict-origin-when-cross-origin"></iframe>`},
		{"vimeo", renderEmbed("vimeo", "76979871"),
			`<iframe class="embed vimeo" src="https://player.vimeo.com/video/76979871" title="Vimeo video" loading="lazy" allow="fullscreen; picture-in-picture" allowfullscreen></iframe>`},
		{"tweet", renderEmbed("tweet", "20"),
			`<div class="embed-link"><a href="https://x.com/i/status/20" rel="nofollow ugc noopener">https://x.com/i/status/20</a></div>`},
		{"instagram", renderEmbed("instagram", "CxYz_12-ab"),
			`<div class="embed-link"><a href="https://www.instagram.com/p/CxYz_12-ab/" rel="nofollow ugc noopener">https://www.instagram.com/p/CxYz_12-ab/</a></div>`},
		{"reddit", renderEmbed("reddit", "programming/abc123"),
			`<div class="embed-link"><a href="https://www.reddit.com/r/programming/comments/abc123/" rel="nofollow ugc noopener">View post on Reddit</a></div>`},
		{"tiktok", renderEmbed("tiktok", "6718335390845095173"),
			`<iframe class="embed tiktok" src="https://www.tiktok.com/player/v1/6718335390845095173" title="TikTok video" loading="lazy" allow="fullscreen" allowfullscreen></iframe>`},
		{"threads", renderEmbed("threads", "zuck/C04flh-M8ii"),
			`<div class="embed-link"><a href="https://www.threads.com/@zuck/post/C04flh-M8ii/" rel="nofollow ugc noopener">https://www.threads.com/@zuck/post/C04flh-M8ii/</a></div>`},
		{"spotify", renderEmbed("spotify", "track/4cOdK2wGLETKBW3PvgPWqT"),
			`<iframe class="embed spotify" src="https://open.spotify.com/embed/track/4cOdK2wGLETKBW3PvgPWqT" title="Spotify" loading="lazy" allow="encrypted-media; fullscreen; picture-in-picture"></iframe>`},
		{"soundcloud track", renderEmbed("soundcloud", "forss/flickermood"),
			`<iframe class="embed soundcloud" src="https://w.soundcloud.com/player/?url=https%3A//soundcloud.com/forss/flickermood" title="SoundCloud" loading="lazy"></iframe>`},
		{"soundcloud set", renderEmbed("soundcloud", "forss/sets/soulhack"),
			`<iframe class="embed soundcloud" src="https://w.soundcloud.com/player/?url=https%3A//soundcloud.com/forss/sets/soulhack" title="SoundCloud" loading="lazy"></iframe>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderHTML(tc.in); got != tc.want {
				t.Errorf("HTML mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestRenderEmbedsAlwaysLazyAndTitled(t *testing.T) {
	ids := map[string]string{
		"youtube": "M7lc1UVf-VE", "vimeo": "76979871", "tiktok": "6718335390845095173",
		"spotify": "track/4cOdK2wGLETKBW3PvgPWqT", "soundcloud": "forss/flickermood",
	}
	for tag, id := range ids {
		out := renderHTML(renderEmbed(tag, id))
		if !strings.Contains(out, ` loading="lazy"`) || !strings.Contains(out, ` title="`) {
			t.Errorf("%s: iframe lacks loading or title: %s", tag, out)
		}
		if strings.Contains(out, "sandbox") {
			t.Errorf("%s: sandbox attribute present: %s", tag, out)
		}
	}
}

func TestRenderEscapes(t *testing.T) {
	got := renderHTML(tree(para(txt(`<script>&"`))))
	if want := `<p>&lt;script&gt;&amp;&#34;</p>`; got != want {
		t.Errorf("text: got %s, want %s", got, want)
	}
	hostile := `"><script>alert(1)</script>`
	esc := `&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`
	checks := map[string]*Node{
		"img alt":     tree(para(el("img", at("src", "/a.png", "alt", hostile)))),
		"url display": tree(para(el("url", at("href", "/a", "display", hostile)))),
		"smiley code": tree(para(el("smiley", at("code", hostile)))),
		"pre text":    tree(el("pre", nil, txt(hostile))),
		"img bad src": tree(para(el("img", at("src", "javascript:x", "alt", hostile)))),
	}
	for name, n := range checks {
		out := renderHTML(n)
		if strings.Contains(out, "<script") || !strings.Contains(out, esc) {
			t.Errorf("%s: output not escaped: %s", name, out)
		}
	}
}

// TestRenderEmitsLiteralURL pins the literal-URL contract: the attribute holds
// the stored value, trimmed, and nothing decodes entities in it. The renderer
// escapes the value once on output.
func TestRenderEmitsLiteralURL(t *testing.T) {
	cases := []struct{ name, href, want string }{
		{"quote entity stays literal", `https://example.com/?a=&quot;onmouseover=x`,
			`href="https://example.com/?a=&amp;quot;onmouseover=x"`},
		{"amp entity stays literal", `https://example.com/?a=1&amp;b=2`, `href="https://example.com/?a=1&amp;amp;b=2"`},
		{"ampersand is escaped", `https://example.com/?a=1&b=2`, `href="https://example.com/?a=1&amp;b=2"`},
		{"padding trimmed", "  https://example.com/x \n", `href="https://example.com/x"`},
		{"legacy entity names stay literal", `https://example.com/?a=1&not=3&para=2&reg=1&copy=4&times=5&shy=6`,
			`href="https://example.com/?a=1&amp;not=3&amp;para=2&amp;reg=1&amp;copy=4&amp;times=5&amp;shy=6"`},
		{"entity slash is a site path", `/&#47;x`, `href="/&amp;#47;x"`},
	}
	for _, tc := range cases {
		out := renderHTML(tree(para(el("url", at("href", tc.href), txt("l")))))
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: got %s, want it to contain %s", tc.name, out, tc.want)
		}
	}
	for _, href := range []string{"&#32;https://example.com/x", `https&#58;//example.com/x`, `&#47;&#47;evil.example`} {
		if out := renderHTML(tree(para(el("url", at("href", href), txt("l"))))); strings.Contains(out, "<a ") {
			t.Errorf("href %q became a link: %s", href, out)
		}
	}
	out := renderHTML(tree(para(el("img", at("src", "/a.png?x&times=1")))))
	if !strings.Contains(out, `src="/a.png?x&amp;times=1"`) {
		t.Errorf("img src: got %s", out)
	}
	out = renderHTML(tree(para(el("img", at("src", "&#32;http&#58;//example.com/a.png"), el("b", nil)))))
	if strings.Contains(out, "<img") {
		t.Errorf("an entity-encoded img src became an image: %s", out)
	}
}

func TestRenderRejectsDangerousSchemes(t *testing.T) {
	bad := []string{
		"javascript:alert(1)", "JaVaScRiPt:x", "data:text/html,x", "vbscript:x", "&#106;avascript:x",
		"&amp;#106;avascript:x", "java\tscript:x", " javascript:x", "//evil.example/x", "/\\evil.example",
		"file:///etc/passwd", "",
	}
	for _, v := range bad {
		for _, tc := range []struct{ name, attr, fallback string }{
			{"url", "href", "TXT"}, {"img", "src", "ALT"}, {"email", "address", "TXT"},
		} {
			var n *Node
			switch tc.name {
			case "url":
				n = tree(para(el("url", at("href", v), txt("TXT"))))
			case "img":
				n = tree(para(el("img", at("src", v, "alt", "ALT"))))
			case "email":
				n = tree(para(el("email", at("address", v), txt("TXT"))))
			}
			out := renderHTML(n)
			if strings.Contains(out, "href") || strings.Contains(out, "src") || strings.Contains(out, "<a") ||
				strings.Contains(out, "<img") {
				t.Errorf("%s %q: output has a link or image: %s", tc.name, v, out)
			}
			if !strings.Contains(out, tc.fallback) {
				t.Errorf("%s %q: fallback %q missing: %s", tc.name, v, tc.fallback, out)
			}
		}
	}
	// mailto: is a link target but never an image source.
	out := renderHTML(tree(para(el("img", at("src", "mailto:a@b.example", "alt", "ALT")))))
	if strings.Contains(out, "src") {
		t.Errorf("img with mailto source: %s", out)
	}
	// A smiley table that returns a bad image gives the code as text.
	for _, image := range []string{"javascript:x", "mailto:a@b.example", "", "//evil.example/x.gif", "/a b.gif"} {
		rc := RenderContext{Smiley: func(code string) (Smiley, bool) { return Smiley{Code: code, Image: image, Alt: "x"}, true }}
		out := string(Render(tree(para(el("smiley", at("code", ":)")))), rc).HTML)
		if out != "<p>:)</p>" {
			t.Errorf("smiley with image %q: %s, want the code as text", image, out)
		}
	}
	// A nil Smiley func is allowed.
	if out := string(Render(tree(para(el("smiley", at("code", ":)")))), RenderContext{}).HTML); out != "<p>:)</p>" {
		t.Errorf("nil Smiley: %s", out)
	}
}

// TestMailtoHref tests the encoder alone, so it holds whatever the address
// rule accepts.
func TestMailtoHref(t *testing.T) {
	cases := []struct{ addr, want string }{
		{"a@b.example", "mailto:a@b.example"},
		{"a@b.example?cc=x", "mailto:a@b.example%3Fcc=x"},
		{"a@b.example#frag", "mailto:a@b.example%23frag"},
		{"a&b@c.example", "mailto:a%26b@c.example"},
		{"a%41@c.example", "mailto:a%2541@c.example"},
		{"a+tag@c.example", "mailto:a+tag@c.example"},
		{"a@b.example/x", "mailto:a@b.example%2Fx"},
	}
	for _, tc := range cases {
		if got := mailtoHref(tc.addr); got != tc.want {
			t.Errorf("mailtoHref(%q) = %q, want %q", tc.addr, got, tc.want)
		}
		if got := mailtoHref(tc.addr); strings.ContainsAny(got, "?#&") {
			t.Errorf("mailtoHref(%q) = %q holds ?, # or &", tc.addr, got)
		}
	}
}

// TestRenderEmailNoRawDelimiter checks the whole path. Whether the address rule
// accepts these values or not, the output has no raw "?" "#" or "&" in a
// mailto: link, and no link carries a header.
func TestRenderEmailNoRawDelimiter(t *testing.T) {
	for _, addr := range []string{
		"a@b.example?cc=x", "a@b.example#frag", "a&b@c.example", "a%41@c.example", "a@b.example?bcc=x&subject=y",
	} {
		out := renderHTML(tree(para(el("email", at("address", addr), txt("m")))))
		if strings.Contains(out, "?") || strings.Contains(out, "#frag") {
			t.Errorf("%q: raw delimiter in %s", addr, out)
		}
		if i := strings.Index(out, `href="`); i >= 0 {
			href := out[i+6:]
			href = href[:strings.Index(href, `"`)]
			if strings.ContainsAny(href, "?#") || strings.Contains(href, "&") {
				t.Errorf("%q: raw delimiter in href %s", addr, href)
			}
		}
	}
}

func TestRenderHTTPImageUpgraded(t *testing.T) {
	cases := []struct{ src, want string }{
		{"http://img.example/a.png", `src="https://img.example/a.png"`},
		{"HTTP://img.example/a.png", `src="https://img.example/a.png"`},
		{"https://img.example/a.png", `src="https://img.example/a.png"`},
		{"/f/a.png", `src="/f/a.png"`},
		{"/f/http://x.png", `src="/f/http://x.png"`},
	}
	for _, tc := range cases {
		out := renderHTML(tree(para(el("img", at("src", tc.src, "alt", "a")))))
		if !strings.Contains(out, tc.want) {
			t.Errorf("%q: got %s, want it to contain %s", tc.src, out, tc.want)
		}
	}
}

func TestRenderOrigins(t *testing.T) {
	n := tree(
		renderEmbed("youtube", "M7lc1UVf-VE").Children[0],
		renderEmbed("youtube", "dQw4w9WgXcQ").Children[0],
		renderEmbed("tweet", "20").Children[0],
	)
	r := Render(n, RenderContext{})
	// The tweet is a plain link (ruling R49): it adds no frame and no script origin.
	wantFrame := []string{"https://www.youtube-nocookie.com"}
	if !reflect.DeepEqual(r.FrameSrc, wantFrame) {
		t.Errorf("FrameSrc = %v, want %v", r.FrameSrc, wantFrame)
	}
	if len(r.ScriptSrc) != 0 {
		t.Errorf("ScriptSrc = %v, want none", r.ScriptSrc)
	}
}

func TestRenderOriginsEveryEmbed(t *testing.T) {
	ids := []struct{ tag, id string }{
		{"youtube", "M7lc1UVf-VE"}, {"vimeo", "76979871"}, {"tweet", "20"}, {"instagram", "CxYz_12-ab"},
		{"reddit", "programming/abc123"}, {"tiktok", "6718335390845095173"}, {"threads", "zuck/C04flh-M8ii"},
		{"spotify", "track/4cOdK2wGLETKBW3PvgPWqT"}, {"soundcloud", "forss/flickermood"},
	}
	var kids []*Node
	for _, e := range ids {
		kids = append(kids, el(e.tag, at("id", e.id)))
	}
	r := Render(tree(kids...), RenderContext{})
	// X, Instagram and Threads are plain links (ruling R49): they add no origin.
	wantFrame := []string{
		"https://open.spotify.com", "https://player.vimeo.com", "https://w.soundcloud.com",
		"https://www.tiktok.com", "https://www.youtube-nocookie.com",
	}
	if !reflect.DeepEqual(r.FrameSrc, wantFrame) {
		t.Errorf("FrameSrc = %v\nwant %v", r.FrameSrc, wantFrame)
	}
	if len(r.ScriptSrc) != 0 || len(r.ConnectSrc) != 0 {
		t.Errorf("ScriptSrc = %v, ConnectSrc = %v, want none", r.ScriptSrc, r.ConnectSrc)
	}
}

func TestRenderOriginsNoneForNoEmbed(t *testing.T) {
	r := Render(tree(para(txt("hi")), renderEmbed("youtube", "not an id").Children[0]), RenderContext{})
	if len(r.FrameSrc) != 0 || len(r.ScriptSrc) != 0 {
		t.Errorf("origins for a tree with no valid embed: %v %v", r.FrameSrc, r.ScriptSrc)
	}
}

func TestRenderInvalidEmbedRendersNothing(t *testing.T) {
	bad := map[string]string{
		"youtube": `x"><script>`, "vimeo": "abc", "tweet": "javascript:x", "instagram": "a b",
		"reddit": "only-one", "tiktok": "12x", "threads": "zuck", "spotify": "track/short",
		"soundcloud": "forss/sets", "": "x", "unknownembed": "x",
	}
	for tag, id := range bad {
		if tag == "" || tag == "unknownembed" {
			continue
		}
		if out := renderHTML(renderEmbed(tag, id)); out != "" {
			t.Errorf("%s %q: got %s, want nothing", tag, id, out)
		}
	}
	if out := renderHTML(tree(el("youtube", nil))); out != "" {
		t.Errorf("embed with no id: %s", out)
	}
}

func TestRenderEmbedUsesCanonicalID(t *testing.T) {
	out := renderHTML(renderEmbed("youtube", "https://www.youtube.com/watch?v=M7lc1UVf-VE&x=1"))
	if !strings.Contains(out, `src="https://www.youtube-nocookie.com/embed/M7lc1UVf-VE"`) {
		t.Errorf("URL-form ID not reduced to the canonical ID: %s", out)
	}
}

func TestRenderUnknownKind(t *testing.T) {
	if out := renderHTML(tree(&Node{Kind: Kind(99), Children: []*Node{txt("hidden")}}, para(txt("shown")))); out != "<p>shown</p>" {
		t.Errorf("unknown kind: got %s", out)
	}
	if out := renderHTML(&Node{Kind: Kind(7), Children: []*Node{txt("hidden")}}); out != "" {
		t.Errorf("unknown kind at the top: got %s", out)
	}
	if out := renderHTML(tree(nil, para(txt("a")), nil)); out != "<p>a</p>" {
		t.Errorf("nil children: got %s", out)
	}
	if r := Render(nil, RenderContext{}); r.HTML != "" || r.Text != "" {
		t.Errorf("nil root: got %+v", r)
	}
	if out := renderHTML(tree(para(&Node{Kind: Root, Children: []*Node{txt("inner")}}))); out != "<p></p>" {
		t.Errorf("root node inside the tree: got %s", out)
	}
}

func TestRenderTextExcludesQuotes(t *testing.T) {
	n := tree(
		para(txt("before")),
		el("quote", nil, para(txt("quoted")), el("quote", nil, para(txt("deep")))),
		renderEmbed("youtube", "M7lc1UVf-VE").Children[0],
		para(txt("after")),
	)
	r := Render(n, RenderContext{})
	if want := "before\nafter"; r.Text != want {
		t.Errorf("Text = %q, want %q", r.Text, want)
	}
	if !strings.Contains(string(r.HTML), "quoted") {
		t.Errorf("quote missing from HTML: %s", r.HTML)
	}
}

func TestRenderText(t *testing.T) {
	cases := []struct {
		name string
		in   *Node
		want string
	}{
		{"blocks join with newline", tree(para(txt("a")), el("h", at("level", "1"), txt("b")), para(txt("c"))), "a\nb\nc"},
		{"br is a newline", tree(para(txt("a"), el("br", nil), txt("b"))), "a\nb"},
		{"inline joins with no space", tree(para(txt("a"), el("b", nil, txt("b")), txt("c"))), "abc"},
		{"list items", tree(el("list", nil, renderLi("a"), renderLi("b"))), "a\nb"},
		{"nested list", tree(el("list", nil, el("li", nil, para(txt("a")), el("list", nil, renderLi("b"))), renderLi("c"))), "a\nb\nc"},
		{"pre", tree(para(txt("a")), el("pre", nil, txt("x\ny")), para(txt("b"))), "a\nx\ny\nb"},
		{"hr does not add text", tree(para(txt("a")), el("hr", nil), para(txt("b"))), "a\nb"},
		{"url display", tree(para(el("url", at("href", "/a", "display", "shown"), txt("hidden")))), "shown /a"},
		{"rejected link keeps text", tree(para(el("url", at("href", "javascript:x"), txt("kept")))), "kept"},
		{"spoiler", tree(para(el("spoiler", nil, txt("secret")))), "secret"},
		{"unknown element", tree(para(el("zzz", nil, txt("in")))), "in"},
		{"no double newline", tree(para(txt("a"), el("br", nil)), para(txt("b"))), "a\nb"},
		{"quote in a list item", tree(el("list", nil, el("li", nil, para(txt("a")), el("quote", nil, para(txt("q")))))), "a"},
		{"empty", tree(), ""},
	}
	for _, tc := range cases {
		if got := Render(tc.in, RenderContext{}).Text; got != tc.want {
			t.Errorf("%s: Text = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRenderTightList(t *testing.T) {
	tight := tree(el("list", at("tight", "true"), renderLi("a"), renderLi("b")))
	if got, want := renderHTML(tight), `<ul><li>a</li><li>b</li></ul>`; got != want {
		t.Errorf("tight: got %s, want %s", got, want)
	}
	loose := tree(el("list", nil, renderLi("a"), renderLi("b")))
	if got, want := renderHTML(loose), `<ul><li><p>a</p></li><li><p>b</p></li></ul>`; got != want {
		t.Errorf("loose: got %s, want %s", got, want)
	}
	// A paragraph in a quote in a tight item keeps its p. A nested loose list
	// inside a tight list keeps its own paragraphs.
	nested := tree(el("list", at("tight", "true"),
		el("li", nil, para(txt("a")), el("list", nil, renderLi("b")), el("quote", nil, para(txt("q"))))))
	want := `<ul><li>a<ul><li><p>b</p></li></ul><blockquote><p>q</p></blockquote></li></ul>`
	if got := renderHTML(nested); got != want {
		t.Errorf("nested: got %s, want %s", got, want)
	}
	ordered := tree(el("list", at("tight", "true", "ordered", "true", "start", "2"), renderLi("a")))
	if got, want := renderHTML(ordered), `<ol start="2"><li>a</li></ol>`; got != want {
		t.Errorf("ordered: got %s, want %s", got, want)
	}
}

// deepChain builds a chain of total nested elements named name, ending in text.
func deepChain(name string, total int) *Node {
	leaf := txt("x")
	cur := leaf
	for i := 0; i < total; i++ {
		cur = el(name, nil, cur)
	}
	return tree(cur)
}

func TestRenderDeepChainDoesNotOverflow(t *testing.T) {
	const depth = 100_000
	if Validate(deepChain("b", depth)) == nil {
		t.Fatal("test premise: Validate must reject the chain")
	}
	for _, name := range []string{"b", "quote", "li", "list", "unknown", "spoiler"} {
		r := Render(deepChain(name, depth), RenderContext{})
		if len(r.HTML) == 0 && name != "li" {
			t.Errorf("%s: empty HTML", name)
		}
		if name == "b" {
			s := string(r.HTML)
			if strings.Count(s, "<strong>") != depth || strings.Count(s, "</strong>") != depth {
				t.Errorf("b: unbalanced tags (%d open, %d close)", strings.Count(s, "<strong>"), strings.Count(s, "</strong>"))
			}
			if r.Text != "x" {
				t.Errorf("b: Text = %q", r.Text)
			}
		}
	}
}

func TestRenderCycleTerminates(t *testing.T) {
	loop := el("b", nil)
	loop.Children = []*Node{loop}
	s := string(Render(tree(loop), RenderContext{}).HTML)
	if s != "<strong></strong>" {
		t.Errorf("cycle: got %q, want one empty strong", s)
	}
	// A node that appears many times (a wide DAG) renders once.
	shared := el("b", nil, txt("x"))
	wide := tree(para(shared, shared))
	if got, want := string(Render(wide, RenderContext{}).HTML), "<p><strong>x</strong></p>"; got != want {
		t.Errorf("shared node: got %q, want %q", got, want)
	}
}

// TestRenderSelfReferenceBoundedMemory checks that a node with a large fan-out
// that lists itself costs memory in proportion to the tree, not to the fan-out
// raised to a depth.
func TestRenderSelfReferenceBoundedMemory(t *testing.T) {
	loop := el("b", nil)
	for i := 0; i < 10; i++ {
		loop.Children = append(loop.Children, loop)
	}
	root := tree(loop)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	r := Render(root, RenderContext{})
	runtime.ReadMemStats(&after)
	if got := string(r.HTML); got != "<strong></strong>" {
		t.Errorf("self reference: got %q", got)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 10<<20 {
		t.Errorf("self reference allocated %d bytes, want under 10 MB", alloc)
	}
}

// TestRenderLargeValidTree checks that no cap cuts off a valid tree: a p with
// 300,000 text and br pairs (600,000 nodes, past the old cap of 1<<19) renders every line in the HTML and the text.
func TestRenderLargeValidTree(t *testing.T) {
	const pairs = 300_000
	p := el("p", nil)
	p.Children = make([]*Node, 0, 2*pairs)
	for i := 0; i < pairs; i++ {
		p.Children = append(p.Children, txt("l"), el("br", nil))
	}
	root := tree(p)
	if err := Validate(root); err != nil {
		t.Fatalf("test premise: Validate rejects the tree: %v", err)
	}
	r := Render(root, RenderContext{})
	if got := strings.Count(string(r.HTML), "<br>"); got != pairs {
		t.Errorf("HTML holds %d br, want %d", got, pairs)
	}
	if got := strings.Count(r.Text, "l\n") + 1; got != pairs {
		t.Errorf("Text holds %d lines, want %d", got, pairs)
	}
}

func TestRenderMailtoURLEncoded(t *testing.T) {
	const addr = "a,b@c.example"
	for _, tc := range []struct {
		name string
		node *Node
	}{
		{"url", el("url", at("href", "mailto:"+addr), txt("t"))},
		{"url upper", el("url", at("href", "MAILTO:"+addr), txt("t"))},
		{"email", el("email", at("address", addr), txt("t"))},
	} {
		out := renderHTML(tree(para(tc.node)))
		if !strings.Contains(out, `href="mailto:a%2Cb@c.example"`) {
			t.Errorf("%s: got %s, want an encoded comma", tc.name, out)
		}
	}
}

func TestRenderEmailDoesNotDecodeEntities(t *testing.T) {
	// Validate rejects this address (it holds "&"), so the renderer must not
	// decode it into a working link.
	out := renderHTML(tree(para(el("email", at("address", "a&#64;c.example"), txt("m")))))
	if want := "<p>m</p>"; out != want {
		t.Errorf("got %s, want %s", out, want)
	}
}

func TestRenderSmileyHTTPUpgraded(t *testing.T) {
	rc := RenderContext{Smiley: func(code string) (Smiley, bool) {
		return Smiley{Code: code, Image: "http://img.example/s.gif", Alt: "smile"}, code == ":)"
	}}
	out := string(Render(tree(para(el("smiley", at("code", ":)")))), rc).HTML)
	want := `<p><img class="smiley" src="https://img.example/s.gif" alt="smile"></p>`
	if out != want {
		t.Errorf("got %s, want %s", out, want)
	}
}

// Image and Alt are live: the renderer uses both, and Alt falls back to the code
// when it is empty. The renderer escapes both.
func TestRenderSmileyUsesImageAndAlt(t *testing.T) {
	render := func(sm Smiley, ok bool) string {
		rc := RenderContext{Smiley: func(string) (Smiley, bool) { return sm, ok }}
		return string(Render(tree(para(el("smiley", at("code", ":)")))), rc).HTML)
	}
	tests := []struct {
		name string
		sm   Smiley
		ok   bool
		want string
	}{
		{"alt", Smiley{Image: "/s/a.gif", Alt: "happy face"}, true,
			`<p><img class="smiley" src="/s/a.gif" alt="happy face"></p>`},
		{"alt empty falls back to the code", Smiley{Image: "/s/a.gif"}, true,
			`<p><img class="smiley" src="/s/a.gif" alt=":)"></p>`},
		{"alt escaped", Smiley{Image: "/s/a.gif", Alt: `"><b>`}, true,
			`<p><img class="smiley" src="/s/a.gif" alt="&#34;&gt;&lt;b&gt;"></p>`},
		{"image escaped", Smiley{Image: "/s/a.gif?x=1&y=2"}, true,
			`<p><img class="smiley" src="/s/a.gif?x=1&amp;y=2" alt=":)"></p>`},
		{"lookup fails", Smiley{Image: "/s/a.gif", Alt: "x"}, false, `<p>:)</p>`},
		{"image fails the check", Smiley{Image: "javascript:x", Alt: "x"}, true, `<p>:)</p>`},
		{"image empty", Smiley{Alt: "x"}, true, `<p>:)</p>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(tc.sm, tc.ok); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestRenderNeverEmitsRawScript(t *testing.T) {
	n := tree(
		para(txt("<script>alert(1)</script>"), el("b", nil, txt("<img src=x onerror=y>"))),
		el("pre", at("lang", "x"), txt("</code></pre><script>")),
		el("zzz", at("href", "javascript:x"), txt("<svg onload=x>")),
	)
	out := renderHTML(n)
	for _, bad := range []string{"<script", "<img src=x", "<svg", "</code></pre><script"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q: %s", bad, out)
		}
	}
}

func TestRenderHTMLType(t *testing.T) {
	var _ template.HTML = Render(tree(), RenderContext{}).HTML
}

// outputTag matches an opening tag in rendered HTML, and outputAttr matches an
// href or src attribute in the attributes of one. The renderer escapes ">" and
// the quote in every value, so neither pattern can match inside a value.
var (
	outputTag  = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9]*)((?:\s[^>]*)?)>`)
	outputAttr = regexp.MustCompile(`\s(href|src)="([^"]*)"`)
)

// checkOutputURLs reports every href and src in r.HTML that the URL checks do
// not accept. It unescapes each value once, as a browser does. A mailto: href
// holds a percent-encoded address, so the check decodes the address first. An
// iframe src is one of the fixed provider origins, which CheckImageURL does not
// cover, so it must start with an origin that r.FrameSrc lists.
func checkOutputURLs(t *testing.T, r Rendered) {
	t.Helper()
	for _, tag := range outputTag.FindAllStringSubmatch(string(r.HTML), -1) {
		for _, a := range outputAttr.FindAllStringSubmatch(tag[2], -1) {
			name, val := a[1], html.UnescapeString(a[2])
			switch {
			case name == "src" && tag[1] == "iframe":
				ok := false
				for _, origin := range r.FrameSrc {
					ok = ok || strings.HasPrefix(val, origin+"/")
				}
				if !ok {
					t.Fatalf("iframe src %q is not under FrameSrc %q in: %s", val, r.FrameSrc, r.HTML)
				}
			case name == "src":
				if !CheckImageURL(val) {
					t.Fatalf("src %q fails CheckImageURL in: %s", val, r.HTML)
				}
			case len(val) >= len("mailto:") && strings.EqualFold(val[:len("mailto:")], "mailto:"):
				addr, err := url.PathUnescape(val[len("mailto:"):])
				if err != nil || !CheckLinkURL("mailto:"+addr) {
					t.Fatalf("href %q fails CheckLinkURL in: %s", val, r.HTML)
				}
			default:
				if !CheckLinkURL(val) {
					t.Fatalf("href %q fails CheckLinkURL in: %s", val, r.HTML)
				}
			}
		}
	}
}

// The fuzz checks are only as good as their patterns. This pins that they find
// the values in real output, and that they skip data attributes.
func TestOutputURLPatterns(t *testing.T) {
	r := Render(Parse("[m](mailto:a@b.c) ![i](/p.png?a=1&b=2)\n\nhttps://youtu.be/dQw4w9WgXcQ", allOn), renderSmileys())
	var got []string
	for _, tag := range outputTag.FindAllStringSubmatch(string(r.HTML), -1) {
		for _, a := range outputAttr.FindAllStringSubmatch(tag[2], -1) {
			got = append(got, tag[1]+" "+a[1]+" "+html.UnescapeString(a[2]))
		}
	}
	want := []string{
		"a href mailto:a@b.c",
		"img src /p.png?a=1&b=2",
		"iframe src https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("found %q, want %q in: %s", got, want, r.HTML)
	}
	checkRendered(t, r)
}

// checkRendered applies the output checks of FuzzRender to r.
func checkRendered(t *testing.T, r Rendered) {
	t.Helper()
	if strings.Contains(strings.ToLower(string(r.HTML)), "<script") {
		t.Fatalf("raw script tag in output: %s", r.HTML)
	}
	checkOutputURLs(t, r)
}

// A link with a display text shows a shortened form of its address. The search
// text keeps the full address, so a search finds the middle of a long URL.
func TestRenderTextKeepsFullHrefBehindDisplay(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 30) + "middlepart" + strings.Repeat("b", 30) + "/end"
	n := Parse(long, Options{Autolink: true})
	r := Render(n, RenderContext{})
	if !strings.Contains(string(r.HTML), " ... ") {
		t.Fatalf("precondition: the link has no display text: %s", r.HTML)
	}
	if strings.Contains(string(r.HTML), "middlepart") && !strings.Contains(string(r.HTML), `href="`+long+`"`) {
		t.Fatalf("precondition: unexpected HTML %s", r.HTML)
	}
	if !strings.Contains(r.Text, long) {
		t.Errorf("Text = %q, want it to contain the full address", r.Text)
	}
	if !strings.Contains(r.Text, " ... ") {
		t.Errorf("Text = %q, want it to keep the display text", r.Text)
	}
	// A short link has no display text and its address appears once.
	short := Render(Parse("https://example.com/x", Options{Autolink: true}), RenderContext{}).Text
	if short != "https://example.com/x" {
		t.Errorf("short link Text = %q", short)
	}
	// A quoted link adds nothing.
	q := Render(tree(el("quote", nil, para(el("url", at("href", "/a", "display", "d"))))), RenderContext{}).Text
	if q != "" {
		t.Errorf("quoted link Text = %q, want empty", q)
	}
}

func TestRenderBasePath(t *testing.T) {
	smileys := func(base string) RenderContext {
		return RenderContext{BasePath: base, Smiley: func(code string) (Smiley, bool) {
			return Smiley{Code: code, Image: "/files/smilies/smile.gif", Alt: "smile"}, code == ":)"
		}}
	}
	doc := tree(para(
		el("url", at("href", "/t/5"), txt("x")), txt(" "),
		el("img", at("src", "/a/1", "alt", "d")), txt(" "),
		el("smiley", at("code", ":)")), txt(" "),
		el("url", at("href", "https://a.example/x"), txt("far")), txt(" "),
		el("url", at("href", "mailto:a@b.example"), txt("mail")), txt(" "),
		el("img", at("src", "https://a.example/i.png", "alt", "far")),
	))
	for _, base := range []string{"/forum/", "/forum"} {
		got := string(Render(doc, smileys(base)).HTML)
		for _, want := range []string{
			`href="/forum/t/5"`,
			`src="/forum/a/1"`,
			`src="/forum/files/smilies/smile.gif"`,
			`href="https://a.example/x"`,
			`href="mailto:a@b.example"`,
			`src="https://a.example/i.png"`,
		} {
			if !strings.Contains(got, want) {
				t.Errorf("base %q: html lacks %s\n%s", base, want, got)
			}
		}
	}
	plain := Render(doc, smileys("")).Text
	for _, base := range []string{"/", "/forum/"} {
		if got := Render(doc, smileys(base)).Text; got != plain {
			t.Errorf("base %q: Text = %q, want %q", base, got, plain)
		}
	}
	// A base of "/" is the same as none.
	if a, b := Render(doc, smileys("/")).HTML, Render(doc, smileys("")).HTML; a != b {
		t.Errorf("base / changed the html:\n%s\n%s", a, b)
	}
}

// A base path in any odd form gives a rooted prefix, never a relative or a
// scheme-relative URL.
func TestRenderBasePathNormalized(t *testing.T) {
	doc := tree(para(el("url", at("href", "/t/5"), txt("x"))))
	for base, want := range map[string]string{
		"forum/":    `href="/forum/t/5"`,
		"forum":     `href="/forum/t/5"`,
		"/forum//":  `href="/forum/t/5"`,
		"//forum/":  `href="/forum/t/5"`,
		"///":       `href="/t/5"`,
		"/a/b/":     `href="/a/b/t/5"`,
		"":          `href="/t/5"`,
		"/":         `href="/t/5"`,
		"https://x": `href="/t/5"`, // not a path: ignored
		`/f\x`:      `href="/t/5"`,
	} {
		got := string(Render(doc, RenderContext{BasePath: base}).HTML)
		if !strings.Contains(got, want) {
			t.Errorf("BasePath %q: html = %s, want %s", base, got, want)
		}
	}
}

func TestRenderImageNote(t *testing.T) {
	doc := tree(para(
		el("img", at("src", "/a/1", "alt", "diagram")), txt(" "),
		el("img", at("src", "/a/2", "alt", "")), txt(" "),
		el("img", at("src", "/files/x.png", "alt", "other")),
	))
	rc := RenderContext{BasePath: "/forum/", ImageNote: func(src string) string {
		if strings.HasPrefix(src, "/a/") {
			return "sign in to view"
		}
		return ""
	}}
	r := Render(doc, rc)
	got := string(r.HTML)
	for _, want := range []string{
		`<span class="img-note">Image: diagram (sign in to view)</span>`,
		`<span class="img-note">Image: image (sign in to view)</span>`,
		`<img src="/forum/files/x.png"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("html lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, `src="/forum/a/`) {
		t.Errorf("the noted image still shows:\n%s", got)
	}
	// The plain text keeps the alt text, as before.
	if want := Render(doc, RenderContext{BasePath: "/forum/"}).Text; r.Text != want {
		t.Errorf("Text = %q, want %q", r.Text, want)
	}
	// A note with markup characters is escaped.
	rc.ImageNote = func(string) string { return `<b>"x"</b>` }
	if got := string(Render(tree(para(el("img", at("src", "/a/1", "alt", "<i>")))), rc).HTML); strings.Contains(got, "<b>") || strings.Contains(got, "<i>") {
		t.Errorf("unescaped note: %s", got)
	}
}

// Ruling R49: no embed loads a provider script into the page. X, Instagram and
// Threads render as a plain link, and every other embed is a frame of the
// provider, so no embed needs a script, a script origin or a connect origin.
func TestRenderEmbedsNeedNoScript(t *testing.T) {
	ids := map[string]string{
		"tweet": "20", "instagram": "CxYz_12-ab", "reddit": "programming/abc123", "threads": "zuck/C04flh-M8ii",
		"youtube": "M7lc1UVf-VE", "vimeo": "76979871", "tiktok": "6718335390845095173",
		"spotify": "track/4cOdK2wGLETKBW3PvgPWqT", "soundcloud": "forss/flickermood",
	}
	for tag, id := range ids {
		r := Render(renderEmbed(tag, id), RenderContext{})
		if len(r.Scripts)+len(r.ScriptSrc)+len(r.ConnectSrc) != 0 {
			t.Errorf("%s: Scripts = %v, ScriptSrc = %v, ConnectSrc = %v, want none", tag, r.Scripts, r.ScriptSrc, r.ConnectSrc)
		}
		out := string(r.HTML)
		if strings.Contains(out, "<script") || strings.Contains(out, "<blockquote") {
			t.Errorf("%s: the output holds a script or a blockquote: %s", tag, out)
		}
	}
	// The scripted tags are links, and the frame tags still frame.
	for _, tag := range []string{"tweet", "instagram", "threads"} {
		r := Render(renderEmbed(tag, ids[tag]), RenderContext{})
		if len(r.FrameSrc) != 0 || !strings.Contains(string(r.HTML), `class="embed-link"`) {
			t.Errorf("%s: want a plain link and no frame: %s %v", tag, r.HTML, r.FrameSrc)
		}
	}
	if r := Render(renderEmbed("youtube", ids["youtube"]), RenderContext{}); len(r.FrameSrc) == 0 || !strings.Contains(string(r.HTML), "<iframe") {
		t.Errorf("youtube lost its frame: %s", r.HTML)
	}
}

// Reddit renders as a plain link: no script, no frame, no origin.
func TestRenderRedditIsAPlainLink(t *testing.T) {
	r := Render(renderEmbed("reddit", "programming/abc123"), RenderContext{})
	if len(r.FrameSrc)+len(r.ScriptSrc)+len(r.ConnectSrc)+len(r.Scripts) != 0 {
		t.Errorf("origins for a Reddit link: %v %v %v %v", r.FrameSrc, r.ScriptSrc, r.ConnectSrc, r.Scripts)
	}
	out := string(r.HTML)
	for _, bad := range []string{"<iframe", "<script", "<blockquote", "embed.reddit.com"} {
		if strings.Contains(out, bad) {
			t.Errorf("the Reddit link holds %q: %s", bad, out)
		}
	}
	if !strings.Contains(out, ">View post on Reddit</a>") {
		t.Errorf("no link text: %s", out)
	}
}

// TestTextKeepsAngleBracketText covers text that stands only inside angle
// brackets. Rendered.Text is the faithful plain text of the post: it keeps the
// brackets, so a snippet shows what the member wrote. The search column and the
// title index of the database map the brackets to spaces (migration 0008); the
// tests of the board package check that a search finds the words.
func TestTextKeepsAngleBracketText(t *testing.T) {
	for _, md := range []bool{true, false} {
		src := "see <like this> here"
		r := Render(Parse(src, Options{Markdown: md}), RenderContext{})
		if r.Text != src {
			t.Errorf("markdown=%v: Text = %q, want %q", md, r.Text, src)
		}
		if want := "<p>see &lt;like this&gt; here</p>"; string(r.HTML) != want {
			t.Errorf("markdown=%v: HTML = %q, want %q", md, r.HTML, want)
		}
	}
	// Brackets in code, in a link address and in a display text count too.
	for src, want := range map[string]string{
		"`<code words>`":                           "<code words>",
		"```\n<fenced words>\n```":                 "<fenced words>",
		"[<in label>](https://example.com/?a=<b>)": "<in label>",
		"<tag>":                "<tag>",
		"if a < b then x -> y": "if a < b then x -> y",
	} {
		if got := Render(Parse(src, allOn), RenderContext{}).Text; got != want {
			t.Errorf("Text of %q = %q, want %q", src, got, want)
		}
	}
}

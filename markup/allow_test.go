package markup

import (
	"html"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// richSource holds every element and attribute that the renderer can write.
const richSource = "# h1\n\n## h2\n\n### h3\n\n#### h4\n\n---\n\n> quote\n\n- a\n- b\n\n7. c\n8. d\n\n" +
	"```go\ncode\n```\n\n`span` **b** *i* ||spoiler||\n\n" +
	"[l](https://example.com/x) [m](mailto:a@b.c) <a@b.c> ![i](/p.png) ![n](/note.png) :) line\\\nbreak\n\n" +
	"https://www.youtube.com/watch?v=dQw4w9WgXcQ\n\nhttps://vimeo.com/123456\n\nhttps://x.com/a/status/1234567890\n\n" +
	"https://www.instagram.com/p/CxYzAbCd12/\n\nhttps://www.reddit.com/r/golang/comments/abc123/title/\n\n" +
	"https://www.tiktok.com/@user/video/7234567890123456789\n\nhttps://www.threads.com/@user/post/CxYz_123\n\n" +
	"https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC\n\nhttps://soundcloud.com/artist/track-name\n"

// richRender renders richSource with the test smiley table and an image note.
func richRender() Rendered {
	rc := renderSmileys()
	rc.ImageNote = func(src string) string {
		if strings.HasSuffix(src, "note.png") {
			return "needs a right"
		}
		return ""
	}
	return Render(Parse(richSource, allOn), rc)
}

var (
	tagPattern  = regexp.MustCompile(`<([a-z][a-z0-9]*)((?:\s[^>]*)?)>`)
	attrPattern = regexp.MustCompile(`\s([a-z][a-z-]*)(?:="[^"]*")?`)
)

// usedTags returns each tag in html with the attributes it carries.
func usedTags(html string) map[string][]string {
	used := map[string][]string{}
	for _, m := range tagPattern.FindAllStringSubmatch(html, -1) {
		attrs := used[m[1]]
		for _, a := range attrPattern.FindAllStringSubmatch(m[2], -1) {
			if !slices.Contains(attrs, a[1]) {
				attrs = append(attrs, a[1])
			}
		}
		slices.Sort(attrs)
		used[m[1]] = attrs
	}
	return used
}

// TestAllowedIsWhatTheRendererWrites pins the table to the output in both
// directions: the renderer writes no tag or attribute outside the table, and the
// table lists nothing the renderer never writes. A looser table would let the
// HTML check pass markup that the renderer cannot produce.
func TestAllowedIsWhatTheRendererWrites(t *testing.T) {
	used := usedTags(string(richRender().HTML))
	want := Allowed()
	for tag := range want {
		slices.Sort(want[tag])
		if want[tag] == nil {
			want[tag] = nil
		}
	}
	for tag, attrs := range used {
		if len(attrs) == 0 {
			attrs = nil
		}
		used[tag] = attrs
	}
	for tag := range used {
		if _, ok := want[tag]; !ok {
			t.Errorf("the renderer writes <%s>, which Allowed does not list", tag)
		}
	}
	for tag, attrs := range want {
		got, ok := used[tag]
		if !ok {
			t.Errorf("Allowed lists <%s>, which the render of richSource never writes", tag)
			continue
		}
		if !slices.Equal(attrs, got) {
			t.Errorf("<%s>: Allowed lists attributes %v, the renderer writes %v", tag, attrs, got)
		}
	}
}

func TestAllowedReturnsACopy(t *testing.T) {
	a := Allowed()
	a["script"] = []string{"src"}
	delete(a, "p")
	a["a"][0] = "onclick"
	b := Allowed()
	if _, ok := b["script"]; ok {
		t.Error("a change to the result reached the table: script")
	}
	if _, ok := b["p"]; !ok {
		t.Error("a change to the result reached the table: p")
	}
	if b["a"][0] == "onclick" {
		t.Error("a change to the result reached the table: the attributes of a")
	}
	o := EmbedOrigins()
	o[0] = "https://evil.example"
	if EmbedOrigins()[0] == "https://evil.example" {
		t.Error("a change to EmbedOrigins reached the table")
	}
}

// TestAllowedHasNoDangerousEntry keeps the table clear of the elements and
// attributes that the HTML check refuses by name.
func TestAllowedHasNoDangerousEntry(t *testing.T) {
	for tag, attrs := range Allowed() {
		switch tag {
		case "script", "style", "object", "embed", "base", "form", "meta", "link", "svg", "math", "input", "button":
			t.Errorf("Allowed lists <%s>", tag)
		}
		for _, a := range attrs {
			if strings.HasPrefix(a, "on") || a == "style" || a == "srcdoc" || a == "formaction" {
				t.Errorf("Allowed lists the attribute %s on <%s>", a, tag)
			}
		}
	}
}

// TestRendererReadsTheTable removes entries from the table and requires the
// renderer to follow: it must take its tags from the table, not from a copy.
func TestRendererReadsTheTable(t *testing.T) {
	restore := allowed
	t.Cleanup(func() { allowed = restore })
	render := func() string { return string(richRender().HTML) }
	if !strings.Contains(render(), "<em>") {
		t.Fatal("precondition: no <em> in the render")
	}
	without := func(tag string) {
		allowed = maps.Clone(restore)
		delete(allowed, tag)
	}
	for _, tag := range []string{"em", "strong", "blockquote", "iframe", "img", "a", "pre", "li", "span", "div"} {
		without(tag)
		if out := render(); strings.Contains(out, "<"+tag) || strings.Contains(out, "</"+tag+">") {
			t.Errorf("the renderer wrote <%s> after the table dropped it", tag)
		}
	}
	for tag, attr := range map[string]string{"a": "rel", "img": "loading", "iframe": "allow", "span": "tabindex", "ol": "start", "pre": "class"} {
		allowed = maps.Clone(restore)
		allowed[tag] = slices.DeleteFunc(slices.Clone(restore[tag]), func(s string) bool { return s == attr })
		out := render()
		for _, m := range tagPattern.FindAllStringSubmatch(out, -1) {
			if m[1] == tag && regexp.MustCompile(`\s`+attr+`(=|\s|$)`).MatchString(m[2]) {
				t.Errorf("the renderer wrote the attribute %s on <%s> after the table dropped it: %s", attr, tag, m[0])
			}
		}
	}
}

func TestEmbedOriginsCoverTheEmbedTable(t *testing.T) {
	origins := EmbedOrigins()
	if !slices.IsSorted(origins) || len(slices.Compact(slices.Clone(origins))) != len(origins) {
		t.Errorf("EmbedOrigins = %v, want sorted and without duplicates", origins)
	}
	frames := richRender().FrameSrc
	for _, f := range frames {
		if !slices.Contains(origins, f) {
			t.Errorf("the render has the frame origin %q, which EmbedOrigins does not list", f)
		}
	}
	for _, o := range origins {
		if !strings.HasPrefix(o, "https://") || strings.HasSuffix(o, "/") {
			t.Errorf("origin %q is not a bare https origin", o)
		}
		if !slices.Contains(frames, o) {
			t.Errorf("EmbedOrigins lists %q, which no embed of richSource uses", o)
		}
	}
}

// TestValuelessAttributeCannotBeForged covers the one way to write an attribute
// with no value. A stored alt that equals the old marker string of the renderer
// must come out as a normal quoted value, and no member text may change the shape
// of a tag.
func TestValuelessAttributeCannotBeForged(t *testing.T) {
	for _, alt := range []string{"\x00bare", "bare", "\x00", ""} {
		n := tree(para(el("img", at("src", "/a.png", "alt", alt))))
		out := string(Render(n, RenderContext{}).HTML)
		want := `<img src="/a.png" alt="` + alt + `" loading="lazy" class="post-img">`
		if !strings.Contains(out, want) {
			t.Errorf("alt %q: HTML = %q, want it to hold %q", alt, out, want)
		}
	}
	// A smiley code and a language name are member text too.
	if out := openTag("iframe", "allowfullscreen", "\x00bare"); out != `<iframe allowfullscreen="`+"\x00bare"+`">` {
		t.Errorf("a string value wrote %q", out)
	}
	if out := openTag("iframe", "allowfullscreen", bare); out != `<iframe allowfullscreen>` {
		t.Errorf("the bare value wrote %q", out)
	}
}

// freeAttrs are the attributes whose values are addresses or member text. The
// HTML check has its own rules for them (the URL rules, or nothing for an alt).
var freeAttrs = map[string]bool{
	"a/href": true, "img/src": true, "img/alt": true, "iframe/src": true,
	"blockquote/data-instgrm-permalink": true, "blockquote/data-text-post-permalink": true,
}

// patternAttrs are the attributes whose value follows a rule, not a list.
var patternAttrs = map[string]bool{"ol/start": true, "code/class": true}

func unescapeHTML(s string) string { return html.UnescapeString(s) }

var attrValuePattern = regexp.MustCompile(`\s([a-z][a-z-]*)(?:="([^"]*)")?`)

// usedValues returns, for each "tag/attr", the values in html. An attribute with
// no value has the value "".
func usedValues(html string) map[string][]string {
	used := map[string][]string{}
	for _, m := range tagPattern.FindAllStringSubmatch(html, -1) {
		for _, a := range attrValuePattern.FindAllStringSubmatch(m[2], -1) {
			key := m[1] + "/" + a[1]
			v := unescapeHTML(a[2])
			if !slices.Contains(used[key], v) {
				used[key] = append(used[key], v)
			}
		}
	}
	return used
}

// TestPinnedValuesAreWhatTheRendererWrites pins the values of the attributes that
// the renderer always writes as a constant, in both directions: every value in
// the output is in the table, and every value in the table is written. Every
// attribute of [Allowed] is pinned, a pattern, or listed as free.
func TestPinnedValuesAreWhatTheRendererWrites(t *testing.T) {
	used := usedValues(string(richRender().HTML))
	for tag, attrs := range Allowed() {
		for _, attr := range attrs {
			key := tag + "/" + attr
			values, pinned := fixedValues[tag][attr]
			switch {
			case freeAttrs[key], patternAttrs[key]:
				if pinned {
					t.Errorf("%s is both pinned and free", key)
				}
			case !pinned:
				t.Errorf("%s has no pinned value and is not listed as free", key)
			default:
				got := slices.Clone(used[key])
				want := slices.Clone(values)
				slices.Sort(got)
				slices.Sort(want)
				if !slices.Equal(got, want) {
					t.Errorf("%s: the renderer writes %q, the table pins %q", key, got, want)
				}
			}
		}
	}
	for key, values := range used {
		tag, attr, _ := strings.Cut(key, "/")
		for _, v := range values {
			if !AttrValueOK(tag, attr, v) {
				t.Errorf("AttrValueOK(%s, %s, %q) refuses a value the renderer writes", tag, attr, v)
			}
		}
	}
}

func TestAttrValueOK(t *testing.T) {
	for _, tc := range []struct {
		tag, attr, value string
		want             bool
	}{
		{"a", "rel", "nofollow ugc noopener", true},
		{"a", "rel", "noopener", false},
		{"a", "rel", "", false},
		{"a", "rel", "nofollow ugc noopener x", false},
		{"iframe", "allow", "fullscreen", true},
		{"iframe", "allow", "camera; microphone", false},
		{"iframe", "allow", "fullscreen; camera", false},
		{"iframe", "allow", "*", false},
		{"iframe", "referrerpolicy", "strict-origin-when-cross-origin", true},
		{"iframe", "referrerpolicy", "unsafe-url", false},
		{"iframe", "loading", "lazy", true},
		{"iframe", "loading", "eager", false},
		{"iframe", "allowfullscreen", "", true},
		{"iframe", "allowfullscreen", "allowfullscreen", false},
		{"iframe", "class", "embed youtube", true},
		{"iframe", "class", "embed youtube x", false},
		{"iframe", "title", "YouTube video", true},
		{"iframe", "title", "x", false},
		{"img", "loading", "lazy", true},
		{"img", "loading", "eager", false},
		{"img", "class", "post-img", true},
		{"img", "class", "smiley", true},
		{"img", "class", "x", false},
		{"span", "class", "spoiler", true},
		{"span", "class", "img-note", true},
		{"span", "class", "x", false},
		{"span", "tabindex", "0", true},
		{"span", "tabindex", "-1", false},
		{"span", "tabindex", "1", false},
		{"div", "class", "embed-link", true},
		{"div", "class", "x", false},
		{"pre", "class", "code", true},
		{"pre", "class", "code x", false},
		{"blockquote", "class", "twitter-tweet", true},
		{"blockquote", "class", "x", false},
		{"code", "class", "language-go", true},
		{"code", "class", "language-c++", true},
		{"code", "class", "language-", false},
		{"code", "class", "language-a b", false},
		{"code", "class", "language-a\"b", false},
		{"code", "class", "x", false},
		{"ol", "start", "3", true},
		{"ol", "start", "123456789", true},
		{"ol", "start", "0", false},
		{"ol", "start", "03", false},
		{"ol", "start", "1234567890", false},
		{"ol", "start", "x", false},
		{"ol", "start", "", false},
		// Not pinned: the check has its own rules.
		{"a", "href", "anything", true},
		{"img", "alt", "anything", true},
		{"img", "src", "anything", true},
	} {
		if got := AttrValueOK(tc.tag, tc.attr, tc.value); got != tc.want {
			t.Errorf("AttrValueOK(%s, %s, %q) = %v, want %v", tc.tag, tc.attr, tc.value, got, tc.want)
		}
	}
}

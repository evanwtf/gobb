package markup

import (
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

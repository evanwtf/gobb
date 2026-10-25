package markup_test

import (
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

// embedCases holds one good ID for each embed tag, and the link that NoEmbeds
// must write.
var embedCases = []struct{ tag, id, href string }{
	{"youtube", "dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ"},
	{"vimeo", "76979871", "https://vimeo.com/76979871"},
	{"tweet", "20", "https://x.com/i/status/20"},
	{"instagram", "CxYz_12-ab", "https://www.instagram.com/p/CxYz_12-ab/"},
	{"reddit", "golang/abc123", "https://www.reddit.com/r/golang/comments/abc123/"},
	{"tiktok", "7123456789012345678", "https://www.tiktok.com/player/v1/7123456789012345678"},
	{"threads", "zuck/Cx_12", "https://www.threads.com/@zuck/post/Cx_12/"},
	{"spotify", "track/4uLU6hMCjMI75M1A2tKUQC", "https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC"},
	{"soundcloud", "artist/track-name", "https://soundcloud.com/artist/track-name"},
}

func embedTree(tag, id string) *markup.Node {
	return &markup.Node{Kind: markup.Root, Children: []*markup.Node{
		{Kind: markup.Element, Name: tag, Attrs: map[string]string{"id": id}},
	}}
}

// TestNoEmbedsWritesALink covers A-D2: a signature renders an embed as a plain
// link, with no frame and no provider script, so it cannot load third-party script
// on every page that shows it. A post keeps its embed.
func TestNoEmbedsWritesALink(t *testing.T) {
	for _, tc := range embedCases {
		t.Run(tc.tag, func(t *testing.T) {
			tree := embedTree(tc.tag, tc.id)
			if err := markup.Validate(tree); err != nil {
				t.Fatalf("the case is not a valid embed: %v", err)
			}
			// A post keeps the embed: a frame, or a provider script.
			post := markup.Render(tree, markup.RenderContext{})
			if len(post.FrameSrc) == 0 && tc.tag != "reddit" {
				t.Errorf("the post lost its embed: %s", post.HTML)
			}
			sig := markup.Render(tree, markup.RenderContext{NoEmbeds: true})
			html := string(sig.HTML)
			want := `<a href="` + tc.href + `" rel="nofollow ugc noopener">`
			if !strings.Contains(html, want) {
				t.Errorf("no plain link %s in %s", want, html)
			}
			for _, bad := range []string{"<iframe", "<blockquote", "<script", "embed "} {
				if strings.Contains(html, bad) {
					t.Errorf("%q in %s", bad, html)
				}
			}
			if len(sig.FrameSrc)+len(sig.ScriptSrc)+len(sig.ConnectSrc)+len(sig.Scripts) != 0 {
				t.Errorf("origins for a signature: %+v", sig)
			}
			htmlcheck.Check(t, html)
		})
	}
}

// TestNoEmbedsKeepsOtherMarkup checks that the option changes nothing else.
func TestNoEmbedsKeepsOtherMarkup(t *testing.T) {
	tree := markup.Parse("**bold** [link](https://example.com) :)\n\nhttps://youtu.be/dQw4w9WgXcQ", markup.Options{Markdown: true, Autolink: true})
	with := markup.Render(tree, markup.RenderContext{NoEmbeds: true})
	without := markup.Render(tree, markup.RenderContext{})
	if len(without.FrameSrc) == 0 {
		t.Fatal("the test text holds no embed")
	}
	if !strings.Contains(string(with.HTML), "<strong>bold</strong>") && !strings.Contains(string(with.HTML), "<b>bold</b>") {
		t.Errorf("bold lost: %s", with.HTML)
	}
	if !strings.Contains(string(with.HTML), `href="https://www.youtube.com/watch?v=dQw4w9WgXcQ"`) {
		t.Errorf("the embed is not a link: %s", with.HTML)
	}
}

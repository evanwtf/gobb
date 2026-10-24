package htmlcheck_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

// catches is one hand-written fragment that the check must refuse, and a part of
// the message that names the reason. A check that returns early, or passes
// everything, or lacks one rule, fails the case of that rule.
var catches = []struct {
	name     string
	fragment string
	want     string
}{
	// An element outside the allowlist.
	{"element marquee", `<marquee>x</marquee>`, "element <marquee> is not allowed"},
	{"element h1", `<h1>x</h1>`, "element <h1> is not allowed"},
	{"element table", `<table><tr><td>x</td></tr></table>`, "element <table> is not allowed"},
	{"element font", `<font color="red">x</font>`, "element <font> is not allowed"},
	{"element svg", `<svg viewBox="0 0 1 1"><circle r="1"></circle></svg>`, "element <svg> is not allowed"},
	{"element math", `<math><mi>x</mi></math>`, "element <math> is not allowed"},
	{"element textarea", `<textarea>x</textarea>`, "element <textarea> is not allowed"},
	{"element video", `<video src="/a.mp4"></video>`, "element <video> is not allowed"},
	{"element noscript", `<noscript>x</noscript>`, "element <noscript> is not allowed"},
	{"element made-up", `<p>x</p><zzz>y</zzz>`, "element <zzz> is not allowed"},
	{"element in the name of a bad tag", `<a<b>x`, "element <a<b> is not allowed"},
	{"end tag of an element outside the list", `<p>x</p></marquee>`, "element <marquee> is not allowed"},
	{"self-closing element outside the list", `<p>x</p><keygen/>`, "element <keygen> is not allowed"},

	// An attribute outside the allowlist of its element.
	{"attribute id on p", `<p id="x">y</p>`, "attribute id is not allowed on <p>"},
	{"attribute target on a", `<a href="/x" target="_blank">y</a>`, "attribute target is not allowed on <a>"},
	{"attribute srcset on img", `<img src="/a.png" srcset="/b.png 2x">`, "attribute srcset is not allowed on <img>"},
	{"attribute srcdoc on iframe", `<iframe src="https://www.youtube-nocookie.com/embed/x" srcdoc="x"></iframe>`, "attribute srcdoc is not allowed on <iframe>"},
	{"attribute href on img", `<img src="/a.png" href="/b">`, "attribute href is not allowed on <img>"},
	{"attribute class on p", `<p class="x">y</p>`, "attribute class is not allowed on <p>"},
	{"attribute data on code", `<code data-x="1">y</code>`, "attribute data-x is not allowed on <code>"},
	{"attribute ping on a", `<a href="/x" ping="/y">z</a>`, "attribute ping is not allowed on <a>"},

	// Any attribute whose name starts with "on".
	{"onclick on p", `<p onclick="x()">y</p>`, "event handler attribute onclick on <p>"},
	{"onerror on img", `<img src="/a.png" onerror="x()">`, "event handler attribute onerror on <img>"},
	{"onload on iframe", `<iframe src="https://player.vimeo.com/video/1" onload="x()"></iframe>`, "event handler attribute onload on <iframe>"},
	{"upper case onmouseover", `<a href="/x" ONMOUSEOVER="x()">y</a>`, "event handler attribute onmouseover on <a>"},
	{"bare on", `<p on>y</p>`, "event handler attribute on on <p>"},
	{"unknown on name", `<p onfuturething="x">y</p>`, "event handler attribute onfuturething on <p>"},

	// A style attribute.
	{"style on p", `<p style="color:red">y</p>`, "style attribute on <p>"},
	{"style on a", `<a href="/x" style="x">y</a>`, "style attribute on <a>"},
	{"upper case style", `<span STYLE="x">y</span>`, "style attribute on <span>"},
	{"style on img", `<img src="/a.png" style="x">`, "style attribute on <img>"},

	// An href that fails the URL rules.
	{"href javascript", `<a href="javascript:alert(1)">x</a>`, `href "javascript:alert(1)" fails the URL rules`},
	{"href vbscript", `<a href="vbscript:x">x</a>`, "fails the URL rules"},
	{"href data", `<a href="data:text/html,x">x</a>`, "fails the URL rules"},
	{"href with a tab in the scheme", "<a href=\"java\tscript:x\">x</a>", "fails the URL rules"},
	{"href with an entity in the scheme", `<a href="&#106;avascript:x">x</a>`, "fails the URL rules"},
	{"href with a newline", "<a href=\"/x\nonerror\">x</a>", "fails the URL rules"},
	{"href protocol-relative", `<a href="//evil.example/x">x</a>`, "fails the URL rules"},
	{"href with a backslash host", `<a href="/\evil.example">x</a>`, "fails the URL rules"},
	{"href ftp", `<a href="ftp://example.com/x">x</a>`, "fails the URL rules"},
	{"href relative", `<a href="foo/bar">x</a>`, "fails the URL rules"},
	{"href fragment", `<a href="#x">x</a>`, "fails the URL rules"},
	{"href empty", `<a href="">x</a>`, "fails the URL rules"},
	{"href without a host", `<a href="https:///x">x</a>`, "fails the URL rules"},
	{"href mailto with a header", `<a href="mailto:a%40b.c%3Fcc%3Dd@e.f">x</a>`, "fails the URL rules"},
	{"href mailto without an address", `<a href="mailto:">x</a>`, "fails the URL rules"},
	{"href with a format character", "<a href=\"https://example.com/\u202e\">x</a>", "fails the URL rules"},
	{"href missing", `<a rel="nofollow">x</a>`, "<a> has no href"},

	// A src that fails the URL rules.
	{"src data", `<img src="data:image/png;base64,AAAA">`, `src "data:image/png;base64,AAAA" fails the URL rules`},
	{"src javascript", `<img src="javascript:x">`, "fails the URL rules"},
	{"src mailto", `<img src="mailto:a@b.c">`, "fails the URL rules"},
	{"src protocol-relative", `<img src="//evil.example/a.png">`, "fails the URL rules"},
	{"src empty", `<img src="">`, "fails the URL rules"},
	{"src missing", `<img alt="x">`, "<img> has no src"},

	// An iframe whose src origin is not in the embed table.
	{"iframe other origin", `<iframe src="https://evil.example/embed/x"></iframe>`, "iframe src"},
	{"iframe lookalike host", `<iframe src="https://www.youtube-nocookie.com.evil.example/embed/x"></iframe>`, "iframe src"},
	{"iframe user info", `<iframe src="https://www.youtube-nocookie.com@evil.example/x"></iframe>`, "iframe src"},
	{"iframe http", `<iframe src="http://www.youtube-nocookie.com/embed/x"></iframe>`, "iframe src"},
	{"iframe javascript", `<iframe src="javascript:x"></iframe>`, "iframe src"},
	{"iframe data", `<iframe src="data:text/html,x"></iframe>`, "iframe src"},
	{"iframe site path", `<iframe src="/files/x"></iframe>`, "iframe src"},
	{"iframe bare origin", `<iframe src="https://www.youtube-nocookie.com"></iframe>`, "iframe src"},
	{"iframe without src", `<iframe title="x"></iframe>`, "iframe src"},
	{"iframe with content", `<iframe src="https://player.vimeo.com/video/1"><b>x</b></iframe>`, "angle bracket in text"},
	{"iframe origin prefix of another host", `<iframe src="https://www.youtube-nocookie.comx/embed/x"></iframe>`, "iframe src"},

	// The forbidden elements, each by name.
	{"script", `<script>alert(1)</script>`, "forbidden element <script>"},
	{"script upper case", `<SCRIPT>alert(1)</SCRIPT>`, "forbidden element <script>"},
	{"script with src", `<script src="https://evil.example/x.js"></script>`, "forbidden element <script>"},
	{"script self-closing", `<script/>`, "forbidden element <script>"},
	{"script end tag alone", `x</script>`, "forbidden element <script>"},
	{"style element", `<style>p{color:red}</style>`, "forbidden element <style>"},
	{"object", `<object data="/x"></object>`, "forbidden element <object>"},
	{"embed", `<embed src="/x">`, "forbidden element <embed>"},
	{"base", `<base href="https://evil.example/">`, "forbidden element <base>"},
	{"form", `<form action="/x"><p>y</p></form>`, "forbidden element <form>"},
	{"meta", `<meta http-equiv="refresh" content="0;url=https://evil.example/">`, "forbidden element <meta>"},
	{"link", `<link rel="stylesheet" href="https://evil.example/x.css">`, "forbidden element <link>"},

	// The permalink attributes of an embed hold a URL too.
	{"instagram permalink javascript", `<blockquote class="instagram-media" data-instgrm-permalink="javascript:x"></blockquote>`, "data-instgrm-permalink"},
	{"threads permalink data", `<blockquote class="text-post-media" data-text-post-permalink="data:x"></blockquote>`, "data-text-post-permalink"},

	// An attribute that the renderer always writes as a constant takes only that
	// constant.
	{"allow with other permissions", `<iframe src="https://www.youtube-nocookie.com/embed/x" allow="camera; microphone"></iframe>`, `attribute allow="camera; microphone" on <iframe> is not a value`},
	{"allow with a wildcard", `<iframe src="https://www.youtube-nocookie.com/embed/x" allow="*"></iframe>`, `attribute allow="*" on <iframe> is not a value`},
	{"allow with an extra permission", `<iframe src="https://www.youtube-nocookie.com/embed/x" allow="fullscreen; camera"></iframe>`, `attribute allow="fullscreen; camera" on <iframe> is not a value`},
	{"referrerpolicy unsafe", `<iframe src="https://player.vimeo.com/video/1" referrerpolicy="unsafe-url"></iframe>`, `attribute referrerpolicy="unsafe-url" on <iframe> is not a value`},
	{"iframe loading eager", `<iframe src="https://player.vimeo.com/video/1" loading="eager"></iframe>`, `attribute loading="eager" on <iframe> is not a value`},
	{"iframe title", `<iframe src="https://player.vimeo.com/video/1" title="x"></iframe>`, `attribute title="x" on <iframe> is not a value`},
	{"iframe class", `<iframe src="https://player.vimeo.com/video/1" class="embed vimeo x"></iframe>`, `attribute class="embed vimeo x" on <iframe> is not a value`},
	{"allowfullscreen with a value", `<iframe src="https://player.vimeo.com/video/1" allowfullscreen="x"></iframe>`, `attribute allowfullscreen="x" on <iframe> is not a value`},
	{"rel opener", `<a href="/x" rel="opener">x</a>`, `attribute rel="opener" on <a> is not a value`},
	{"rel empty", `<a href="/x" rel="">x</a>`, `attribute rel="" on <a> is not a value`},
	{"img loading eager", `<img src="/a.png" loading="eager">`, `attribute loading="eager" on <img> is not a value`},
	{"img class", `<img src="/a.png" class="x">`, `attribute class="x" on <img> is not a value`},
	{"span class", `<span class="x">y</span>`, `attribute class="x" on <span> is not a value`},
	{"span tabindex 1", `<span class="spoiler" tabindex="1">y</span>`, `attribute tabindex="1" on <span> is not a value`},
	{"span tabindex negative", `<span class="spoiler" tabindex="-1">y</span>`, `attribute tabindex="-1" on <span> is not a value`},
	{"div class", `<div class="x">y</div>`, `attribute class="x" on <div> is not a value`},
	{"pre class", `<pre class="x"><code>y</code></pre>`, `attribute class="x" on <pre> is not a value`},
	{"blockquote class", `<blockquote class="x">y</blockquote>`, `attribute class="x" on <blockquote> is not a value`},
	{"code class", `<code class="x">y</code>`, `attribute class="x" on <code> is not a value`},
	{"code class with a space", `<code class="language-a b">y</code>`, `attribute class="language-a b" on <code> is not a value`},
	{"ol start zero", `<ol start="0"><li>x</li></ol>`, `attribute start="0" on <ol> is not a value`},
	{"ol start text", `<ol start="x"><li>x</li></ol>`, `attribute start="x" on <ol> is not a value`},

	// The tags balance: each start tag has its end tag, in order.
	{"unclosed p", `<p>x`, "unclosed <p>"},
	{"unclosed a", `<p>x</p><a href="/x">y`, "unclosed <a>"},
	{"unclosed span", `<p><span class="spoiler" tabindex="0">y</p>`, "end tag </p> does not match <span>"},
	{"unclosed li", `<ul><li>x</ul>`, "end tag </ul> does not match <li>"},
	{"unclosed blockquote", `<blockquote><p>x</p>`, "unclosed <blockquote>"},
	{"stray end tag", `<p>x</p></p>`, "end tag </p> has no start tag"},
	{"end tag alone", `</p>`, "end tag </p> has no start tag"},
	{"wrong order", `<p><strong>x</p></strong>`, "end tag </p> does not match <strong>"},
	{"self-closing p opens", `<p/>x`, "unclosed <p>"},
	{"end tag of a void element", `<br></br>`, "end tag </br> has no start tag"},
	{"end tag of img", `<img src="/a.png"></img>`, "end tag </img> has no start tag"},

	// Markup that is not an element at all.
	{"comment", `<p>a</p><!-- x -->`, "comment"},
	{"conditional comment", `<!--[if IE]><script>x</script><![endif]-->`, "comment"},
	{"cdata", `<![CDATA[x]]>`, "comment"},
	{"processing instruction", `<?php echo 1 ?>`, "comment"},
	{"bogus comment", `</ >`, "comment"},
	{"doctype", `<!DOCTYPE html><p>x</p>`, "doctype"},
	{"incomplete tag", `<p>x</p><a href="/x`, "incomplete tag"},
	{"incomplete handler", `<img src=x onerror=y`, "incomplete tag"},
	{"duplicate attribute", `<a href="/x" href="/y">z</a>`, "duplicate attribute href"},
	{"raw less-than in text", `<p>a < b</p>`, "angle bracket in text"},
	{"raw greater-than in text", `<p>a > b</p>`, "angle bracket in text"},
	{"trailing less-than", `<p>a</p><`, "angle bracket in text"},
}

func TestCheckCatches(t *testing.T) {
	for _, tc := range catches {
		t.Run(tc.name, func(t *testing.T) {
			got := htmlcheck.Problems(tc.fragment)
			if len(got) == 0 {
				t.Fatalf("Problems(%q) found nothing, want a problem that holds %q", tc.fragment, tc.want)
			}
			for _, p := range got {
				if strings.Contains(p, tc.want) {
					return
				}
			}
			t.Errorf("Problems(%q) = %q, want one that holds %q", tc.fragment, got, tc.want)
		})
	}
}

// TestCheckCatchesEachForbiddenCase lists the rules of the brief and requires a
// row of the table above for each, so a deleted row shows up as a failure.
func TestCheckCatchesEachForbiddenCase(t *testing.T) {
	need := []string{
		"element <marquee> is not allowed",
		"attribute id is not allowed on <p>",
		"event handler attribute onclick on <p>",
		"style attribute on <p>",
		`href "javascript:alert(1)" fails the URL rules`,
		`src "data:image/png;base64,AAAA" fails the URL rules`,
		"iframe src",
	}
	for _, name := range []string{"script", "style", "object", "embed", "base", "form", "meta", "link"} {
		need = append(need, "forbidden element <"+name+">")
	}
	for _, want := range need {
		found := false
		for _, tc := range catches {
			found = found || tc.want == want
		}
		if !found {
			t.Errorf("no row of the table expects %q", want)
		}
	}
}

// TestCheckAcceptsWhatTheRendererWrites feeds the check real output: a post that
// uses every element, attribute and embed of the renderer. Any problem here
// means the check is stricter than the renderer, and a page test would fail.
func TestCheckAcceptsWhatTheRendererWrites(t *testing.T) {
	src := "# h1\n\n## h2\n\n### h3\n\n#### h4\n\n---\n\n> quote\n\n- a\n- b\n\n7. c\n8. d\n\n" +
		"```go\ncode\n```\n\n`span` **b** *i* ||spoiler|| a < b > c\n\n" +
		"[l](https://example.com/x?a=1&b=2) [m](mailto:a@b.c) [p](/path?q=\"1\") <a@b.c> ![i](/p.png) ![n](/note.png) :) line\\\nbreak\n\n" +
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ\n\nhttps://vimeo.com/123456\n\nhttps://x.com/a/status/1234567890\n\n" +
		"https://www.instagram.com/p/CxYzAbCd12/\n\nhttps://www.reddit.com/r/golang/comments/abc123/title/\n\n" +
		"https://www.tiktok.com/@user/video/7234567890123456789\n\nhttps://www.threads.com/@user/post/CxYz_123\n\n" +
		"https://open.spotify.com/track/4uLU6hMCjMI75M1A2tKUQC\n\nhttps://soundcloud.com/artist/track-name\n"
	opt := markup.Options{Markdown: true, Smilies: true, Autolink: true,
		SmileyList: []markup.Smiley{{Code: ":)", Image: "/static/smile.gif", Alt: "smile"}}}
	for _, base := range []string{"", "/forum/"} {
		rc := markup.RenderContext{BasePath: base,
			Smiley: func(code string) (markup.Smiley, bool) {
				return markup.Smiley{Code: code, Image: "/static/smile.gif", Alt: "smile"}, code == ":)"
			},
			ImageNote: func(src string) string {
				if strings.HasSuffix(src, "note.png") {
					return "needs a right"
				}
				return ""
			},
		}
		out := string(markup.Render(markup.Parse(src, opt), rc).HTML)
		if p := htmlcheck.Problems(out); len(p) != 0 {
			t.Errorf("base %q: problems %q in %s", base, p, out)
		}
		htmlcheck.Check(t, out)
	}
}

func TestCheckAccepts(t *testing.T) {
	for _, f := range []string{
		``,
		`plain text`,
		`<p>a &lt; b &gt; c &amp; &quot;d&quot;</p>`,
		`<p>x</p><br><hr>`,
		`<a href="https://example.com/x" rel="nofollow ugc noopener">x</a>`,
		`<a href="/forum/t/1">x</a>`,
		`<a href="mailto:a%40b.c">x</a>`,
		`<img src="/files/a.png" alt="" loading="lazy" class="post-img">`,
		`<img src="https://example.com/a.png?x=1&amp;y=2" alt="x">`,
		`<iframe class="embed youtube" src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" title="YouTube video" allowfullscreen></iframe>`,
		`<ol start="3"><li>x</li></ol>`,
		`<span class="spoiler" tabindex="0">x</span>`,
		`<P>upper case tag</P>`,
		`<ul><li><p>x</p></li></ul><br><hr><img src="/a.png" alt="x" loading="lazy" class="post-img">`,
		`<pre class="code"><code class="language-c++">x</code></pre>`,
		`<blockquote class="twitter-tweet"><a href="https://x.com/i/status/1">View post on X</a></blockquote>`,
	} {
		if p := htmlcheck.Problems(f); len(p) != 0 {
			t.Errorf("Problems(%q) = %q, want none", f, p)
		}
	}
}

// recorder is a testing.TB that records the failures and stops nothing.
type recorder struct {
	testing.TB
	errors []string
	fatal  bool
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.fatal = true
	r.Errorf(format, args...)
}

func TestCheckReportsThroughT(t *testing.T) {
	r := &recorder{TB: t}
	htmlcheck.Check(r, `<script>x</script><p onclick="y">z</p>`)
	if len(r.errors) < 2 {
		t.Fatalf("Check reported %d problems, want at least 2 (every problem, not the first): %q", len(r.errors), r.errors)
	}
	if !strings.Contains(strings.Join(r.errors, "\n"), "<script>x</script>") {
		t.Errorf("the report does not show the fragment: %q", r.errors)
	}
	ok := &recorder{TB: t}
	htmlcheck.Check(ok, `<p>fine</p>`)
	if len(ok.errors) != 0 {
		t.Errorf("Check reported %q for a fine fragment", ok.errors)
	}
}

// TestNotInTheBinary keeps golang.org/x/net/html out of gobb: only tests import
// the check. The standard library's own vendored copies of x/net are fine.
func TestNotInTheBinary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/evanwtf/gobb/cmd/gobb").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 50 {
		t.Fatalf("go list gave %d lines, want the dependency list of the binary", len(lines))
	}
	for _, pkg := range lines {
		if pkg == "golang.org/x/net/html" || strings.HasPrefix(pkg, "golang.org/x/net/") {
			t.Errorf("the gobb binary depends on %s", pkg)
		}
		if pkg == "github.com/evanwtf/gobb/internal/htmlcheck" {
			t.Errorf("the gobb binary depends on %s", pkg)
		}
	}
}

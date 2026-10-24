package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// cssRule matches the selector list of a rule that has declarations.
	cssRule = regexp.MustCompile(`([^{}]+)\{[^{}]*\}`)
	// cssLastCompound is the part after the last combinator of a selector.
	cssLastCompound = regexp.MustCompile(`[^\s>+~]+$`)
)

// siblingTargets returns, for each selector of css that has a sibling combinator,
// the compound selector at its end that can match a blockquote: the element is
// blockquote itself, or any element (a class, an id, an attribute or a
// pseudo-class with no element name, or *).
func siblingTargets(css string) []string {
	css = cssComment.ReplaceAllString(css, "")
	var out []string
	for _, m := range cssRule.FindAllStringSubmatch(css, -1) {
		for sel := range strings.SplitSeq(m[1], ",") {
			sel = strings.TrimSpace(sel)
			if !strings.ContainsAny(sel, "+~") {
				continue
			}
			last := cssLastCompound.FindString(sel)
			// Drop a pseudo-element: it styles a part of the element.
			if i := strings.Index(last, "::"); i >= 0 {
				last = last[:i]
			}
			if last == "" {
				continue
			}
			typ := last
			if i := strings.IndexAny(typ, ".#[:"); i >= 0 {
				typ = typ[:i]
			}
			if typ == "" || typ == "*" || typ == "blockquote" {
				out = append(out, sel)
			}
		}
	}
	return out
}

// A provider script mounts its card and may leave or remove its fallback
// blockquote, so a rule that picks a blockquote by the element before it can hit
// a quote that a member wrote. Gobb hides a fallback by its own class instead.
func TestCSSHasNoSiblingRuleForBlockquotes(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/gobb.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range siblingTargets(string(b)) {
		t.Errorf("a sibling selector can match a blockquote in a post: %q", sel)
	}
}

func TestSiblingTargets(t *testing.T) {
	for _, tc := range []struct {
		css  string
		want int
	}{
		{`.a li + li::before { x: y }`, 0},
		{`.notice p + p { x: y }`, 0},
		{`iframe.x + blockquote { display: none }`, 1},
		{`.post-body iframe.x + blockquote.y, .z p + p { a: b }`, 1},
		{`.a ~ .b { x: y }`, 1},
		{`.a + * { x: y }`, 1},
		{`.a+blockquote{x:y}`, 1},
		{`.a > blockquote { x: y }`, 0},
		{`/* iframe + blockquote { x: y } */ p { x: y }`, 0},
		{`h2 ~ [data-x] { x: y }`, 1},
	} {
		if got := siblingTargets(tc.css); len(got) != tc.want {
			t.Errorf("%q: %v, want %d", tc.css, got, tc.want)
		}
	}
}

// The fallback of a Threads embed hides by its own class, once the script has
// set it.
func TestCSSHidesRegisteredThreadsFallback(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/gobb.css")
	if err != nil {
		t.Fatal(err)
	}
	css := cssComment.ReplaceAllString(string(b), "")
	if !regexp.MustCompile(`\.post-body blockquote\.text-post-media-registered\s*\{\s*display:\s*none`).MatchString(css) {
		t.Error("no rule hides blockquote.text-post-media-registered")
	}
	if strings.Contains(css, "instagram-media") && strings.Contains(css, "display: none") &&
		regexp.MustCompile(`instagram-media[^{]*\{\s*display:\s*none`).MatchString(css) {
		t.Error("a rule hides an Instagram element; its script removes the fallback itself")
	}
}

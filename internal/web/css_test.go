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
	// cssAttr is an attribute selector. Its value may hold a + or a ~ (and ~=
	// is an operator), which is no combinator.
	cssAttr = regexp.MustCompile(`\[[^\]]*\]`)
	// cssNthArg is the An+B argument of an :nth-*() pseudo-class, up to an "of"
	// selector list. Its + is no combinator.
	cssNthArg = regexp.MustCompile(`(:nth-(?:last-)?(?:child|of-type)\()[\s0-9n+-]*(?:odd|even)?\s*`)
)

// siblingTargets returns each selector of css that has a sibling combinator,
// at the top level or inside a pseudo-class such as :is(), :not() or
// :nth-child(... of S), when the compound selector at its end can match a
// blockquote: the element is blockquote itself, or any element (a class, an
// id, an attribute or a pseudo-class with no element name, or *).
func siblingTargets(css string) []string {
	css = cssComment.ReplaceAllString(css, "")
	var out []string
	for _, m := range cssRule.FindAllStringSubmatch(css, -1) {
		for _, sel := range splitTopLevel(m[1]) {
			sel = strings.TrimSpace(sel)
			bare := cssNthArg.ReplaceAllString(cssAttr.ReplaceAllString(sel, "[]"), "$1")
			if !strings.ContainsAny(bare, "+~") {
				continue
			}
			last := subjectCompound(bare)
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

// splitTopLevel splits a selector list at the commas that are outside any
// parentheses, so ":is(a, b + c)" stays one selector.
func splitTopLevel(list string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(list); i++ {
		switch list[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, list[start:i])
				start = i + 1
			}
		}
	}
	return append(out, list[start:])
}

// subjectCompound returns the compound selector after the last combinator of
// sel that is outside any parentheses or brackets: the element that the rule
// styles.
func subjectCompound(sel string) string {
	depth := 0
	for i := len(sel) - 1; i >= 0; i-- {
		switch c := sel[i]; {
		case c == ')' || c == ']':
			depth++
		case c == '(' || c == '[':
			depth--
		case depth == 0 && strings.IndexByte(" \t\n>+~", c) >= 0:
			return sel[i+1:]
		}
	}
	return sel
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
		// A combinator inside a pseudo-class counts, and the subject is the
		// compound that holds the pseudo-class.
		{`.post-body :is(iframe + blockquote) { display: none }`, 1},
		{`.post-body :is(p, iframe + blockquote) { display: none }`, 1},
		{`.post-body p:is(.a + p) { x: y }`, 0},
		{`.post-body blockquote:not(.x ~ *) { display: none }`, 1},
		{`.post-body :not(iframe ~ blockquote) { x: y }`, 1},
		{`.post-body p:not(.x ~ *) { x: y }`, 0},
		{`.post-body blockquote:nth-child(1 of iframe + *) { display: none }`, 1},
		{`.post-body :nth-last-child(2n+1 of .x ~ *) { x: y }`, 1},
		{`.post-body li:nth-child(1 of .x + li) { x: y }`, 0},
		// The + of An+B and the ~ of an attribute are no combinators.
		{`.post-body :nth-child(2n+1) { x: y }`, 0},
		{`.post-body tr:nth-of-type(-n+3) { x: y }`, 0},
		{`.post-body [class~=x] { x: y }`, 0},
		{`.post-body [data-x="a+b"] { x: y }`, 0},
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

// The member's links in the header are hidden in print, and their focus ring
// uses the header text color: --link has little contrast on the header.
func TestCSSHeaderLinks(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/gobb.css")
	if err != nil {
		t.Fatal(err)
	}
	css := cssComment.ReplaceAllString(string(b), "")
	if !regexp.MustCompile(`@media print\s*\{[^@]*\.header-user[^{]*\{\s*display:\s*none`).MatchString(css) {
		t.Error("the print rule does not hide .header-user")
	}
	if !regexp.MustCompile(`\.header-user a:focus-visible,\s*\.brand:focus-visible\s*\{\s*outline-color:\s*var\(--header-text\)`).MatchString(css) {
		t.Error("the focus ring of .header-user a and .brand does not use --header-text")
	}
}

package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	inlineScript  = regexp.MustCompile(`(?is)<script\b[^>]*>`)
	scriptSrcAttr = regexp.MustCompile(`(?i)\bsrc\s*=`)
	styleAttr     = regexp.MustCompile(`(?i)\sstyle\s*=`)
	styleElem     = regexp.MustCompile(`(?i)<style\b`)
	eventAttr     = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// The CSP forbids inline script and inline style, so no template may hold any.
func TestTemplatesHaveNoInlineScriptOrStyle(t *testing.T) {
	n := 0
	err := fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		n++
		src := string(b)
		for _, tag := range inlineScript.FindAllString(src, -1) {
			if !scriptSrcAttr.MatchString(tag) {
				t.Errorf("%s: inline script %s", path, tag)
			}
		}
		// A script element with a src may still hold text; it must be empty.
		if strings.Contains(strings.ToLower(src), "</script>") {
			for _, part := range strings.Split(src, "</script>")[:strings.Count(strings.ToLower(src), "</script>")] {
				i := strings.LastIndex(strings.ToLower(part), "<script")
				if i >= 0 {
					if _, after, ok := strings.Cut(part[i:], ">"); ok && strings.TrimSpace(after) != "" {
						t.Errorf("%s: script element with a body: %q", path, after)
					}
				}
			}
		}
		if styleAttr.MatchString(src) {
			t.Errorf("%s: a style attribute", path)
		}
		if styleElem.MatchString(src) {
			t.Errorf("%s: a style element", path)
		}
		if m := eventAttr.FindString(src); m != "" {
			t.Errorf("%s: an event handler attribute %q", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no template scanned")
	}
}

package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	scriptElem = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	scriptOpen = regexp.MustCompile(`(?i)<script\b`)
	// A src attribute has white space before it, so data-src does not count.
	scriptSrcAttr = regexp.MustCompile(`(?i)\ssrc\s*=`)
	styleAttr     = regexp.MustCompile(`(?i)\sstyle\s*=`)
	styleElem     = regexp.MustCompile(`(?i)<style\b`)
	eventAttr     = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// cspProblems lists what in src the CSP would block: a script with no src or
// with a body, a style attribute, a style element and an event handler.
func cspProblems(src string) []string {
	var out []string
	scripts := scriptElem.FindAllStringSubmatch(src, -1)
	if len(scripts) != len(scriptOpen.FindAllString(src, -1)) {
		out = append(out, "a script element that the scan cannot parse")
	}
	for _, m := range scripts {
		if !scriptSrcAttr.MatchString(m[1]) {
			out = append(out, "inline script <script"+m[1]+">")
		}
		if strings.TrimSpace(m[2]) != "" {
			out = append(out, "a script element with a body: "+m[2])
		}
	}
	if styleAttr.MatchString(src) {
		out = append(out, "a style attribute")
	}
	if styleElem.MatchString(src) {
		out = append(out, "a style element")
	}
	if m := eventAttr.FindString(src); m != "" {
		out = append(out, "an event handler attribute "+m)
	}
	return out
}

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
		for _, p := range cspProblems(string(b)) {
			t.Errorf("%s: %s", path, p)
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

// The scan itself must catch what it claims to catch, in any letter case.
func TestCSPScanFindsProblems(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      int
	}{
		{"clean", `<script src="/a.js" defer></script><p class="x">`, 0},
		{"inline script", `<script>alert(1)</script>`, 2},
		{"upper case close", `<SCRIPT src="/a.js"></SCRIPT>`, 0},
		{"upper case inline", `<SCRIPT>x</SCRIPT>`, 2},
		{"data-src is not src", `<script data-src="/a.js"></script>`, 1},
		{"src with a body", `<script src="/a.js">x()</script>`, 1},
		{"unclosed", `<script src="/a.js">`, 1},
		{"style attribute", `<p STYLE="color:red">`, 1},
		{"style element", `<STYLE>p{}</STYLE>`, 1},
		{"event handler", `<a href="/" onClick="x()">`, 1},
	} {
		if got := cspProblems(tc.src); len(got) != tc.want {
			t.Errorf("%s: %d problems %v, want %d", tc.name, len(got), got, tc.want)
		}
	}
}

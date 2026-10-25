package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/bundletest"
)

func clockAt(y int, m time.Month, d int) Options {
	now := time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	return Options{Now: func() time.Time { return now }}
}

// factRows returns the dt labels of the facts list of a profile, in order.
func factRows(page string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<dt>([^<]*)</dt>`).FindAllStringSubmatch(page, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestProfileAbout(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), clockAt(2026, 10, 25))
	bob := signIn(t, h, "bob", testPassword)

	page := bodyOf(t, getAs(t, h, "/u/1", bob))
	for _, want := range []string{
		`<dt>Location</dt><dd>Lake Placid, NY</dd>`,
		`<dt>Age</dt><dd>42</dd>`,
		`<a href="https://example.com/alice" rel="nofollow ugc noopener">example.com/alice</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/u/1 lacks %q", want)
		}
	}
	// The five rows come after "Last visit", in this order, each when set.
	got := strings.Join(factRows(page), ",")
	if got != "Posts,Joined,Last visit,Age,Location,Website,Groups" {
		t.Errorf("alice rows = %s", got)
	}

	// Bob gave no birth year: no age. He has no About fields either.
	page = bodyOf(t, getAs(t, h, "/u/2", bob))
	if got := strings.Join(factRows(page), ","); got != "Posts,Joined,Last visit,Groups" {
		t.Errorf("bob rows = %s", got)
	}
	// Carol has none of the five.
	page = bodyOf(t, getAs(t, h, "/u/3", bob))
	for _, no := range []string{"<dt>Age</dt>", "<dt>Location</dt>", "<dt>Occupation</dt>", "<dt>Interests</dt>", "<dt>Website</dt>"} {
		if strings.Contains(page, no) {
			t.Errorf("/u/3 shows %q", no)
		}
	}
}

func TestProfileAboutEscapes(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), clockAt(2026, 10, 25))
	exec(t, pool, `update users set occupation = '<b>boss</b>', interests = 'a & b "c"' where id = 3`)
	bob := signIn(t, h, "bob", testPassword)
	page := bodyOf(t, getAs(t, h, "/u/3", bob))
	if strings.Contains(page, "<b>boss</b>") || !strings.Contains(page, "&lt;b&gt;boss&lt;/b&gt;") {
		t.Error("occupation is not escaped")
	}
	if !strings.Contains(pageText(page), `a & b "c"`) {
		t.Error("interests lacks its text")
	}
}

func TestProfileAge(t *testing.T) {
	for _, c := range []struct {
		y    int
		m    time.Month
		d    int
		want string
	}{
		{2026, 10, 25, "42"},
		{2027, 3, 13, "42"},
		{2027, 3, 14, "43"},
	} {
		h, _ := siteWithPool(t, bundletest.Small(), clockAt(c.y, c.m, c.d))
		bob := signIn(t, h, "bob", testPassword)
		page := bodyOf(t, getAs(t, h, "/u/1", bob))
		if want := `<dt>Age</dt><dd>` + c.want + `</dd>`; !strings.Contains(page, want) {
			t.Errorf("%d-%02d-%02d: lacks %q", c.y, c.m, c.d, want)
		}
	}
}

func TestIndexBirthdays(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), clockAt(2026, 3, 14))
	bob := signIn(t, h, "bob", testPassword)
	page := bodyOf(t, getAs(t, h, "/", bob))
	want := `Birthdays today: <a href="/u/1"><bdi>alice</bdi></a> (42)</p>`
	if !strings.Contains(page, want) {
		t.Errorf("index lacks %q", want)
	}

	// A guest has no account.view_profiles: no line.
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK || strings.Contains(bodyOf(t, rec), "Birthdays today") {
		t.Errorf("guest index: status %d, or the line shows", rec.Code)
	}

	// Two names: the one with no year has no age.
	exec(t, pool, `update users set birth_day = 14, birth_month = 3, birth_year = null where id = 2`)
	page = bodyOf(t, getAs(t, h, "/", bob))
	want = `Birthdays today: <a href="/u/1"><bdi>alice</bdi></a> (42), <a href="/u/2"><bdi>bob</bdi></a></p>`
	if !strings.Contains(page, want) {
		t.Errorf("index lacks %q", want)
	}

	// No birthday today: no line.
	h2, _ := siteWithPool(t, bundletest.Small(), clockAt(2026, 10, 25))
	bob2 := signIn(t, h2, "bob", testPassword)
	if strings.Contains(bodyOf(t, getAs(t, h2, "/", bob2)), "Birthdays today") {
		t.Error("the line shows on a day with no birthday")
	}
}

func TestIndexBirthdaysLeapDay(t *testing.T) {
	for _, c := range []struct {
		y    int
		m    time.Month
		d    int
		want bool
	}{
		{2027, 2, 28, true},
		{2027, 3, 1, false},
		{2028, 2, 28, false},
		{2028, 2, 29, true},
	} {
		h, pool := siteWithPool(t, bundletest.Small(), clockAt(c.y, c.m, c.d))
		exec(t, pool, `update users set birth_day = 29, birth_month = 2, birth_year = 2000 where id = 3`)
		bob := signIn(t, h, "bob", testPassword)
		page := bodyOf(t, getAs(t, h, "/", bob))
		if got := strings.Contains(page, `<bdi>carol</bdi>`); got != c.want {
			t.Errorf("%d-%02d-%02d: carol listed = %v, want %v", c.y, c.m, c.d, got, c.want)
		}
	}
}

func TestWebsiteText(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/alice":      "example.com/alice",
		"https://example.com":            "example.com",
		"http://example.com/a/b/":        "example.com/a/b/",
		"https://example.com/p?q=1#frag": "example.com/p",
		"not a url":                      "not a url",
	} {
		if got := websiteText(in); got != want {
			t.Errorf("websiteText(%q) = %q, want %q", in, got, want)
		}
	}
}

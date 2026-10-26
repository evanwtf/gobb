package web

import (
	"net/http"
	"strings"
	"testing"
)

// Ruling R46: a topic title, a post subject and a message subject print inside
// <bdi>, so a bidirectional control in one cannot reorder the text around it. The
// stored text here holds U+202E and U+2066, as an old row could. The <title>
// element stays as it is.
func TestSubjectsPrintInBdi(t *testing.T) {
	const (
		title   = "Topic \u202eevil\u2066 end"
		subject = "Subject \u2066odd\u202e end"
		pmSubj  = "Message \u202eodd\u2066 end"
	)
	h, pool := siteWithPool(t, searchSiteBoard(), Options{})
	exec(t, pool, `update topics set title = '`+title+`' where id = 1`)
	exec(t, pool, `update posts set subject = '`+subject+`' where id = 2`)
	exec(t, pool, `update pms set subject = '`+pmSubj+`' where id = 1`)
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)

	tests := []struct {
		name, target string
		cookies      []*http.Cookie
		want         string
	}{
		{"topic heading", "/t/1", bob, "<bdi>" + title + "</bdi>"},
		{"post subject", "/t/1", bob, "<bdi>" + subject + "</bdi>"},
		{"topic breadcrumb", "/t/1/reply", bob, `<li><a href="/t/1"><bdi>` + title + `</bdi></a></li>`},
		{"forum topic list", "/f/2", nil, "<bdi>" + title + "</bdi></a>"},
		{"index last post", "/", nil, `<bdi>` + title + `</bdi></a>
        <span class="last-meta">`},
		{"search hit", "/search?q=example", nil, "<bdi>" + title + "</bdi></a>"},
		{"message heading", "/messages/1", bob, "<h1><bdi>" + pmSubj + "</bdi></h1>"},
		{"inbox list", "/messages", bob, "<bdi>" + pmSubj + "</bdi></a>"},
		{"sent list", "/messages/sent", alice, "<bdi>" + pmSubj + "</bdi></a>"},
	}
	for _, tc := range tests {
		rec := getAs(t, h, tc.target, tc.cookies)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: GET %s = %d", tc.name, tc.target, rec.Code)
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Errorf("%s: GET %s lacks %q", tc.name, tc.target, tc.want)
		}
	}
	// The <title> element prints the text as it is, with no <bdi>.
	body := bodyOf(t, getAs(t, h, "/t/1", bob))
	if !strings.Contains(body, "<title>"+title+" - ") {
		t.Errorf("the <title> element changed:\n%.400s", body)
	}
}

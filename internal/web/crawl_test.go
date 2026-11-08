package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/markup"
)

// crawlBoard is the small board with what the sitemap must list and must leave
// out, all in forum 2, which guests may read:
//
//   - topic 7, a long topic of 30 posts: two pages;
//   - topics 20 to 74, one post each, so that forum 2 lists 58 topics in two pages;
//   - topic 9, a moved-topic marker to topic 1: listed in the forum, never as a
//     topic;
//   - topic 10, held for approval, and topic 11, deleted: hidden from guests.
//
// Forum 3 is for members only, with topic 2 and the marker topic 3. Forum 4,
// under the category, is one that guests may read but not list, with topic 90.
// Forum 2 also has the announcement 91 and the global topic 92, and topic 1 has
// a held reply that is newer than every post a guest sees.
func crawlBoard() *bundletest.Board {
	b := pagedBoard()
	for i := range 55 {
		id := int64(20 + i)
		b.Topics = append(b.Topics, bundleTopic(id, 2, fmt.Sprintf("Topic %d", id), "normal", at(2022, 1, 1, i, 0)))
		b.Posts = append(b.Posts, bundlePost(1000+id, id, 2, "bob", at(2022, 1, 1, i, 0), "A short post."))
	}
	marker := bundleTopic(9, 2, "Welcome to the example board", "normal", at(2019, 5, 2, 9, 0))
	marker.MovedTo = ptr(int64(1))
	held := bundleTopic(10, 2, "Held topic", "normal", at(2023, 1, 1, 0, 0))
	held.Visibility = "unapproved"
	gone := bundleTopic(11, 2, "Deleted topic", "normal", at(2023, 1, 2, 0, 0))
	gone.Visibility = "deleted"
	b.Topics = append(b.Topics, marker, held, gone)
	heldPost := bundlePost(2010, 10, 3, "carol", at(2023, 1, 1, 0, 0), "Held text.")
	heldPost.Visibility = "unapproved"
	gonePost := bundlePost(2011, 11, 1, "alice", at(2023, 1, 2, 0, 0), "Deleted text.")
	gonePost.Visibility = "deleted"
	b.Posts = append(b.Posts, heldPost, gonePost)

	b.Forums = append(b.Forums, bundleForum(4, 1, "forum", "Unlisted Room", 2))
	b.Grants = append(b.Grants, guestGrant(4, "forum.read"))
	b.Topics = append(b.Topics,
		bundleTopic(90, 4, "In the unlisted room", "normal", at(2021, 5, 1, 0, 0)),
		bundleTopic(91, 2, "An announcement", "announce", at(2021, 5, 2, 0, 0)),
		bundleTopic(92, 2, "A global topic", "global", at(2021, 5, 3, 0, 0)))
	lateHeld := bundlePost(2099, 1, 3, "carol", at(2021, 6, 1, 0, 0), "A late held reply.")
	lateHeld.Visibility = "unapproved"
	b.Posts = append(b.Posts,
		bundlePost(2090, 90, 1, "alice", at(2021, 5, 1, 0, 0), "Unlisted text."),
		bundlePost(2091, 91, 1, "alice", at(2021, 5, 2, 0, 0), "Announcement text."),
		bundlePost(2092, 92, 1, "alice", at(2021, 5, 3, 0, 0), "Global text."),
		lateHeld)
	return b
}

// crawlPublic is the public address of the crawl tests.
const crawlPublic = "https://board.example.org"

// wantSitemap is the set of guest-visible pages of crawlBoard, worked out from
// the fixture and not from the code: the index, the category 1, the two pages
// of forum 2, the unlisted forum 4, each approved topic of forums 2 and 4 that
// is not a marker, the announcement and the global topic. The pinned topics do
// not count toward the pages of forum 2.
func wantSitemap(base string) []string {
	u := func(p string) string { return crawlPublic + base + p }
	want := []string{u(""), u("f/1"), u("f/2"), u("f/2?page=2"), u("f/4"), u("t/1"), u("t/7"), u("t/7?page=2"),
		u("t/90"), u("t/91"), u("t/92")}
	for i := range 55 {
		want = append(want, u(fmt.Sprintf("t/%d", 20+i)))
	}
	slices.Sort(want)
	return want
}

// sitemapLocs reads the <loc> and <lastmod> of each <url> of a sitemap.
func sitemapLocs(t *testing.T, body string) (locs []string, mods map[string]string) {
	t.Helper()
	var set struct {
		XMLName xml.Name
		URLs    []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("the sitemap is not XML: %v\n%s", err, body)
	}
	if set.XMLName.Local != "urlset" || set.XMLName.Space != "http://www.sitemaps.org/schemas/sitemap/0.9" {
		t.Fatalf("root = %v, want the urlset of the sitemap protocol", set.XMLName)
	}
	mods = map[string]string{}
	for _, x := range set.URLs {
		locs = append(locs, x.Loc)
		mods[x.Loc] = x.LastMod
	}
	slices.Sort(locs)
	return locs, mods
}

func TestSitemapListsTheGuestPages(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		t.Run(base, func(t *testing.T) {
			h, _ := siteWithPool(t, crawlBoard(), Options{BasePath: base, PublicURL: crawlPublic + base})
			// A member asks too: the sitemap is the guest's all the same.
			for _, who := range []string{"guest", "alice"} {
				var rec = get(t, h, base+"sitemap.xml")
				if who == "alice" {
					rec = getAs(t, h, base+"sitemap.xml", signInAt(t, h, base, "alice"))
				}
				if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/xml; charset=utf-8" {
					t.Fatalf("%s: %d %q", who, rec.Code, rec.Header().Get("Content-Type"))
				}
				body := bodyOf(t, rec)
				locs, mods := sitemapLocs(t, body)
				if want := wantSitemap(base); !slices.Equal(locs, want) {
					t.Errorf("%s: sitemap =\n%v\nwant\n%v", who, locs, want)
				}
				// The members-only forum and its topics, the marker, the held
				// topic and the deleted one are absent; so is a page 1 address.
				for _, hidden := range []string{"t/2<", "t/3<", "t/9<", "t/10<", "t/11<", "f/3<", "?page=1<"} {
					if strings.Contains(body, hidden) {
						t.Errorf("%s: the sitemap lists %s", who, hidden)
					}
				}
				for loc, want := range map[string]string{
					"t/1":        "2020-02-01T11:00:00Z", // post 4: the held reply of 2021 does not count
					"t/90":       "2021-05-01T00:00:00Z",
					"t/92":       "2021-05-03T00:00:00Z",
					"t/7":        "2021-04-02T00:00:00Z", // post 25 ends page 1
					"t/7?page=2": "2021-04-02T05:00:00Z", // post 30
					"":           "2022-01-03T06:00:00Z", // the newest page of all: topic 74
				} {
					if got := mods[crawlPublic+base+loc]; got != want {
						t.Errorf("%s: lastmod of %q = %q, want %q", who, loc, got, want)
					}
				}
			}
			// The parts of a sitemap index do not exist while the sitemap fits.
			if rec := get(t, h, base+"sitemaps/1.xml"); rec.Code != http.StatusNotFound {
				t.Errorf("sitemaps/1.xml with one sitemap: %d, want 404", rec.Code)
			}
		})
	}
}

// Past sitemapMax URLs, /sitemap.xml is an index of numbered sitemaps, which
// together list the same pages.
func TestSitemapIndex(t *testing.T) {
	h, _ := siteWithPool(t, crawlBoard(), Options{BasePath: "/forum/", PublicURL: crawlPublic + "/forum/"})
	was := sitemapMax
	sitemapMax = 25
	t.Cleanup(func() { sitemapMax = was })
	body := bodyOf(t, get(t, h, "/forum/sitemap.xml"))
	var idx struct {
		XMLName  xml.Name
		Sitemaps []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"sitemap"`
	}
	if err := xml.Unmarshal([]byte(body), &idx); err != nil || idx.XMLName.Local != "sitemapindex" {
		t.Fatalf("not a sitemap index: %v\n%s", err, body)
	}
	want := wantSitemap("/forum/")
	if n := (len(want) + 24) / 25; len(idx.Sitemaps) != n {
		t.Fatalf("index lists %d sitemaps, want %d", len(idx.Sitemaps), n)
	}
	var all []string
	for i, sm := range idx.Sitemaps {
		wantLoc := fmt.Sprintf("%s/forum/sitemaps/%d.xml", crawlPublic, i+1)
		if sm.Loc != wantLoc || sm.LastMod == "" {
			t.Errorf("sitemap %d = %q %q, want %q with a lastmod", i+1, sm.Loc, sm.LastMod, wantLoc)
		}
		rec := get(t, h, strings.TrimPrefix(sm.Loc, crawlPublic))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", sm.Loc, rec.Code)
		}
		locs, _ := sitemapLocs(t, bodyOf(t, rec))
		if len(locs) > 25 {
			t.Errorf("%s holds %d URLs, more than 25", sm.Loc, len(locs))
		}
		all = append(all, locs...)
	}
	slices.Sort(all)
	if !slices.Equal(all, want) {
		t.Errorf("the parts list\n%v\nwant\n%v", all, want)
	}
	for _, p := range []string{"/forum/sitemaps/4.xml", "/forum/sitemaps/0.xml", "/forum/sitemaps/01.xml", "/forum/sitemaps/1", "/forum/sitemaps/x.xml"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
}

func TestRobotsText(t *testing.T) {
	const sitemap = "https://board.example.org/forum/sitemap.xml"
	all := robotsText(board.AICrawlersAll, "/forum/", sitemap)
	if want := "User-agent: *\nDisallow: /forum/search\n\nSitemap: " + sitemap + "\n"; all != want {
		t.Errorf("all =\n%s\nwant\n%s", all, want)
	}
	search := robotsText(board.AICrawlersSearch, "/forum/", sitemap)
	none := robotsText(board.AICrawlersNone, "/forum/", sitemap)
	blocks := func(body, token string) bool {
		return regexp.MustCompile(`(?m)^User-agent: ` + regexp.QuoteMeta(token) + `\n(User-agent: .*\n)*Disallow: /forum/\n`).MatchString(body)
	}
	for _, c := range aiCrawlers {
		if blocks(all, c.Token) {
			t.Errorf("all blocks %s", c.Token)
		}
		if blocks(search, c.Token) != c.Training {
			t.Errorf("search-and-answers: %s blocked = %v, want %v", c.Token, blocks(search, c.Token), c.Training)
		}
		if !blocks(none, c.Token) {
			t.Errorf("none does not block %s", c.Token)
		}
	}
	// Known names in each class, so that a table edit cannot flip one silently.
	for _, tok := range []string{"GPTBot", "ClaudeBot", "Google-Extended", "CCBot"} {
		if !blocks(search, tok) {
			t.Errorf("search-and-answers lets %s train", tok)
		}
	}
	for _, tok := range []string{"OAI-SearchBot", "Claude-SearchBot", "PerplexityBot"} {
		if blocks(search, tok) || !blocks(none, tok) {
			t.Errorf("%s: blocked by search-and-answers %v, by none %v", tok, blocks(search, tok), blocks(none, tok))
		}
	}
	for _, body := range []string{search, none} {
		if !strings.HasPrefix(body, "User-agent: *\nDisallow: /forum/search\n") || !strings.HasSuffix(body, "\nSitemap: "+sitemap+"\n") {
			t.Errorf("the search rule or the sitemap line is missing:\n%s", body)
		}
		if strings.Contains(body, "Applebot\n") || strings.Contains(body, "Googlebot") {
			t.Errorf("a policy blocks a web search crawler:\n%s", body)
		}
	}
	// Under a base path, a blocked crawler is kept out of the base path only: the
	// rest of the host is not the board's. At the root, that is the whole host.
	if strings.Contains(none, "Disallow: /\n") || !strings.Contains(none, "\nDisallow: /forum/\n") {
		t.Errorf("none under /forum/ does not disallow the base path alone:\n%s", none)
	}
	if root := robotsText(board.AICrawlersNone, "/", "https://board.example.org/sitemap.xml"); !strings.Contains(root, "User-agent: Perplexity-User\nDisallow: /\n") {
		t.Errorf("none at the root:\n%s", root)
	}
	seen := map[string]bool{}
	for _, c := range aiCrawlers {
		if seen[c.Token] || strings.ContainsAny(c.Token, " :\n") {
			t.Errorf("bad or repeated token %q", c.Token)
		}
		seen[c.Token] = true
	}
}

// robots.txt answers at the host root, from the setting, with the base path in
// its rules.
func TestRobotsRoute(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		h, pool := siteWithPool(t, bundletest.Small(), Options{BasePath: base})
		rec := get(t, h, "/robots.txt")
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s: /robots.txt = %d %q", base, rec.Code, rec.Header().Get("Content-Type"))
		}
		if want := robotsText(board.AICrawlersAll, base, testOrigin+base+"sitemap.xml"); body != want {
			t.Errorf("%s: /robots.txt =\n%s\nwant\n%s", base, body, want)
		}
		if base != "/" {
			if rec := get(t, h, base+"robots.txt"); rec.Code != http.StatusNotFound {
				t.Errorf("%srobots.txt = %d, want 404", base, rec.Code)
			}
		}
		exec(t, pool, `insert into settings (key, value) values ('ai_crawlers', '"none"')`)
		if body := bodyOf(t, get(t, h, "/robots.txt")); body != robotsText(board.AICrawlersNone, base, testOrigin+base+"sitemap.xml") {
			t.Errorf("%s: with none:\n%s", base, body)
		}
	}
	// The public URL gives the host of the sitemap line.
	h, _ := siteWithPool(t, bundletest.Small(), Options{PublicURL: crawlPublic + "/"})
	if body := bodyOf(t, get(t, h, "/robots.txt")); !strings.HasSuffix(body, "Sitemap: "+crawlPublic+"/sitemap.xml\n") {
		t.Errorf("with a public URL:\n%s", body)
	}
}

// The admin settings page saves the policy, and robots.txt follows it.
func TestAdminSavesTheCrawlerPolicy(t *testing.T) {
	e := newSettingsEnv(t, Options{})
	body := bodyOf(t, getAs(t, e.h, "/admin/settings", e.alice))
	for _, want := range []string{`name="ai_crawlers" id="ai-crawlers-all" value="all" checked`, `value="search-and-answers"`, `value="none"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the settings page lacks %s", want)
		}
	}
	rec := e.save(t, e.alice, withFields(e.form(t), "ai_crawlers", "search-and-answers"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d", rec.Code)
	}
	if robots := bodyOf(t, get(t, e.h, "/robots.txt")); !strings.Contains(robots, "User-agent: GPTBot\n") || strings.Contains(robots, "User-agent: OAI-SearchBot\n") {
		t.Errorf("robots.txt after the save:\n%s", robots)
	}
	rec = e.save(t, e.alice, withFields(e.form(t), "ai_crawlers", "everything"))
	body = bodyOf(t, rec)
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, `id="ai-crawlers-error" role="alert">Pick one of the three crawler policies.`) {
		t.Errorf("a bad policy: %d", rec.Code)
	}
	if !strings.Contains(body, `value="search-and-answers" checked`) {
		t.Error("the refused form does not keep the stored policy")
	}
}

// canonicalRE finds each canonical link.
var canonicalRE = regexp.MustCompile(`<link rel="canonical" href="([^"]*)">`)

// canonicals returns the canonical links of a page.
func canonicals(body string) []string {
	var out []string
	for _, m := range canonicalRE.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestCanonicalLink(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		h, _ := siteWithPool(t, crawlBoard(), Options{BasePath: base, PublicURL: crawlPublic + base})
		alice := signInAt(t, h, base, "alice")
		for _, tc := range []struct{ target, want string }{
			{"", ""},
			{"?scheme=slate-deep", ""},
			{"?page=2", ""},
			{"?page=999999", ""},
			{"f/1", "f/1"},
			{"f/2", "f/2"},
			{"f/2?page=1", "f/2"},
			{"f/2?page=2&utm_source=feed&scheme=slate", "f/2?page=2"},
			{"f/%32", "f/2"},
			{"f/4", "f/4"},
			{"t/1", "t/1"},
			{"t/%31", "t/1"},
			{"t/1?page=1&held=1", "t/1"},
			{"t/7?page=2", "t/7?page=2"},
			{"t/%37?page=2&sort=x", "t/7?page=2"},
			{"team", "team"},
			{"team?page=2", "team"},
			{"pages/about", "pages/about"},
			{"pages/about?page=2", "pages/about"},
		} {
			for _, who := range []string{"guest", "alice"} {
				if who == "alice" && tc.want == "f/4" {
					continue // forum 4 is for guests only in this fixture
				}
				rec := get(t, h, base+tc.target)
				if who == "alice" {
					rec = getAs(t, h, base+tc.target, alice)
				}
				if rec.Code != http.StatusOK {
					t.Errorf("%s %s%s: %d", who, base, tc.target, rec.Code)
					continue
				}
				body := bodyOf(t, rec)
				got := canonicals(body)
				want := crawlPublic + base + tc.want
				if len(got) != 1 || got[0] != want {
					t.Errorf("%s %s%s: canonical %v, want [%s]", who, base, tc.target, got, want)
				}
				if url, n := metaContent(body, "property", "og:url"); who == "guest" && (n != 1 || url != want) {
					t.Errorf("guest %s%s: og:url %q (%d), want %q", base, tc.target, url, n, want)
				}
			}
		}
		// A member's page of a members-only forum has its own canonical link too.
		if got := canonicals(bodyOf(t, getAs(t, h, base+"t/2", alice))); len(got) != 1 || got[0] != crawlPublic+base+"t/2" {
			t.Errorf("members-only topic: canonical %v", got)
		}
	}
}

// With no public URL, a page has no canonical link and no og:url: the Host
// header of a request must not choose them. The sitemap and robots.txt take the
// host of the request, and the start logs one warning.
func TestNoPublicURL(t *testing.T) {
	b := crawlBoard()
	pool, dataDir := boardtest.Load(t, b)
	logs := captureLogs(t)
	h, err := New(t.Context(), pool, Options{DataDir: dataDir, LoginFloor: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var warns int
	for _, l := range logs.lines() {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, "no public URL") {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("%d warnings about the public URL, want 1:\n%s", warns, strings.Join(logs.lines(), "\n"))
	}
	for _, p := range []string{"/", "/f/2", "/t/1", "/team"} {
		body := bodyOf(t, get(t, h, p))
		if strings.Contains(body, `rel="canonical"`) || strings.Contains(body, `property="og:url"`) {
			t.Errorf("%s has a canonical link or og:url with no public URL", p)
		}
		if _, n := metaContent(body, "property", "og:title"); n != 1 {
			t.Errorf("%s lost its og:title", p)
		}
	}
	locs, _ := sitemapLocs(t, bodyOf(t, get(t, h, "/sitemap.xml")))
	if len(locs) == 0 || !strings.HasPrefix(locs[0], testOrigin+"/") {
		t.Errorf("sitemap locs %v, want the host of the request", locs)
	}
	if robots := bodyOf(t, get(t, h, "/robots.txt")); !strings.Contains(robots, "Sitemap: "+testOrigin+"/sitemap.xml\n") {
		t.Errorf("robots.txt:\n%s", robots)
	}
	// With a public URL, the start logs no such warning.
	pool2, dir2 := boardtest.Load(t, bundletest.Small())
	logs = captureLogs(t)
	if _, err := New(t.Context(), pool2, Options{DataDir: dir2, LoginFloor: time.Millisecond, PublicURL: crawlPublic + "/"}); err != nil {
		t.Fatal(err)
	}
	for _, l := range logs.lines() {
		if strings.Contains(l, "no public URL") {
			t.Errorf("a warning with a public URL set: %s", l)
		}
	}
}

// Each <loc> of the sitemap opens as a guest with status 200, and the page names
// the same address as its canonical link.
func TestSitemapRoundTrip(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		h, _ := siteWithPool(t, crawlBoard(), Options{BasePath: base, PublicURL: crawlPublic + base})
		locs, _ := sitemapLocs(t, bodyOf(t, get(t, h, base+"sitemap.xml")))
		if len(locs) < 10 {
			t.Fatalf("only %d locs", len(locs))
		}
		for _, loc := range locs {
			rec := get(t, h, strings.TrimPrefix(loc, crawlPublic))
			if rec.Code != http.StatusOK {
				t.Errorf("%s: %d", loc, rec.Code)
				continue
			}
			if got := canonicals(bodyOf(t, rec)); len(got) != 1 || got[0] != loc {
				t.Errorf("%s: canonical %v", loc, got)
			}
		}
	}
}

// The sitemap is built once in sitemapTTL, whatever asks for it: the index,
// its parts and a part past the end. After the TTL the next request builds it
// again.
func TestSitemapCache(t *testing.T) {
	now := at(2026, 1, 1, 0, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	h, _ := siteWithPool(t, crawlBoard(), Options{PublicURL: crawlPublic + "/", Now: clock})
	was := sitemapMax
	sitemapMax = 25
	t.Cleanup(func() { sitemapMax = was })

	start := sitemapBuilds.Load()
	builds := func() int64 { return sitemapBuilds.Load() - start }
	first := bodyOf(t, get(t, h, "/sitemap.xml"))
	if builds() != 1 {
		t.Fatalf("builds after the first request = %d, want 1", builds())
	}
	if again := bodyOf(t, get(t, h, "/sitemap.xml")); again != first {
		t.Error("a second request inside the TTL gave another sitemap")
	}
	var all []string
	for n := 1; n <= 3; n++ {
		rec := get(t, h, fmt.Sprintf("/sitemaps/%d.xml", n))
		if rec.Code != http.StatusOK {
			t.Fatalf("part %d: %d", n, rec.Code)
		}
		locs, _ := sitemapLocs(t, bodyOf(t, rec))
		all = append(all, locs...)
	}
	slices.Sort(all)
	if !slices.Equal(all, wantSitemap("/")) {
		t.Errorf("the parts of one build list\n%v\nwant\n%v", all, wantSitemap("/"))
	}
	for _, p := range []string{"/sitemaps/4.xml", "/sitemaps/99.xml", "/sitemaps/x.xml"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", p, rec.Code)
		}
	}
	if builds() != 1 {
		t.Errorf("builds inside the TTL = %d, want 1", builds())
	}

	// Many requests at once share one build.
	mu.Lock()
	now = now.Add(sitemapTTL)
	mu.Unlock()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if rec := get(t, h, "/sitemap.xml"); rec.Code != http.StatusOK {
				t.Errorf("concurrent request: %d", rec.Code)
			}
		})
	}
	wg.Wait()
	if builds() != 2 {
		t.Errorf("builds after the TTL, with 8 requests at once = %d, want 2", builds())
	}
}

// The thin pages and the error pages keep the robots meta, and so carry no
// canonical link and no description.
func TestNoindexPagesHaveNoCanonical(t *testing.T) {
	b := crawlBoard()
	allowGroup(b, 1, 0, "account.search")
	allowGroup(b, 1, 2, "forum.search")
	h, _ := siteWithPool(t, b, Options{})
	alice := signIn(t, h, "alice", testPassword)
	const meta = `<meta name="robots" content="noindex">`
	for _, c := range []struct {
		target string
		member bool
		code   int
	}{
		{"/search", false, 200},
		{"/search?q=example", false, 200},
		{"/login", false, 200},
		{"/members", true, 200},
		{"/members?page=2", true, 404},
		{"/f/2?page=3", false, 404},
		{"/t/1?page=2", false, 404},
		{"/t/2", false, 404},
		{"/f/3", false, 404},
		{"/t/10", false, 404},
	} {
		rec := get(t, h, c.target)
		if c.member {
			rec = getAs(t, h, c.target, alice)
		}
		body := bodyOf(t, rec)
		if rec.Code != c.code {
			t.Errorf("%s: %d, want %d", c.target, rec.Code, c.code)
		}
		if !strings.Contains(body, meta) {
			t.Errorf("%s lacks the robots meta", c.target)
		}
		if strings.Contains(body, `rel="canonical"`) || strings.Contains(body, `property="og:`) || strings.Contains(body, `name="description"`) {
			t.Errorf("%s has a canonical link or page metadata", c.target)
		}
	}
	if robots := bodyOf(t, get(t, h, "/robots.txt")); !strings.Contains(robots, "User-agent: *\nDisallow: /search\n") {
		t.Errorf("robots.txt lacks the search rule:\n%s", robots)
	}
}

// metaContent returns the content of the meta element with the attribute
// attr="name", and how many there are.
func metaContent(body, attr, name string) (string, int) {
	re := regexp.MustCompile(`<meta ` + attr + `="` + regexp.QuoteMeta(name) + `" content="([^"]*)">`)
	all := re.FindAllStringSubmatch(body, -1)
	if len(all) == 0 {
		return "", 0
	}
	return all[0][1], len(all)
}

func TestPageMetadata(t *testing.T) {
	b := crawlBoard()
	b.Topics = append(b.Topics, bundleTopic(80, 2, "Tom & <Jerry>", "normal", at(2024, 1, 1, 0, 0)))
	b.Posts = append(b.Posts,
		bundlePost(3080, 80, 1, "alice", at(2024, 1, 1, 0, 0), "> A quote that the excerpt skips.\n\nTom & **Jerry** <b>chase</b> each other.\n\nA [link](https://example.com/x) and `code`."),
		bundlePost(3081, 80, 2, "bob", at(2024, 1, 1, 1, 0), "A reply."))
	h, _ := siteWithPool(t, b, Options{PublicURL: crawlPublic + "/"})

	for _, c := range []struct {
		target, title, desc, typ string
	}{
		{"/", "Example Board", "A board for examples", "website"},
		{"/f/2", "Main Discussion", "The main forum", "website"},
		{"/f/2?page=2", "Main Discussion", "The main forum", "website"},
		{"/f/1", "General", "Talk about anything", "website"},
		{"/t/1", "Welcome to the example board", "Welcome to the example board.", "article"},
		{"/t/7?page=2", "A long topic", "Post number.", "article"},
		{"/team", "Team", "A board for examples", "website"},
		{"/pages/about", "About", "A board for examples", "website"},
		{"/t/80", "Tom &amp; &lt;Jerry&gt;", "Tom &amp; Jerry &lt;b&gt;chase&lt;/b&gt; each other. A link and code.", "article"},
	} {
		rec := get(t, h, c.target)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d", c.target, rec.Code)
			continue
		}
		u := crawlPublic + c.target
		u = strings.Replace(u, "?page=1", "", 1)
		for _, m := range []struct{ attr, name, want string }{
			{"name", "description", c.desc},
			{"property", "og:title", c.title},
			{"property", "og:description", c.desc},
			{"property", "og:url", u},
			{"property", "og:type", c.typ},
			{"property", "og:site_name", "Example Board"},
		} {
			if got, n := metaContent(body, m.attr, m.name); n != 1 || got != m.want {
				t.Errorf("%s: %s = %q (%d of them), want %q", c.target, m.name, got, n, m.want)
			}
		}
	}

	// A member sees no description and no Open Graph tags on any page: a page
	// that only a member can open must not describe itself, and the guest's
	// page is the one that crawlers and link previews read.
	alice := signIn(t, h, "alice", testPassword)
	for _, p := range []string{"/", "/f/2", "/t/1", "/t/2", "/f/3", "/t/10"} {
		rec := getAs(t, h, p, alice)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusOK {
			t.Errorf("member %s: %d", p, rec.Code)
		}
		if strings.Contains(body, `property="og:`) || strings.Contains(body, `name="description"`) {
			t.Errorf("member %s has page metadata", p)
		}
	}
}

// A guest moderator sees a held topic; the page still describes nothing.
func TestHeldTopicHasNoMetadata(t *testing.T) {
	b := crawlBoard()
	allowGroup(b, 1, 2, "mod.approve")
	h, _ := siteWithPool(t, b, Options{})
	rec := get(t, h, "/t/10")
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("/t/10 for a guest who may approve: %d", rec.Code)
	}
	if strings.Contains(body, `property="og:`) || strings.Contains(body, `name="description"`) {
		t.Error("a held topic has page metadata")
	}
}

func TestExcerpt(t *testing.T) {
	long := strings.Repeat("word ", 40) // 200 characters
	got := excerpt(long, descriptionLimit)
	if !strings.HasSuffix(got, "word…") || len([]rune(got)) > descriptionLimit+1 {
		t.Errorf("excerpt = %q (%d characters)", got, len([]rune(got)))
	}
	for _, c := range []struct{ in, want string }{
		{"short text", "short text"},
		{"  spaced \n\t out  ", "spaced out"},
		{"a\x00b c", "a b c"},
		{strings.Repeat("é", 154) + " end", strings.Repeat("é", 154) + "…"},
		{strings.Repeat("x", 200), strings.Repeat("x", 155) + "…"},
		{"evil\u202etxt.exe", "eviltxt.exe"},
		{"a \u2066isolated\u2069 b\u200f c", "a isolated b c"},
		{"Ends with a comma, " + strings.Repeat("y", 140) + " more words", "Ends with a comma…"},
	} {
		if got := excerpt(c.in, descriptionLimit); got != c.want {
			t.Errorf("excerpt(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The cut falls at a word boundary: every word of the result is a whole word
	// of the input.
	in := "The quick brown fox jumps over the lazy dog. " + strings.Repeat("Pack my box with five dozen liquor jugs. ", 5)
	got = strings.TrimSuffix(excerpt(in, descriptionLimit), "…")
	if !strings.HasPrefix(in, got+" ") {
		t.Errorf("the cut %q is not at a word boundary of the input", got)
	}
}

func TestTreeText(t *testing.T) {
	tree := markup.Parse("Hello **bold** and *it*.\n\n> quoted text\n\n- one\n- two\n\n```\ncode block\n```\n\n||secret|| end", markup.Options{Markdown: true})
	got := excerpt(treeText(tree), descriptionLimit)
	if strings.Contains(got, "quoted") || strings.Contains(got, "code block") || strings.Contains(got, "*") || strings.Contains(got, "<") {
		t.Errorf("treeText = %q", got)
	}
	for _, want := range []string{"Hello bold and it.", "one two"} {
		if !strings.Contains(got, want) {
			t.Errorf("treeText = %q, want it to hold %q", got, want)
		}
	}
}

// The new routes refuse a POST, like every GET page.
func TestCrawlRoutesRefusePost(t *testing.T) {
	h, _ := smallSite(t)
	for _, p := range []string{"/robots.txt", "/sitemap.xml", "/sitemaps/1.xml"} {
		if rec := do(t, h, http.MethodPost, p); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST %s: %d", p, rec.Code)
		}
	}
}

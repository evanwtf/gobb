package web

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/markup"
)

// The pages for crawlers: /robots.txt at the host root, the sitemap under the
// base path, and the canonical link, the meta description and the Open Graph
// tags of a page. A crawler is a guest, so each of them shows what a guest can
// see and nothing more.

// aiCrawler is one AI crawler that robots.txt can name.
type aiCrawler struct {
	// Token is the user-agent token for robots.txt, as its operator writes it.
	Token string
	// Training is true for a crawler whose operator says that it collects text
	// to train AI models. The policy "search-and-answers" blocks these.
	Training bool
}

// aiCrawlers are the AI crawlers that robots.txt names. Each entry comes from
// the operator's own public page about its crawlers. The policy
// "search-and-answers" blocks the entries with Training; the policy "none"
// blocks every entry. A general search crawler (one that also feeds a web
// search, such as Applebot) is not here: "none" keeps the board in search.
var aiCrawlers = []aiCrawler{
	// OpenAI: GPTBot collects content that may train its generative models.
	{Token: "GPTBot", Training: true},
	// OpenAI: OAI-SearchBot finds pages for the search of ChatGPT.
	{Token: "OAI-SearchBot"},
	// OpenAI: ChatGPT-User fetches a page when a user of ChatGPT asks for it.
	{Token: "ChatGPT-User"},
	// Anthropic: ClaudeBot collects content that may train its models.
	{Token: "ClaudeBot", Training: true},
	// Anthropic: Claude-SearchBot indexes pages for the search results of Claude.
	{Token: "Claude-SearchBot"},
	// Anthropic: Claude-User fetches a page when a user of Claude asks for it.
	{Token: "Claude-User"},
	// Google: Google-Extended is a control token with no crawler of its own. It
	// says whether content that Google crawls may train Gemini models and ground
	// their answers. It does not change Google Search.
	{Token: "Google-Extended", Training: true},
	// Apple: Applebot-Extended is a control token with no crawler of its own. It
	// says whether content that Applebot crawls may train Apple's foundation
	// models. It does not change the search results of Apple.
	{Token: "Applebot-Extended", Training: true},
	// Common Crawl: CCBot builds the open Common Crawl archive, which others
	// reuse, model training included.
	{Token: "CCBot", Training: true},
	// Meta: meta-externalagent crawls to train foundation AI models and to index
	// content for its products.
	{Token: "meta-externalagent", Training: true},
	// Meta: meta-webindexer crawls for the search results of Meta AI.
	{Token: "meta-webindexer"},
	// Meta: meta-externalfetcher fetches a page when a user of Meta AI asks for it.
	{Token: "meta-externalfetcher"},
	// Amazon: Amazonbot crawls to improve Amazon products, and its content may
	// train Amazon AI models.
	{Token: "Amazonbot", Training: true},
	// Amazon: Amzn-SearchBot crawls for search in Amazon products, such as Alexa.
	{Token: "Amzn-SearchBot"},
	// Amazon: Amzn-User fetches a page for a user action, such as an Alexa answer.
	{Token: "Amzn-User"},
	// Perplexity: PerplexityBot finds pages for the search results of Perplexity.
	{Token: "PerplexityBot"},
	// Perplexity: Perplexity-User fetches a page when a user of Perplexity asks.
	{Token: "Perplexity-User"},
}

// blockedCrawlers returns the tokens that policy blocks, in table order.
func blockedCrawlers(policy string) []string {
	var out []string
	for _, c := range aiCrawlers {
		switch {
		case policy == board.AICrawlersNone,
			policy == board.AICrawlersSearch && c.Training:
			out = append(out, c.Token)
		}
	}
	return out
}

// robotsText returns the body of robots.txt for the AI crawler policy, the base
// path and the absolute address of the sitemap. Every crawler skips the search
// pages: each one costs a query, they are endless, and the sitemap lists every
// page that they lead to. The pages that a guest cannot see need no rule: a
// crawler is a guest.
func robotsText(policy, base, sitemap string) string {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	b.WriteString("Disallow: " + base + "search\n")
	if blocked := blockedCrawlers(policy); len(blocked) > 0 {
		b.WriteString("\n")
		for _, t := range blocked {
			b.WriteString("User-agent: " + t + "\n")
		}
		// Under a base path, the site owns that path and nothing else on the host.
		b.WriteString("Disallow: " + base + "\n")
	}
	b.WriteString("\nSitemap: " + sitemap + "\n")
	return b.String()
}

// robots serves GET /robots.txt at the host root. A board with no settings yet
// has the default policy.
func (s *site) robots(w http.ResponseWriter, r *http.Request) {
	policy := board.AICrawlersAll
	switch set, err := board.LoadSettings(r.Context(), s.pool); {
	case err == nil:
		policy = set.AICrawlers
	case !errors.Is(err, board.ErrNoSettings):
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(robotsText(policy, s.base, s.absolute(r, s.path("sitemap.xml")))))
}

// origin returns the scheme and host of the site, with no path: the ones of
// Options.PublicURL (or of the email address of the site) when it is set, and
// otherwise the ones of the request. A request takes https when it came over
// TLS or the cookies are Secure, since a Secure cookie needs https.
func (s *site) origin(r *http.Request) string {
	if s.publicOrigin != "" {
		return s.publicOrigin
	}
	scheme := "http"
	if r.TLS != nil || s.opt.CookieSecure {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// absolute returns the absolute URL of the site path p.
func (s *site) absolute(r *http.Request, p string) string { return s.origin(r) + p }

// publicOrigin checks the public URL of the options against the base path and
// returns its scheme and host. An empty URL gives the empty string.
func publicOrigin(raw, base string, strict bool) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("web: PublicURL must be an http or https URL with a host and no query")
	}
	if strict && normalizeBase(u.Path) != base {
		return "", fmt.Errorf("web: the path of PublicURL must be the base path %s", base)
	}
	return u.Scheme + "://" + u.Host, nil
}

// pageMeta is the description and the Open Graph tags of a page that a guest can
// see.
type pageMeta struct {
	Title, Description, Type, URL, SiteName string
}

// descriptionLimit is the length, in characters, of a meta description.
const descriptionLimit = 155

// excerpt returns s as one line of plain text of at most limit characters. A
// longer text is cut at the last space before the limit and ends with "…". It
// drops the bidirectional controls, which could turn the text around in a
// search result or a link preview.
func excerpt(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Bidi_Control, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	cut := string(runes[:limit])
	if i := strings.LastIndex(cut, " "); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRightFunc(cut, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) + "…"
}

// plainSkip are the elements of a post tree whose text the description leaves
// out: quoted text is somebody else's, a spoiler is hidden, a code block is
// not prose, and an image, a smiley or an embed has no text of its own.
var plainSkip = map[string]bool{
	"quote": true, "spoiler": true, "pre": true, "img": true, "smiley": true,
	"youtube": true, "vimeo": true, "tiktok": true, "spotify": true, "soundcloud": true,
	"tweet": true, "instagram": true, "reddit": true, "threads": true,
}

// plainBreak are the elements that end a run of text: the description puts a
// space between two of them.
var plainBreak = map[string]bool{"p": true, "br": true, "hr": true, "h": true, "li": true, "list": true}

// treeText returns the text of a post tree with no markup. The text nodes hold
// the text itself, not HTML, so nothing needs decoding; the template escapes it.
func treeText(n *markup.Node) string {
	var b strings.Builder
	var walk func(n *markup.Node, depth int)
	walk = func(n *markup.Node, depth int) {
		if n == nil || depth > markup.MaxDepth+1 {
			return
		}
		switch n.Kind {
		case markup.Text:
			b.WriteString(n.Text)
			return
		case markup.Element:
			if plainSkip[n.Name] {
				b.WriteByte(' ')
				return
			}
		}
		brk := n.Kind == markup.Element && plainBreak[n.Name]
		if brk {
			b.WriteByte(' ')
		}
		for _, c := range n.Children {
			walk(c, depth+1)
		}
		if brk {
			b.WriteByte(' ')
		}
	}
	walk(n, 0)
	return b.String()
}

// sitemapMax is the most URLs in one sitemap file, the limit of the sitemap
// protocol. A test makes it small.
var sitemapMax = 50_000

// sitemapURL is one <url> of a sitemap, and sitemapRef one <sitemap> of a
// sitemap index.
type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name     `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapIndex struct {
	XMLName  xml.Name     `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 sitemapindex"`
	Sitemaps []sitemapURL `xml:"sitemap"`
}

// lastmod formats t for a sitemap, in UTC. The zero time gives "".
func lastmod(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// sitemapTTL is how long a built sitemap serves before the next request
// builds it again. The sitemap reads every visible post of the board, so a
// crawler that asks for it often must not cost a build each time.
const sitemapTTL = 15 * time.Minute

// sitemapKeys is the most origins that the cache keeps at once. With no public
// URL, the origin comes from the Host header of each request, and a client
// must not grow the cache without end.
const sitemapKeys = 8

// sitemapBuilds counts the builds of the sitemap. A test reads it.
var sitemapBuilds atomic.Int64

// sitemapBuild is one build of the sitemap for one origin. done closes when
// the build ends; then urls and mods, or err, hold the result.
type sitemapBuild struct {
	done chan struct{}
	at   time.Time
	urls []sitemapURL
	mods []time.Time
	err  error
}

// sitemapCache holds the newest build of the sitemap for each origin.
type sitemapCache struct {
	mu     sync.Mutex
	builds map[string]*sitemapBuild
}

// sitemapFor returns the sitemap for the origin of r: the cached build while it
// is younger than sitemapTTL, and otherwise a new one. One request builds at a
// time for each origin; the others wait for its result. A failed build is not
// kept, so the next request tries again.
func (s *site) sitemapFor(r *http.Request) (*sitemapBuild, error) {
	origin := s.origin(r)
	now := s.opt.Now()
	c := &s.sitemaps
	c.mu.Lock()
	b := c.builds[origin]
	fresh := false
	if b != nil {
		select {
		case <-b.done:
			fresh = b.err == nil && now.Sub(b.at) < sitemapTTL
		default:
			fresh = true // in flight: wait for it
		}
	}
	if !fresh {
		if c.builds == nil || len(c.builds) >= sitemapKeys {
			c.builds = map[string]*sitemapBuild{}
		}
		b = &sitemapBuild{done: make(chan struct{}), at: now}
		c.builds[origin] = b
		c.mu.Unlock()
		// The build does not end with the request that started it: other
		// requests wait for it.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), requestTimeout)
		b.urls, b.mods, b.err = s.buildSitemap(ctx, origin)
		cancel()
		close(b.done)
	} else {
		c.mu.Unlock()
	}
	select {
	case <-b.done:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if b.err != nil {
		return nil, b.err
	}
	return b, nil
}

// buildSitemap returns the URLs of the sitemap, with origin in front of each
// one, and the time of each: the pages that a guest can open on the index, the
// forums and the topics.
func (s *site) buildSitemap(ctx context.Context, origin string) ([]sitemapURL, []time.Time, error) {
	sitemapBuilds.Add(1)
	set, err := board.LoadSettings(ctx, s.pool)
	if err != nil {
		return nil, nil, err
	}
	guest, err := board.Guest(ctx, s.pool, set)
	if err != nil {
		return nil, nil, err
	}
	pages, err := board.Sitemap(ctx, s.pool, guest)
	if err != nil {
		return nil, nil, err
	}
	urls := make([]sitemapURL, len(pages))
	mods := make([]time.Time, len(pages))
	for i, p := range pages {
		var path string
		switch p.Kind {
		case "index":
			path = s.path()
		case "forum":
			path = pageHref(s.path("f", strconv.FormatInt(p.ID, 10)), p.Page)
		default:
			path = pageHref(s.path("t", strconv.FormatInt(p.ID, 10)), p.Page)
		}
		urls[i] = sitemapURL{Loc: origin + path, LastMod: lastmod(p.Mod)}
		mods[i] = p.Mod
	}
	return urls, mods, nil
}

// writeXML writes v as an XML document.
func (s *site) writeXML(w http.ResponseWriter, r *http.Request, v any) {
	b, err := xml.Marshal(v)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(b)
}

// sitemap serves GET /sitemap.xml. Up to sitemapMax URLs it is the sitemap
// itself. Past that it is a sitemap index of the files /sitemaps/1.xml,
// /sitemaps/2.xml and so on, each of sitemapMax URLs at most. It reads no
// session: every request gets the guest's view.
func (s *site) sitemap(w http.ResponseWriter, r *http.Request) {
	b, err := s.sitemapFor(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	urls, mods := b.urls, b.mods
	if len(urls) <= sitemapMax {
		s.writeXML(w, r, urlSet{URLs: urls})
		return
	}
	var idx sitemapIndex
	for i := 0; i*sitemapMax < len(urls); i++ {
		var newest time.Time
		for _, m := range mods[i*sitemapMax : min((i+1)*sitemapMax, len(urls))] {
			if m.After(newest) {
				newest = m
			}
		}
		loc := s.absolute(r, s.path("sitemaps", strconv.Itoa(i+1)+".xml"))
		idx.Sitemaps = append(idx.Sitemaps, sitemapURL{Loc: loc, LastMod: lastmod(newest)})
	}
	s.writeXML(w, r, idx)
}

// sitemapPart serves GET /sitemaps/{n}.xml, one file of a sitemap index, from
// the same cached build as the index. A number past the last file, or any file
// while the sitemap fits in one, is a 404.
func (s *site) sitemapPart(w http.ResponseWriter, r *http.Request) {
	num, ok := strings.CutSuffix(r.PathValue("name"), ".xml")
	n, valid := parseID(num)
	if !ok || !valid {
		s.notFound(w, r)
		return
	}
	b, err := s.sitemapFor(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	urls := b.urls
	if len(urls) <= sitemapMax || n > int64(len(urls)) || (int(n)-1)*sitemapMax >= len(urls) {
		s.notFound(w, r)
		return
	}
	start := (int(n) - 1) * sitemapMax
	s.writeXML(w, r, urlSet{URLs: urls[start:min(start+sitemapMax, len(urls))]})
}

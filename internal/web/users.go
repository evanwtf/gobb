package web

import (
	"cmp"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/markup"
)

// profileData is the data of profile.html.
type profileData struct {
	board.ProfilePage
	// SigHTML comes from markup.Render, which escapes everything.
	SigHTML template.HTML
	// WebsiteText is the text of the website link: the host and the path, with
	// no scheme.
	WebsiteText string
	// FindHref is the link to the posts of the member, or empty for a viewer
	// without account.search, whom search would refuse.
	FindHref string
	// MessageHref is the link to the compose form for this member, or empty when
	// the viewer may not message the member.
	MessageHref string
}

// websiteText returns the host and the path of a website address, without the
// scheme, for the text of a link. The query and the fragment stay out. Format
// characters (Unicode category Cf, such as the bidirectional controls) stay out
// too, because a decoded path could use them to reorder the text. An address that
// does not parse shows as it is, without them.
func websiteText(raw string) string {
	text := raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		text = u.Host + u.Path
	}
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
}

// denied answers 403 for a page that the viewer may not see: a page that needs
// account.view_profiles, or a forum that the viewer may list but not read. A
// guest gets a link to sign in that comes back to the page; a member gets the
// plain refusal.
func (s *site) denied(w http.ResponseWriter, p *page, what string) {
	p.Title = "No access"
	d := errorData{Status: http.StatusForbidden, Heading: "No access"}
	if p.Viewer.UserID == 0 {
		d.Message = "Sign in to see " + what + "."
		d.Href, d.HrefLabel = s.loginHref(p.Return), "Sign in"
	} else {
		d.Message = "You do not have the right to see " + what + "."
	}
	p.Data = d
	s.render(w, http.StatusForbidden, "error.html", p)
}

// profileFailed answers the error of [board.Profile] and reports whether it
// did: 410 for a gone user, 403 for a viewer who may not see profiles, 404 for a
// missing user. The old links to a profile use it too, so the rule is the same.
func (s *site) profileFailed(w http.ResponseWriter, r *http.Request, p *page, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, board.ErrGone):
		s.failLink(w, r, http.StatusGone, "Gone", "This account no longer exists.", "", "")
	case errors.Is(err, board.ErrForbidden):
		s.denied(w, p, "member profiles")
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
	default:
		s.serverError(w, r, err)
	}
	return true
}

// profile serves GET /u/{id}. A gone user answers 410.
func (s *site) profile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.begin(w, r, "", "members")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pp, err := board.Profile(r.Context(), s.pool, p.Viewer, id, s.opt.Now())
	if s.profileFailed(w, r, p, err) {
		return
	}
	d := profileData{ProfilePage: pp, WebsiteText: websiteText(pp.Website)}
	if p.Viewer.Perms.Has("account.search", 0) {
		d.FindHref = s.path("search") + "?" + url.Values{"author": {pp.Card.Name}}.Encode()
	}
	if pp.CanMessage {
		d.MessageHref = s.messageHref(pp.Card.Name)
	}
	if pp.Signature != nil {
		html, err := s.renderTree(r, p, pp.Signature, true)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.SigHTML = html
	}
	p.Title = pp.Card.Name
	p.Data = d
	s.render(w, http.StatusOK, "profile.html", p)
}

// renderTree renders a stored tree to HTML with the smilies of the board. It
// adds what the embeds of the tree need to the policy and the scripts of p. A
// signature passes noEmbeds: its embed links render as plain links.
func (s *site) renderTree(r *http.Request, p *page, tree *markup.Node, noEmbeds bool) (template.HTML, error) {
	smileys, err := board.SmileyLookup(r.Context(), s.pool)
	if err != nil {
		return "", err
	}
	out := markup.Render(tree, markup.RenderContext{Smiley: smileys, BasePath: s.base, NoEmbeds: noEmbeds})
	p.addRendered(out)
	return out.HTML, nil
}

// sortHead is one sortable column head of the member list.
type sortHead struct {
	Label, Href string
	// Dir is the aria-sort value of the current column, and empty for the others.
	Dir string
}

// membersData is the data of members.html.
type membersData struct {
	Members []board.UserCard
	Heads   []sortHead
	Pager   *pager
}

// memberHref returns the link to page n of the member list in the given order.
// The name order and page 1 have no parameter.
func (s *site) memberHref(sort string, n int) string {
	var q []string
	if sort != "name" {
		q = append(q, "sort="+sort)
	}
	if n > 1 {
		q = append(q, "page="+strconv.Itoa(n))
	}
	href := s.path("members")
	if len(q) > 0 {
		href += "?" + strings.Join(q, "&")
	}
	return href
}

// members serves GET /members.
func (s *site) members(w http.ResponseWriter, r *http.Request) {
	page, err := board.ParsePage(r.URL.Query().Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.begin(w, r, "Members", "members")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	mp, err := board.Members(r.Context(), s.pool, p.Viewer, r.URL.Query().Get("sort"), page)
	switch {
	case errors.Is(err, board.ErrForbidden):
		s.denied(w, p, "the member list")
		return
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	d := membersData{
		Members: mp.Members,
		Pager:   newPagerWith(func(n int) string { return s.memberHref(mp.Sort, n) }, mp.Page, mp.Pages),
	}
	for _, h := range []struct{ sort, label, dir string }{
		{"name", "Member", "ascending"}, {"posts", "Posts", "descending"}, {"joined", "Joined", "ascending"},
	} {
		head := sortHead{Label: h.label, Href: s.memberHref(h.sort, 1)}
		if h.sort == mp.Sort {
			head.Dir = h.dir
		}
		d.Heads = append(d.Heads, head)
	}
	p.Data = d
	s.render(w, http.StatusOK, "members.html", p)
}

// team serves GET /team.
func (s *site) team(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "Team", "team")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	groups, err := board.Team(r.Context(), s.pool)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = groups
	s.render(w, http.StatusOK, "team.html", p)
}

// pageData is the data of page.html.
type pageData struct {
	board.StaticPage
	HTML template.HTML
}

// staticPage serves GET /pages/{slug}.
func (s *site) staticPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sp, err := board.Page(r.Context(), s.pool, r.PathValue("slug"))
	switch {
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	html, err := s.renderTree(r, p, sp.Tree, false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Title = cmp.Or(sp.Title, sp.Slug)
	p.Data = pageData{StaticPage: sp, HTML: html}
	s.render(w, http.StatusOK, "page.html", p)
}

package web

import "strconv"

// pager is the data of the page links under a list.
type pager struct {
	Page, Pages int
	// Prev and Next are the links to the neighbors, or empty.
	Prev, Next string
	Items      []pageItem
	// Label names the nav element, so that two pagers on a page differ.
	Label string
}

// pageItem is one entry of the page links: a page, or a gap.
type pageItem struct {
	Num     int
	Href    string
	Current bool
	// Gap is true for the dots between two runs of pages.
	Gap bool
}

// pageHref returns the link to page n of the list at base. Page 1 has no query.
func pageHref(base string, n int) string {
	if n <= 1 {
		return base
	}
	return base + "?page=" + strconv.Itoa(n)
}

// newPager builds the page links for page of pages at base, a site path. It
// returns nil when there is one page or none. It links the first and last
// pages, and two pages on each side of the current one.
func newPager(base string, page, pages int) *pager {
	if pages <= 1 {
		return nil
	}
	p := &pager{Page: page, Pages: pages}
	if page > 1 {
		p.Prev = pageHref(base, page-1)
	}
	if page < pages {
		p.Next = pageHref(base, page+1)
	}
	last := 0
	for n := 1; n <= pages; n++ {
		if n != 1 && n != pages && (n < page-2 || n > page+2) {
			continue
		}
		if last != 0 && n != last+1 {
			p.Items = append(p.Items, pageItem{Gap: true})
		}
		p.Items = append(p.Items, pageItem{Num: n, Href: pageHref(base, n), Current: n == page})
		last = n
	}
	return p
}

// pagerAt returns a copy of p named label. A nil pager stays nil.
func pagerAt(p *pager, label string) *pager {
	if p == nil {
		return nil
	}
	c := *p
	c.Label = label
	return &c
}

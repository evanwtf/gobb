package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// parseTemplates parses the layout with each page template. Every page gets
// its own set, so two pages may both define "content".
func (s *site) parseTemplates() error {
	funcs := template.FuncMap{
		"path": s.path,
		"time": func(t time.Time, c clock) template.HTML { return timeTag(t, c, false) },
		"date": func(t time.Time, c clock) template.HTML { return timeTag(t, c, true) },
		"rows": func(rows []*board.Forum, v board.Viewer, c clock) forumRows {
			return forumRows{Rows: rows, Clock: c, Member: v.UserID != 0}
		},
		"initial":       initial,
		"textareaValue": textareaValue,
		"postItem":      postItem,
		"plural":        plural,
		"size":          size,
		"num":           num,
		"pagerAt":       pagerAt,
		"markForm": func(action, label string, p *page) markFormData {
			return markFormData{Action: action, Label: label, CSRF: p.CSRF, Return: p.Return}
		},
	}
	s.pages = map[string]*template.Template{}
	for _, name := range pageFiles {
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", "templates/"+name)
		if err != nil {
			return err
		}
		s.pages[name] = t
	}
	return nil
}

// markFormData is the data of the mark-read form partial.
type markFormData struct {
	Action, Label, CSRF, Return string
}

// index serves the board index.
func (s *site) index(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "", "index")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	forums, err := board.Index(r.Context(), s.pool, p.Viewer)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	birthdays, err := board.Birthdays(r.Context(), s.pool, p.Viewer, s.opt.Now())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = indexData{Groups: groupForums(forums), Birthdays: birthdays}
	s.render(w, http.StatusOK, "index.html", p)
}

// indexData is the data of index.html.
type indexData struct {
	Groups []*forumGroup
	// Birthdays lists the members whose birthday is today. It is empty for a
	// viewer who may not see profiles.
	Birthdays []board.BirthdayRow
}

// forumGroup is a bar and the rows under it.
type forumGroup struct {
	// Title is the text of the bar. It is empty for the forums that have no
	// category above them.
	Title string
	// Forum is the category, or nil for the untitled group.
	Forum *board.Forum
	Rows  []*board.Forum
}

// groupForums turns the top-level forums into bars. A category becomes a bar
// with its children as rows. Forums with no category above them gather under one
// bar with no title, placed where the first of them stands.
func groupForums(top []*board.Forum) []*forumGroup {
	var groups []*forumGroup
	var loose *forumGroup
	for _, f := range top {
		if f.Kind == "category" {
			groups = append(groups, &forumGroup{Title: f.Name, Forum: f, Rows: f.Children})
			continue
		}
		if loose == nil {
			loose = &forumGroup{}
			groups = append(groups, loose)
		}
		loose.Rows = append(loose.Rows, f)
	}
	return groups
}

// forumRows is the data of the forum table partial: the rows, the zone in
// which it shows times, and whether the viewer is a member. A forum that the
// viewer may list and not read shows "Members only" to a guest and "No access"
// to a member, for whom signing in would change nothing.
type forumRows struct {
	Rows   []*board.Forum
	Clock  clock
	Member bool
}

// plural returns "1 thing" or "2 things". A count over 999 has separators.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return num(n) + " " + many
}

// num writes an integer with a comma between each group of three digits:
// 1076250 becomes "1,076,250". It takes any integer type, so a template needs no
// conversion.
func num(v any) string {
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int32:
		n = int64(x)
	case int64:
		n = x
	default:
		return fmt.Sprint(v)
	}
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits
}

// size writes a byte count in the unit that fits: "512 B", "1.5 KB", "3.2 MB".
func size(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	v, suffix := float64(n), ""
	for _, s := range []string{"KB", "MB", "GB", "TB"} {
		v /= unit
		suffix = s
		if v < unit {
			break
		}
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + suffix
}

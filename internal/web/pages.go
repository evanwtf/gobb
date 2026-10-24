package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/board"
)

// timeLayout and dateLayout are the display formats of times and dates.
const (
	timeLayout = "Jan 2, 2006, 3:04 pm"
	dateLayout = "Jan 2, 2006"
)

// parseTemplates parses the layout with each page template. Every page gets
// its own set, so two pages may both define "content".
func (s *site) parseTemplates() error {
	funcs := template.FuncMap{
		"path": s.path,
		"time": func(t time.Time, loc *time.Location) template.HTML {
			return timeTag(t, loc, timeLayout)
		},
		"date": func(t time.Time, loc *time.Location) template.HTML {
			return timeTag(t, loc, dateLayout)
		},
		"rows":    func(rows []*board.Forum, loc *time.Location) forumRows { return forumRows{Rows: rows, Loc: loc} },
		"initial": initial,
		"plural":  plural,
		"size":    size,
		"num":     num,
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

// timeTag writes t in loc as a <time> element. The datetime attribute holds the
// RFC 3339 time in UTC. The arguments are numbers and a layout, so the result
// holds nothing that needs escaping.
func timeTag(t time.Time, loc *time.Location, layout string) template.HTML {
	if loc == nil {
		loc = time.UTC
	}
	return template.HTML(`<time datetime="` + t.UTC().Format(time.RFC3339) + `">` +
		t.In(loc).Format(layout) + `</time>`)
}

// index serves the board index.
func (s *site) index(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(r, "", "index")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	forums, err := board.Index(r.Context(), s.pool, p.Viewer)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = indexData{Groups: groupForums(forums)}
	s.render(w, http.StatusOK, "index.html", p)
}

// indexData is the data of index.html.
type indexData struct {
	Groups []*forumGroup
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

// forumRows is the data of the forum table partial: the rows and the zone in
// which it shows times.
type forumRows struct {
	Rows []*board.Forum
	Loc  *time.Location
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
